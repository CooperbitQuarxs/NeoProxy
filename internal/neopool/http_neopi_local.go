package neopool

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopigateway"
)

// neopiConfigRoot locates the NeoPi config directory of the user NeoProxy runs
// as. Tests point it at a fake profile.
var neopiConfigRoot = neopigateway.DefaultConfigRoot

func (h *HTTP) registerNeoPiLocal(group *gin.RouterGroup) {
	group.GET("/access/neopi/local", h.neopiLocal)
	group.POST("/access/neopi/local/connect", h.neopiLocalConnect)
}

// neopiLocalView is the browser view of a same-machine NeoPi gateway. The
// token stays server-side.
type neopiLocalView struct {
	neopigateway.Local
	AlreadyConnected string `json:"alreadyConnected,omitempty"`
}

func (h *HTTP) detectNeoPiLocal(ctx context.Context) (neopiLocalView, error) {
	root, err := neopiConfigRoot()
	if err != nil {
		return neopiLocalView{Local: neopigateway.Local{Reason: "NeoProxy cannot locate its home directory to look for NeoPi."}}, nil
	}
	view := neopiLocalView{Local: neopigateway.Detect(ctx, root, nil)}
	if !view.Found {
		return view, nil
	}
	view.AlreadyConnected, err = h.pool.sourceForGateway(ctx, view.URL, view.Token())
	return view, err
}

// neopiLocal reports whether a NeoPi gateway runs on this machine for the
// user NeoProxy runs as, and whether it is already a source.
func (h *HTTP) neopiLocal(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	view, err := h.detectNeoPiLocal(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// neopiLocalConnect adds the same-machine gateway as a source with the token
// read from NeoPi's config directory. Connecting twice returns the existing source.
func (h *HTTP) neopiLocalConnect(c *gin.Context) {
	if !sourceSession(c) {
		return
	}
	var body struct {
		OwnerID string `json:"ownerId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		poolError(c, http.StatusBadRequest, "send {} or {\"ownerId\": \"...\"}")
		return
	}
	ctx := c.Request.Context()
	view, err := h.detectNeoPiLocal(ctx)
	if err != nil {
		respondError(c, err)
		return
	}
	if view.AlreadyConnected != "" {
		h.sourceResult(c, view.AlreadyConnected)
		return
	}
	if !view.Connectable() {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{"message": view.Reason}, "local": view})
		return
	}
	v, err := h.pool.SaveSource(ctx, "", SourceInput{Label: view.Name, URL: view.URL, Bearer: view.Token(), OwnerID: body.OwnerID, Enabled: true})
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			poolError(c, http.StatusBadRequest, "the owner must be an active administrator or owner")
		} else {
			respondError(c, err)
		}
		return
	}
	_ = h.pool.SyncSource(ctx, v.ID)
	h.sourceResult(c, v.ID)
}
