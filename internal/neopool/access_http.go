package neopool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/snowflake"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *HTTP) registerAccess(group *gin.RouterGroup) {
	group.GET("/access", h.accessList)
	group.GET("/access/providers", h.accessProviders)
	group.POST("/access/api-key", h.accessAPIKey)
	group.POST("/access/file", h.accessFile)
	group.PATCH("/access/:id", h.accessPatch)
	group.DELETE("/access/:id", h.accessDelete)
	group.POST("/access/:id/refresh", h.accessRefresh)
	group.POST("/access/:id/test", h.accessTest)
	group.GET("/access/:id/test/config", h.accessTestConfig)
	group.PUT("/access/:id/test/config", h.accessTestConfig)
	group.DELETE("/access/:id/test/config", h.accessTestConfig)
	group.GET("/access/:id/file", h.accessDownload)
	group.POST("/access/:id/quota/refresh", h.accessQuotaRefresh)
}

func respondAccessError(c *gin.Context, err error) {
	var accessError *AccessError
	switch {
	case errors.As(err, &accessError):
		poolError(c, accessError.Status, accessError.Message)
	case errors.Is(err, coreauth.ErrAccountKeyConflict):
		poolError(c, http.StatusConflict, "This key is already in the pool. Each key can be added once.")
	case errors.Is(err, coreauth.ErrAccountKeyInvalid):
		poolError(c, http.StatusBadRequest, "The key or provider is not valid. Choose a provider from the list and paste the whole key.")
	case errors.Is(err, coreauth.ErrAccountNotFound), errors.Is(err, ErrUnavailable):
		poolError(c, http.StatusNotFound, "This provider access no longer exists. Refresh the page to see the current pool.")
	case errors.Is(err, coreauth.ErrAccountUnsupported):
		poolError(c, http.StatusConflict, "This change is not possible for this kind of provider access.")
	default:
		respondError(c, err)
	}
}

func (h *HTTP) accessList(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	list, err := h.AccessList(c.Request.Context(), p)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *HTTP) accessProviders(c *gin.Context) {
	if _, ok := session(c); !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": providerCatalog})
}

// accessTarget resolves :id for a mutation and enforces who may change it.
func (h *HTTP) accessTarget(c *gin.Context, p neo.Principal) (*coreauth.Auth, string, bool) {
	id := c.Param("id")
	a, found := h.engineAuthForAccess(id)
	if !found {
		poolError(c, http.StatusNotFound, "This provider access no longer exists. Refresh the page to see the current pool.")
		return nil, "", false
	}
	var owner string
	_ = h.pool.db.QueryRowContext(c.Request.Context(), `SELECT COALESCE(owner_id,'') FROM neo_accounts WHERE id=?`, id).Scan(&owner)
	if !visibleTo(p, owner) {
		poolError(c, http.StatusNotFound, "This provider access no longer exists. Refresh the page to see the current pool.")
		return nil, "", false
	}
	return a, owner, true
}

