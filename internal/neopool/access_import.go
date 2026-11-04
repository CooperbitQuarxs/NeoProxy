package neopool

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiimport"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// maxImportBatch bounds one import request; a vault holds a few dozen rows.
const maxImportBatch = 200

func (h *HTTP) registerNeoPiImport(group *gin.RouterGroup) {
	group.GET("/access/import/neopi", h.neoPiImportList)
	group.POST("/access/import/neopi", h.neoPiImport)
	group.POST("/access/:id/reimport", h.neoPiReimport)
}

// adminSession requires an administrator: importing reads a vault on the
// server's own filesystem, which only the operator may do.
func adminSession(c *gin.Context) (neo.Principal, bool) {
	p, ok := session(c)
	if !ok {
		return p, false
	}
	if p.Role != neo.RoleAdmin {
		poolError(c, http.StatusForbidden, "Only an administrator can import from a NeoPi vault on this server.")
		return p, false
	}
	return p, true
}

func neoPiProfileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, neopiimport.ErrNotProfile):
		poolError(c, http.StatusBadRequest, "That folder is not a NeoPi profile: it must be an absolute path to a directory holding NeoPi's agent.db. NeoPi keeps its vault in ~/.omp/agent unless PI_CODING_AGENT_DIR points elsewhere.")
	case errors.Is(err, neopiimport.ErrCredentialNotFound):
		poolError(c, http.StatusNotFound, "NeoPi no longer has this credential; it may have been removed or replaced there. Sign in separately, or import the current one from the vault.")
	default:
		log.WithError(err).Error("NeoProxy access: reading a NeoPi vault failed")
		respondError(c, err)
	}
}

func (h *HTTP) neoPiImportList(c *gin.Context) {
	if _, ok := adminSession(c); !ok {
		return
	}
	profile, err := neopiimport.ResolveProfile(c.Query("profile"))
	if err != nil {
		neoPiProfileError(c, err)
		return
	}
	creds, err := neopiimport.Read(c.Request.Context(), profile)
	if err != nil {
		neoPiProfileError(c, err)
		return
	}
	now := time.Now()
	index := h.importIndex(profile)
	out := make([]neopiimport.Candidate, 0, len(creds))
	for _, cred := range creds {
		candidate := neopiimport.Describe(cred, now)
		if itemID := index.lookup(cred, now); itemID != "" {
			candidate.AlreadyImported = &itemID
		}
		out = append(out, candidate)
	}
	c.JSON(http.StatusOK, gin.H{"profile": profile, "credentials": out})
}

type neoPiImportFailure struct {
	CredentialID string `json:"credentialId"`
	Error        string `json:"error"`
}

