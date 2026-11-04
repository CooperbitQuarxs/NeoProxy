package neopool

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopigateway"
)

func sourceSession(c *gin.Context) bool {
	if _, ok := session(c); !ok {
		return false
	}
	if _, err := sourceAdmin(c.Request.Context()); err != nil {
		poolError(c, http.StatusForbidden, "organization administrator required to manage inference sources")
		return false
	}
	return true
}

func (h *HTTP) sourceList(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	list, err := h.pool.Sources(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *HTTP) sourceSave(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	var body struct {
		SourceInput
		// A connection link is the "paste and go" path: enabled unless the
		// request says otherwise. URL + bearer keeps its explicit default.
		Enabled *bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&body) != nil {
		poolError(c, http.StatusBadRequest, "send a connection link, or a source label, URL, owner and gateway bearer")
		return
	}
	in := body.SourceInput
	in.Enabled = body.Enabled != nil && *body.Enabled || body.Enabled == nil && strings.TrimSpace(in.ConnectionLink) != ""
	v, err := h.pool.SaveSource(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrForbidden):
			respondError(c, err)
		case errors.Is(err, neopigateway.ErrInvalidLink):
			poolError(c, http.StatusBadRequest, err.Error())
		default:
			poolError(c, http.StatusBadRequest, "invalid source configuration; use a valid URL, active account owner and a bearer (required for a new or changed URL)")
		}
		return
	}
	if v.Enabled {
		_ = h.pool.SyncSource(c.Request.Context(), v.ID)
	}
	h.sourceResult(c, v.ID)
}

func (h *HTTP) sourceSync(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	if err := h.pool.SyncSource(c.Request.Context(), c.Param("id")); err != nil {
		// The durable safe status describes failed discovery without exposing the
		// gateway response or bearer. Return it so the dashboard can offer retry.
		list, listErr := h.pool.Sources(c.Request.Context())
		if listErr != nil {
			respondError(c, listErr)
			return
		}
		for _, v := range list {
			if v.ID == c.Param("id") {
				c.JSON(http.StatusOK, v)
				return
			}
		}
		poolError(c, http.StatusNotFound, "source is unavailable")
		return
	}
	h.sourceResult(c, c.Param("id"))
}

func (h *HTTP) sourceResult(c *gin.Context, id string) {
	list, err := h.pool.Sources(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	for _, v := range list {
		if v.ID == id {
			c.JSON(http.StatusOK, v)
			return
		}
	}
	poolError(c, http.StatusNotFound, "source is unavailable")
}

func (h *HTTP) sourceDelete(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	if err := h.pool.DeleteSource(c.Request.Context(), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