func (h *HTTP) accessPatch(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if c.ShouldBindJSON(&body) != nil || len(body) == 0 {
		poolError(c, http.StatusBadRequest, "Send at least one of ownerId, planId, monthlyUSD, label or disabled.")
		return
	}
	admin := p.Role == neo.RoleAdmin
	ownerCanEdit := owner == p.UserID && p.Role == neo.RoleOwner
	for field := range body {
		switch field {
		case "ownerId", "planId", "monthlyUSD":
			if !admin {
				poolError(c, http.StatusForbidden, "Only an administrator can change the contributor or the plan.")
				return
			}
		case "label", "disabled":
			if !admin && !ownerCanEdit {
				poolError(c, http.StatusForbidden, "Only the contributor or an administrator can change this provider access.")
				return
			}
		default:
			poolError(c, http.StatusBadRequest, fmt.Sprintf("Unknown field %q. Allowed: ownerId, planId, monthlyUSD, label, disabled.", field))
			return
		}
	}
	ctx := c.Request.Context()
	id := AccountID(a.ID)
	kind := accessKind(a)
	provider := accessProviderID(a, kind)
	if _, err := h.pool.ensureAccessRow(ctx, id, a); err != nil {
		respondError(c, err)
		return
	}
	if raw, present := body["ownerId"]; present {
		var ownerID string
		if json.Unmarshal(raw, &ownerID) != nil || ownerID == "" {
			poolError(c, http.StatusBadRequest, "ownerId must be the ID of a person in this office.")
			return
		}
		if err := h.pool.AssignOwner(ctx, id, ownerID); err != nil {
			if errors.Is(err, ErrUnavailable) {
				poolError(c, http.StatusBadRequest, "No person with that ownerId exists. Pick someone from the people list.")
				return
			}
			respondError(c, err)
			return
		}
	}
	if raw, present := body["planId"]; present {
		var planID *string
		if json.Unmarshal(raw, &planID) != nil || (planID != nil && !validPlanID(provider, *planID)) {
			poolError(c, http.StatusBadRequest, "planId must be one of this provider's plans, \"default\", \"apiKey\", or null to return to the detected plan.")
			return
		}
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET plan_id=? WHERE account_id=?`, planID, id); err != nil {
			respondError(c, err)
			return
		}
	}
	if raw, present := body["monthlyUSD"]; present {
		var monthly *float64
		if json.Unmarshal(raw, &monthly) != nil || (monthly != nil && (math.IsNaN(*monthly) || *monthly < 0 || *monthly > 1e6)) {
			poolError(c, http.StatusBadRequest, "monthlyUSD must be a number from 0 to 1000000, or null to use the plan's value.")
			return
		}
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET monthly_usd=? WHERE account_id=?`, monthly, id); err != nil {
			respondError(c, err)
			return
		}
	}
	if raw, present := body["label"]; present {
		var label *string
		if json.Unmarshal(raw, &label) != nil || (label != nil && len(strings.TrimSpace(*label)) > 120) {
			poolError(c, http.StatusBadRequest, "label must be text of at most 120 characters, or null for the automatic label.")
			return
		}
		if label != nil && strings.TrimSpace(*label) == "" {
			label = nil
		}
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET label=? WHERE account_id=?`, label, id); err != nil {
			respondError(c, err)
			return
		}
	}
	if raw, present := body["disabled"]; present {
		var disabled bool
		if json.Unmarshal(raw, &disabled) != nil {
			poolError(c, http.StatusBadRequest, "disabled must be true or false.")
			return
		}
		if err := h.setAccessDisabled(ctx, a, kind, disabled); err != nil {
			respondAccessError(c, err)
			return
		}
	}
	h.pool.mu.Lock()
	h.pool.notifyLocked()
	h.pool.mu.Unlock()
	h.respondItem(c, p, a.ID, http.StatusOK)
}

// setAccessDisabled pauses an item. NeoPi gateway accounts are runtime-only, so
// the pause is stored here and re-applied by every source sync.
func (h *HTTP) setAccessDisabled(ctx context.Context, a *coreauth.Auth, kind string, disabled bool) error {
	id := AccountID(a.ID)
	if kind == "neopi" {
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET disabled=? WHERE account_id=?`, disabled, id); err != nil {
			return err
		}
		updated := a.Clone()
		updated.Disabled = disabled || (a.Attributes["neopi_state"] != "" && a.Attributes["neopi_state"] != "healthy")
		if updated.Disabled {
			updated.Status = coreauth.StatusDisabled
		} else {
			updated.Status = coreauth.StatusActive
		}
		_, err := h.pool.manager.Update(coreauth.WithSkipPersist(ctx), updated)
		return err
	}
	if h.management == nil {
		return fmt.Errorf("engine is unavailable")
	}
	return h.management.SetAccountDisabled(ctx, a.ID, disabled)
}