func (h *HTTP) neoPiImport(c *gin.Context) {
	p, ok := adminSession(c)
	if !ok {
		return
	}
	var body struct {
		Profile       string   `json:"profile"`
		CredentialIDs []string `json:"credentialIds"`
		OwnerID       string   `json:"ownerId"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.CredentialIDs) == 0 || len(body.CredentialIDs) > maxImportBatch {
		poolError(c, http.StatusBadRequest, "Send credentialIds: the NeoPi credentials to import (1 to 200), as listed by GET /v8/neo/access/import/neopi.")
		return
	}
	if h.management == nil {
		poolError(c, http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
		return
	}
	ownerID, err := h.principalOwner(p, body.OwnerID)
	if err != nil {
		respondAccessError(c, err)
		return
	}
	profile, err := neopiimport.ResolveProfile(body.Profile)
	if err != nil {
		neoPiProfileError(c, err)
		return
	}
	creds, err := neopiimport.Read(c.Request.Context(), profile)
	if err != nil {
		neoPiProfileError(c, err)
		return
	}
	byID := make(map[string]neopiimport.Credential, len(creds))
	for _, cred := range creds {
		byID[cred.ID] = cred
	}
	ctx := c.Request.Context()
	now := time.Now()
	index := h.importIndex(profile)
	items := make([]AccessItem, 0, len(body.CredentialIDs))
	failed := make([]neoPiImportFailure, 0)
	seen := make(map[string]bool, len(body.CredentialIDs))
	for _, id := range body.CredentialIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		cred, found := byID[id]
		if !found {
			failed = append(failed, neoPiImportFailure{CredentialID: id, Error: "NeoPi has no credential with this id in that vault."})
			continue
		}
		if itemID := index.lookup(cred, now); itemID != "" {
			message := "This key is already in the pool."
			if neopiimport.Describe(cred, now).Kind == neopiimport.KindSubscription {
				message = "Already imported. Use Re-import on that provider access to pull NeoPi's current token."
			}
			failed = append(failed, neoPiImportFailure{CredentialID: id, Error: message})
			continue
		}
		item, errImport := h.importOne(ctx, p, profile, cred, ownerID, now)
		if errImport != nil {
			failed = append(failed, neoPiImportFailure{CredentialID: id, Error: importErrorMessage(errImport)})
			continue
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": gin.H{"message": "Nothing was imported. " + failed[0].Error}, "items": items, "failed": failed})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "failed": failed})
}

func (h *HTTP) importOne(ctx context.Context, p neo.Principal, profile string, cred neopiimport.Credential, ownerID string, now time.Time) (AccessItem, error) {
	if neopiimport.Describe(cred, now).Kind == neopiimport.KindAPIKey {
		key, err := neopiimport.BuildAPIKey(cred, now)
		if err != nil {
			return AccessItem{}, err
		}
		return h.AddAPIKey(ctx, p, APIKeyRequest{Provider: key.Provider, APIKey: key.APIKey, BaseURL: key.BaseURL, Label: key.Label, OwnerID: ownerID})
	}
	file, err := neopiimport.BuildAuthFile(cred, profile, now, now)
	if err != nil {
		return AccessItem{}, err
	}
	// Created under the administrator's own login ownership, then assigned,
	// exactly like an API key added on someone's behalf.
	a, err := h.management.SaveAccountAuthFile(coreauth.WithAccountLoginOwner(ctx, p.UserID), file.FileName, file.Data, true)
	if err != nil {
		return AccessItem{}, err
	}
	if err = h.assignNew(ctx, p, a, ownerID, "", nil, ""); err != nil {
		return AccessItem{}, err
	}
	return h.AccessItemByEngineID(ctx, p, a.ID)
}

// neoPiReimport re-reads the credential recorded in the item's provenance. The
// path was chosen by an administrator at import time, so its owner may also
// re-import: it can only renew what was already imported for them.
func (h *HTTP) neoPiReimport(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	if h.management == nil {
		poolError(c, http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
		return
	}
	current, found := h.engineAuthForAccess(c.Param("id"))
	if !found {
		poolError(c, http.StatusNotFound, "This provider access no longer exists. Refresh the page to see the current pool.")
		return
	}
	if p.Role != neo.RoleAdmin {
		item, err := h.AccessItemByEngineID(c.Request.Context(), p, current.ID)
		if err != nil || p.Role != neo.RoleOwner || item.OwnerID != p.UserID {
			poolError(c, http.StatusForbidden, "Only an administrator or this access's contributor can re-import it.")
			return
		}
	}
	provenance, imported := coreauth.ImportedFromNeoPi(current)
	if !imported {
		poolError(c, http.StatusBadRequest, "This provider access was not imported from NeoPi, so there is nothing to re-import.")
		return
	}
	profile, err := neopiimport.ResolveProfile(provenance.Profile)
	if err != nil {
		neoPiProfileError(c, err)
		return
	}
	cred, err := neopiimport.Find(c.Request.Context(), profile, provenance.CredentialID)
	if err != nil {
		neoPiProfileError(c, err)
		return
	}
	now := time.Now()
	file, err := neopiimport.BuildAuthFile(cred, profile, now, now)
	if err != nil {
		poolError(c, http.StatusUnprocessableEntity, importErrorMessage(err))
		return
	}
	if file.Provider != current.Provider || file.FileName != current.FileName {
		poolError(c, http.StatusConflict, "NeoPi's credential "+cred.ID+" is now a different account or provider. Import it as new provider access instead.")
		return
	}
	a, err := h.management.SaveAccountAuthFile(c.Request.Context(), current.FileName, file.Data, false)
	if err != nil {
		log.WithError(err).Error("NeoProxy access: re-importing a NeoPi credential failed")
		respondAccessError(c, err)
		return
	}
	item, err := h.AccessItemByEngineID(c.Request.Context(), p, a.ID)
	if err != nil {
		respondAccessError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item})
}

func importErrorMessage(err error) string {
	var accessError *AccessError
	switch {
	case errors.As(err, &accessError):
		return accessError.Message
	case errors.Is(err, neopiimport.ErrNotImportable):
		return strings.TrimPrefix(err.Error(), neopiimport.ErrNotImportable.Error()+": ")
	case errors.Is(err, coreauth.ErrAccountKeyConflict):
		return "This key is already in the pool."
	case errors.Is(err, ErrForbidden):
		return "Another person already owns this account in the pool."
	default:
		log.WithError(err).Error("NeoProxy access: importing a NeoPi credential failed")
		return "NeoProxy could not save this credential; see the server log."
	}
}

// importIndexMap finds credentials of a NeoPi profile already in the pool:
// subscriptions by import provenance, API keys by identical key.
type importIndexMap struct {
	byCredential map[string]string
	byAPIKey     map[string]string
}

func (h *HTTP) importIndex(profile string) importIndexMap {
	index := importIndexMap{byCredential: map[string]string{}, byAPIKey: map[string]string{}}
	if h.pool == nil || h.pool.manager == nil {
		return index
	}
	for _, a := range h.pool.manager.List() {
		if provenance, ok := coreauth.ImportedFromNeoPi(a); ok && provenance.Profile == profile {
			index.byCredential[provenance.CredentialID] = AccountID(a.ID)
		}
		if key := strings.TrimSpace(a.Attributes["api_key"]); key != "" {
			index.byAPIKey[key] = AccountID(a.ID)
		}
	}
	return index
}

func (i importIndexMap) lookup(cred neopiimport.Credential, now time.Time) string {
	if id, ok := i.byCredential[cred.ID]; ok {
		return id
	}
	if key, err := neopiimport.BuildAPIKey(cred, now); err == nil {
		return i.byAPIKey[key.APIKey]
	}
	return ""
}
