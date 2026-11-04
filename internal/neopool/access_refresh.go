package neopool

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// fileItemRefusal explains why an item has no engine auth file to refresh or
// download (verb), or returns "" when it has one.
func fileItemRefusal(a *coreauth.Auth, kind, verb string) string {
	switch {
	case kind == "imported" || coreauth.IsRefreshLocked(a):
		return "This sign-in was imported from NeoPi with refresh locked: NeoProxy never refreshes it or hands out its tokens. Re-import it from NeoPi, or sign in separately."
	case kind == "neopi":
		return "This account is shared from NeoPi. NeoPi keeps its credentials, so there is nothing here to " + verb + "."
	case kind == "apiKey":
		return "This is an API key in the engine configuration, not a sign-in file, so there is nothing to " + verb + "."
	case credentialFilePath(a, kind) == "":
		return "This provider access has no credential file to " + verb + "."
	}
	return ""
}

// accessRefresh renews a sign-in now through the engine's existing forced
// refresh, the same path as the management "refresh auth file" action.
func (h *HTTP) accessRefresh(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	if p.Role != neo.RoleAdmin && !(owner == p.UserID && p.Role == neo.RoleOwner) {
		poolError(c, http.StatusForbidden, "Only the contributor or an administrator can refresh this sign-in.")
		return
	}
	if refusal := fileItemRefusal(a, accessKind(a), "refresh"); refusal != "" {
		poolError(c, http.StatusConflict, refusal)
		return
	}
	if _, err := h.pool.manager.ForceRefreshAuth(c.Request.Context(), a.ID); err != nil {
		log.WithError(err).Warn("NeoProxy access: forced sign-in refresh failed")
		poolError(c, http.StatusBadGateway, "The provider did not renew this sign-in. If it keeps failing, sign in again to replace it.")
		return
	}
	h.pool.mu.Lock()
	h.pool.notifyLocked()
	h.pool.mu.Unlock()
	h.respondItem(c, p, a.ID, http.StatusOK)
}

// accessDownload streams the engine auth file. Administrator-only: the file
// holds the sign-in's tokens.
func (h *HTTP) accessDownload(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	if p.Role != neo.RoleAdmin {
		poolError(c, http.StatusForbidden, "Only an administrator can download credential files.")
		return
	}
	a, _, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	kind := accessKind(a)
	if refusal := fileItemRefusal(a, kind, "download"); refusal != "" {
		poolError(c, http.StatusConflict, refusal)
		return
	}
	path := credentialFilePath(a, kind)
	data, err := os.ReadFile(path)
	if err != nil {
		poolError(c, http.StatusNotFound, "The credential file is no longer on disk. Refresh the page to see the current pool.")
		return
	}
	name := filepath.Base(path)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ReplaceAll(name, `"`, "")))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/json", data)
}