func (h *HTTP) accessDelete(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	kind := accessKind(a)
	if p.Role != neo.RoleAdmin && !(owner == p.UserID && p.Role == neo.RoleOwner && kind != "neopi") {
		poolError(c, http.StatusForbidden, "Only the contributor or an administrator can remove this provider access.")
		return
	}
	ctx := c.Request.Context()
	id := AccountID(a.ID)
	if kind == "neopi" {
		// Detach only this account from the pool. The NeoPi source and everything
		// inside NeoPi stay untouched; the detach survives later source syncs.
		if _, err := h.pool.ensureAccessRow(ctx, id, a); err != nil {
			respondError(c, err)
			return
		}
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET detached=1 WHERE account_id=?`, id); err != nil {
			respondError(c, err)
			return
		}
		h.pool.sourceMu.Lock()
		registry.GetGlobalRegistry().UnregisterClient(a.ID)
		h.pool.manager.Remove(ctx, a.ID)
		h.pool.sourceMu.Unlock()
	} else {
		if h.management == nil {
			poolError(c, http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
			return
		}
		if err := h.management.DeleteAccountCredential(ctx, a.ID); err != nil {
			respondAccessError(c, err)
			return
		}
		if _, err := h.pool.db.ExecContext(ctx, `DELETE FROM neo_access WHERE account_id=?`, id); err != nil {
			respondError(c, err)
			return
		}
	}
	h.pool.mu.Lock()
	h.pool.notifyLocked()
	h.pool.mu.Unlock()
	c.Status(http.StatusNoContent)
}

// APIKeyRequest is POST /v8/neo/access/api-key. Custom upstream addresses
// require administrator authority; Token Plan also accepts a selected region.
type APIKeyRequest struct {
	Provider   string   `json:"provider"`
	APIKey     string   `json:"apiKey"`
	BaseURL    string   `json:"baseUrl"`
	Label      string   `json:"label"`
	OwnerID    string   `json:"ownerId"`
	PlanID     string   `json:"planId"`
	MonthlyUSD *float64 `json:"monthlyUSD"`
	// Cookie is QwenCloud Token Plan's optional console Cookie header, which
	// enables its quota reporting (NeoPi's credential bundle `cookie`).
	Cookie string `json:"cookie"`
	// CustomBaseURL is NeoPi's "Custom" endpoint option of a regional
	// provider (administrators only). It is chosen explicitly because it
	// changes the key check even when BaseURL names a region.
	CustomBaseURL bool `json:"customBaseUrl"`
	// Fields are the provider's own details for the key, keyed by the
	// catalogue's field IDs (Snowflake's account; Cloudflare's accountId and
	// gatewayId).
	Fields map[string]string `json:"fields"`
}

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func compatSlug(text string) string {
	slug := strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return slug
}

// compatName picks the openai-compatibility entry for a key: the catalogue ID
// for known providers, and custom-<label or host> for custom servers, with a
// numeric suffix when that name already points at another upstream.
func (h *HTTP) compatName(provider, base, label string) string {
	name := provider
	if provider == "custom" {
		slug := compatSlug(label)
		if slug == "" {
			if u, err := url.Parse(base); err == nil {
				slug = compatSlug(u.Host)
			}
		}
		name = "custom-" + firstNonEmpty(slug, "server")
	}
	bases := map[string]string{}
	for _, a := range h.pool.manager.List() {
		if compat := strings.ToLower(a.Attributes["compat_name"]); compat != "" {
			bases[compat] = strings.TrimRight(a.Attributes["base_url"], "/")
		}
	}
	candidate := name
	for n := 2; ; n++ {
		existing, taken := bases[candidate]
		if !taken || existing == base {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
}

func (h *HTTP) principalOwner(p neo.Principal, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == p.UserID {
		return p.UserID, nil
	}
	if p.Role != neo.RoleAdmin {
		return "", accessErr(http.StatusForbidden, "Only an administrator can add provider access on someone else's behalf.")
	}
	return requested, nil
}

// assignNew completes ownership and the optional plan after a credential was
// created under the caller's own login ownership.
func (h *HTTP) assignNew(ctx context.Context, p neo.Principal, a *coreauth.Auth, ownerID, planID string, monthly *float64, label string) error {
	id := AccountID(a.ID)
	if ownerID != p.UserID {
		if err := h.pool.AssignOwner(ctx, id, ownerID); err != nil {
			if errors.Is(err, ErrUnavailable) {
				return accessErr(http.StatusBadRequest, "The access was added for you, but no person with that ownerId exists. Reassign it from the pool.")
			}
			return err
		}
	}
	if _, err := h.pool.ensureAccessRow(ctx, id, a); err != nil {
		return err
	}
	if planID != "" {
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET plan_id=? WHERE account_id=?`, planID, id); err != nil {
			return err
		}
	}
	if monthly != nil {
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET monthly_usd=? WHERE account_id=?`, *monthly, id); err != nil {
			return err
		}
	}
	if label = strings.TrimSpace(label); label != "" {
		if len(label) > 120 {
			label = label[:120]
		}
		if _, err := h.pool.db.ExecContext(ctx, `UPDATE neo_access SET label=? WHERE account_id=?`, label, id); err != nil {
			return err
		}
	}
	h.pool.mu.Lock()
	h.pool.notifyLocked()
	h.pool.mu.Unlock()
	return nil
}

// AddAPIKey validates a pay-as-you-go key by listing the provider's models, then
// saves it through the engine configuration and assigns its contributor.
func (h *HTTP) AddAPIKey(ctx context.Context, p neo.Principal, req APIKeyRequest) (AccessItem, error) {
	if !p.Session || (p.Role != neo.RoleAdmin && p.Role != neo.RoleOwner) {
		return AccessItem{}, accessErr(http.StatusForbidden, "Only contributors and administrators can add provider access.")
	}
	if h.management == nil {
		return AccessItem{}, accessErr(http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
	}
	info, ok := catalogProvider(strings.TrimSpace(req.Provider))
	if !ok || info.APIKey == nil {
		return AccessItem{}, accessErr(http.StatusBadRequest, "%q does not take API keys here. Choose a provider from the list.", req.Provider)
	}
	key, err := normalizeNeoPiKey(info, strings.TrimSpace(req.APIKey))
	if err != nil {
		return AccessItem{}, err
	}
	if key == "" && !info.APIKey.KeyOptional {
		return AccessItem{}, accessErr(http.StatusBadRequest, "Paste the %s API key. Keys look like %s.", info.Name, info.APIKey.KeyHint)
	}
	if len(key) > 16384 || strings.ContainsAny(key, " \r\n\t") {
		return AccessItem{}, accessErr(http.StatusBadRequest, "The key contains spaces or line breaks. Paste only the key itself.")
	}
	switch info.ID {
	case snowflake.Provider:
		// Requests go to the account the credential is bound to: NeoPi's
		// structured key names it; a PAT is bound to the entered account.
		credential, _, parseErr := snowflake.ParseCredential(key)
		if parseErr != nil {
			return AccessItem{}, accessErr(http.StatusBadRequest, "%s", parseErr.Error())
		}
		accountURL := credential.AccountURL
		if account := strings.TrimSpace(req.Fields["account"]); account != "" || accountURL == "" {
			entered, accountErr := snowflake.NormalizeAccountURL(account)
			if accountErr != nil {
				return AccessItem{}, accessErr(http.StatusBadRequest, "%s", accountErr.Error())
			}
			if accountURL != "" && entered != accountURL {
				return AccessItem{}, accessErr(http.StatusBadRequest, "This Snowflake key belongs to %s, not %s.", accountURL, entered)
			}
			accountURL = entered
		}
		structured, _ := json.Marshal(map[string]string{"token": credential.Token, "enterpriseUrl": accountURL})
		key = string(structured)
	case cloudflare.Provider:
		// NeoPi's login stores the token with the account and gateway it
		// collects; a structured key already naming them keeps them.
		credential, ok := cloudflare.ParseCredential(key)
		if !ok {
			return AccessItem{}, accessErr(http.StatusBadRequest, "Paste the Cloudflare AI Gateway token.")
		}
		for _, field := range []struct {
			id, name string
			value    *string
		}{{"accountId", "account ID", &credential.AccountID}, {"gatewayId", "AI Gateway ID", &credential.GatewayID}} {
			entered := strings.TrimSpace(req.Fields[field.id])
			if entered != "" && *field.value != "" && entered != *field.value {
				return AccessItem{}, accessErr(http.StatusBadRequest, "This Cloudflare key names %s %s, not %s.", field.name, *field.value, entered)
			}
			if entered != "" {
				*field.value = entered
			}
		}
		if credential.AccountID == "" {
			return AccessItem{}, accessErr(http.StatusBadRequest, "Cloudflare account ID is required")
		}
		if credential.GatewayID == "" {
			return AccessItem{}, accessErr(http.StatusBadRequest, "Cloudflare AI Gateway ID is required")
		}
		key = cloudflare.Serialize(credential)
	}
	if req.PlanID != "" && !validPlanID(info.ID, req.PlanID) {
		return AccessItem{}, accessErr(http.StatusBadRequest, "planId %q is not a plan of %s.", req.PlanID, info.Name)
	}
	if req.MonthlyUSD != nil && (math.IsNaN(*req.MonthlyUSD) || *req.MonthlyUSD < 0 || *req.MonthlyUSD > 1e6) {
		return AccessItem{}, accessErr(http.StatusBadRequest, "monthlyUSD must be a number from 0 to 1000000.")
	}
	if (req.PlanID != "" || req.MonthlyUSD != nil) && p.Role != neo.RoleAdmin {
		return AccessItem{}, accessErr(http.StatusForbidden, "Only an administrator can set the plan or monthly value.")
	}
	ownerID, err := h.principalOwner(p, req.OwnerID)
	if err != nil {
		return AccessItem{}, err
	}
	base := catalogBaseURL(info.ID)
	var preferred regionChoice
	if info.APIKey.EditableBaseURL {
		if p.Role != neo.RoleAdmin {
			return AccessItem{}, accessErr(http.StatusForbidden, "Only an administrator can add a server by URL, because NeoProxy will send requests to it.")
		}
		// A self-hosted gateway with a known default address falls back to it;
		// a custom server has no meaningful default and must be named.
		if base = strings.TrimSpace(req.BaseURL); base == "" && info.ID != "custom" {
			base = catalogBaseURL(info.ID)
		}
		if base == "" {
			return AccessItem{}, accessErr(http.StatusBadRequest, "Enter the server's base URL, for example http://localhost:11434/v1 for Ollama.")
		}
	} else {
		preferred = regionChoice{BaseURL: strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"), Custom: req.CustomBaseURL}
		if regions := catalogRegions(info.ID); len(regions) > 0 {
			// A URL that is not one of the regions is the Custom option too.
			preferred.Custom = preferred.Custom || (preferred.BaseURL != "" && !slices.Contains(regions, preferred.BaseURL))
			if preferred.Custom {
				if p.Role != neo.RoleAdmin {
					return AccessItem{}, accessErr(http.StatusForbidden, "Only an administrator can add a server by URL, because NeoProxy will send requests to it.")
				}
				u, parseErr := url.Parse(preferred.BaseURL)
				if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return AccessItem{}, accessErr(http.StatusBadRequest, "Enter a valid custom %s base URL.", info.Name)
				}
			}
		} else {
			preferred = regionChoice{}
		}
	}
	base, models, routes, err := validateCatalogKey(ctx, info, strings.TrimRight(base, "/"), preferred, key)
	if err != nil {
		return AccessItem{}, err
	}
	cookie, err := neoPiConsoleCookie(info, req.Cookie, base)
	if err != nil {
		return AccessItem{}, err
	}
	spec := coreauth.AccountKey{Provider: info.engine, APIKey: key}
	if info.engine == "" {
		spec = coreauth.AccountKey{Provider: "openai-compatibility", APIKey: key, BaseURL: base, CompatName: h.compatName(info.ID, base, req.Label), Models: models,
			Dialect: info.dialect, SessionHeader: info.sessionHeader, ModelRoutes: accountModelRoutes(routes),
			RequestScopedErrors: info.upstreamErrors, Headers: info.headers, ConsoleCookie: cookie}
	}
	record, err := h.management.CreateAccountAPIKey(coreauth.WithAccountLoginOwner(ctx, p.UserID), spec)
	if err != nil {
		return AccessItem{}, err
	}
	if err = h.assignNew(ctx, p, record, ownerID, req.PlanID, req.MonthlyUSD, req.Label); err != nil {
		return AccessItem{}, err
	}
	return h.AccessItemByEngineID(ctx, p, record.ID)
}

func (h *HTTP) accessAPIKey(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	var req APIKeyRequest
	if c.ShouldBindJSON(&req) != nil {
		poolError(c, http.StatusBadRequest, "Send JSON with provider and apiKey.")
		return
	}
	item, err := h.AddAPIKey(c.Request.Context(), p, req)
	if err != nil {
		var accessError *AccessError
		if !errors.As(err, &accessError) && !errors.Is(err, coreauth.ErrAccountKeyConflict) && !errors.Is(err, coreauth.ErrAccountKeyInvalid) {
			log.WithError(err).Error("NeoProxy access: adding an API key failed")
		}
		respondAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"item": item})
}

const maxCredentialFile = 1 << 20

func (h *HTTP) accessFile(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	if p.Role != neo.RoleAdmin && p.Role != neo.RoleOwner {
		poolError(c, http.StatusForbidden, "Only contributors and administrators can add provider access.")
		return
	}
	if h.management == nil {
		poolError(c, http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCredentialFile+64<<10)
	header, err := c.FormFile("file")
	if err != nil {
		poolError(c, http.StatusBadRequest, "Attach the credential file as the multipart field \"file\". "+acceptedCredentialFiles)
		return
	}
	if header.Size > maxCredentialFile {
		poolError(c, http.StatusBadRequest, "This file is larger than 1 MiB, which is far larger than any credential file. "+acceptedCredentialFiles)
		return
	}
	src, err := header.Open()
	if err != nil {
		poolError(c, http.StatusBadRequest, "The uploaded file could not be read. Try again.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(src, maxCredentialFile+1))
	_ = src.Close()
	if err != nil {
		poolError(c, http.StatusBadRequest, "The uploaded file could not be read. Try again.")
		return
	}
	ctx := c.Request.Context()
	ownerID, err := h.principalOwner(p, c.PostForm("ownerId"))
	if err != nil {
		respondAccessError(c, err)
		return
	}
	file, err := detectCredentialFile(header.Filename, data)
	if err != nil {
		respondAccessError(c, err)
		return
	}
	if file.APIKey != "" {
		item, errKey := h.AddAPIKey(ctx, p, APIKeyRequest{Provider: file.Provider, APIKey: file.APIKey, OwnerID: ownerID})
		if errKey != nil {
			respondAccessError(c, errKey)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"item": item, "format": file.Format})
		return
	}
	record, err := h.management.SaveAccountAuthFile(coreauth.WithAccountLoginOwner(ctx, p.UserID), file.Name, file.Data, true)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			poolError(c, http.StatusConflict, "This account is already in the pool, contributed by someone else. Ask an administrator to reassign it.")
			return
		}
		if strings.Contains(err.Error(), "invalid auth file") {
			poolError(c, http.StatusBadRequest, "NeoProxy could not use this file: "+err.Error()+". "+acceptedCredentialFiles)
			return
		}
		log.WithError(err).Error("NeoProxy access: saving an uploaded credential failed")
		respondAccessError(c, err)
		return
	}
	if err = h.assignNew(ctx, p, record, ownerID, "", nil, ""); err != nil {
		respondAccessError(c, err)
		return
	}
	item, err := h.AccessItemByEngineID(ctx, p, record.ID)
	if err != nil {
		respondAccessError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"item": item, "format": file.Format})
}

func (h *HTTP) respondItem(c *gin.Context, p neo.Principal, engineAuthID string, status int) {
	item, err := h.AccessItemByEngineID(c.Request.Context(), p, engineAuthID)
	if err != nil {
		respondAccessError(c, err)
		return
	}
	c.JSON(status, gin.H{"item": item})
}
