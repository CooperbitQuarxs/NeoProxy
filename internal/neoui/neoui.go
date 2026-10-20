// Package neoui serves the NeoProxy dashboard: a static single-page app embedded
// in the binary and mounted under /neo/. It talks to the management API from the
// browser, so it carries no server-side state of its own.
package neoui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed static
var static embed.FS

// Prefix is the URL prefix the dashboard is mounted under.
const Prefix = "/neo/"

// Register mounts the dashboard on the engine. enabled is consulted per request
// so that configuration reloads (home mode, disable-control-panel) take effect
func Register(engine *gin.Engine, enabled func() bool) {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FS(sub)
	// http.FileServer answers "index.html" with a redirect to "./", which loops
	// behind a catch-all route, so the shell is served as bytes.
	serveIndex := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	engine.GET(strings.TrimSuffix(Prefix, "/"), func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, Prefix)
	})
	engine.GET(Prefix+"*filepath", func(c *gin.Context) {
		if !enabled() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		name := path.Clean("/" + c.Param("filepath"))
		if name == "/" || name == "/index.html" {
			serveIndex(c)
			return
		}
		if f, errOpen := sub.Open(strings.TrimPrefix(name, "/")); errOpen == nil {
			_ = f.Close()
			c.Header("Cache-Control", "no-cache")
			c.FileFromFS(name, files)
			return
		}
		// Client-side routes (/neo/credentials, ...) fall back to the app shell.
		serveIndex(c)
	})
}

// WantsHTML reports whether a request comes from a browser that would rather
// see the dashboard than the JSON banner on "/".
func WantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}
