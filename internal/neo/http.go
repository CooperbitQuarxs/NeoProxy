package neo

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
)

const SessionCookie = "neoproxy_session"

// HTTP exposes only cookie-authenticated dashboard operations. Operator authority
// is injected exclusively for bootstrap and the existing maintenance routes.
type HTTP struct{ Store *Store }

func NewHTTP(store *Store) *HTTP { return &HTTP{Store: store} }

func Error(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "neoproxy_internal", "internal error"
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, code, message = 401, "neoproxy_unauthenticated", "invalid credentials or expired session"
	case errors.Is(err, ErrSuspended):
		status, code, message = 403, "neoproxy_suspended", ErrSuspended.Error()
	case errors.Is(err, ErrForbidden):
		status, code, message = 403, "neoproxy_forbidden", ErrForbidden.Error()
	case errors.Is(err, ErrInvalid):
		status, code, message = 400, "neoproxy_invalid", err.Error()
	case errors.Is(err, ErrConflict):
		status, code, message = 409, "neoproxy_conflict", err.Error()
	case errors.Is(err, ErrNotFound):
		status, code, message = 404, "neoproxy_not_found", ErrNotFound.Error()
	}
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "code": code}})
}

// CheckMutationOrigin rejects cross-origin cookie mutations. Native clients that
// do not send Origin must deliberately opt in with X-Neoproxy-CSRF: 1. Browsers
// cannot forge this header across origins without sending a rejected Origin.
func CheckMutationOrigin(c *gin.Context) bool {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if site := c.GetHeader("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		Error(c, ErrForbidden)
		return false
	}
	origin := c.GetHeader("Origin")
	if origin == "" {
		if c.GetHeader("X-Neoproxy-CSRF") == "1" {
			return true
		}
		Error(c, ErrForbidden)
		return false
	}
	parsed, err := url.Parse(origin)
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if err != nil || parsed.Scheme != scheme || !strings.EqualFold(parsed.Host, c.Request.Host) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		Error(c, ErrForbidden)
		return false
	}
	return true
}
func (h *HTTP) Available(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if h == nil || h.Store == nil {
		c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"message": "identity database unavailable", "code": "neoproxy_unavailable"}})
		return false
	}
	return true
}
func (h *HTTP) SessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.authenticateSession(c) {
			return
		}
		c.Next()
	}
}
func (h *HTTP) authenticateSession(c *gin.Context) bool {
	if !h.Available(c) || !CheckMutationOrigin(c) {
		return false
	}
	token, err := c.Cookie(SessionCookie)
	if err != nil {
		Error(c, ErrUnauthenticated)
		return false
	}
	_, p, err := h.Store.Session(c.Request.Context(), token)
	if err != nil {
		Error(c, err)
		return false
	}
	c.Request = c.Request.WithContext(WithPrincipal(c.Request.Context(), p))
	return true
}
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := PrincipalFromContext(c.Request.Context())
		if !ok || !p.IsAdmin() {
			Error(c, ErrForbidden)
			return
		}
		c.Next()
	}
}

// ManagementMiddleware preserves explicit operator maintenance authentication.
// A session is never synthesized from a management key or inference key.
func (h *HTTP) ManagementMiddleware(operator gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), "nk_") || strings.HasPrefix(c.GetHeader("X-Management-Key"), "nk_") {
			Error(c, ErrForbidden)
			return
		}
		if _, err := c.Cookie(SessionCookie); err == nil {
			if !h.authenticateSession(c) {
				return
			}
			p, ok := PrincipalFromContext(c.Request.Context())
			if !ok || !p.IsAdmin() {
				Error(c, ErrForbidden)
				return
			}
			c.Next()
			return
		}
		operator(c)
	}
}
func cookie(c *gin.Context, token string, expires int64) {
	maxAge := int(time.Until(time.UnixMilli(expires)).Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", Expires: time.UnixMilli(expires), MaxAge: maxAge, HttpOnly: true, Secure: c.Request.TLS != nil, SameSite: http.SameSiteStrictMode})
}
func (h *HTTP) RegisterRoutes(engine *gin.Engine, operator gin.HandlerFunc) {
	engine.POST("/v8/session", h.login)
	engine.GET("/v8/session", h.restore)
	engine.DELETE("/v8/session", h.logout)
	engine.POST("/v8/session/bootstrap", func(c *gin.Context) {
		if !h.Available(c) || !CheckMutationOrigin(c) {
			return
		}
		operator(c)
	}, h.bootstrap)
	group := engine.Group("/v8/neo", h.SessionMiddleware())
	group.GET("/core", h.core)
	group.GET("/users", RequireAdmin(), h.users)
	group.POST("/users", RequireAdmin(), h.createUser)
	group.GET("/users/:id", h.user)
	group.PUT("/users/:id", RequireAdmin(), h.updateUser)
	group.DELETE("/users/:id", RequireAdmin(), h.deleteUser)
	group.POST("/users/:id/password", h.password)
	group.POST("/users/:id/keys", h.mintKey)
	group.DELETE("/users/:id/keys/:keyId", h.revokeKey)
}
func (h *HTTP) login(c *gin.Context) {
	if !h.Available(c) || !CheckMutationOrigin(c) {
		return
	}
	var in struct {
		Handle   string `json:"handle"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		Error(c, ErrInvalid)
		return
	}
	session, token, err := h.Store.Login(c.Request.Context(), in.Handle, in.Password)
	if err != nil {
		Error(c, err)
		return
	}
	cookie(c, token, session.ExpiresAt)
	c.JSON(200, session)
}
func (h *HTTP) bootstrap(c *gin.Context) {
	var in struct {
		Handle   string `json:"handle"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		Error(c, ErrInvalid)
		return
	}
	session, token, err := h.Store.Bootstrap(c.Request.Context(), in.Handle, in.Name, in.Password)
	if err != nil {
		Error(c, err)
		return
	}
	cookie(c, token, session.ExpiresAt)
	c.JSON(201, session)
}
func (h *HTTP) restore(c *gin.Context) {
	if !h.Available(c) {
		return
	}
	token, err := c.Cookie(SessionCookie)
	if err != nil {
		Error(c, ErrUnauthenticated)
		return
	}
	session, _, err := h.Store.Session(c.Request.Context(), token)
	if err != nil {
		Error(c, err)
		return
	}
	c.JSON(200, session)
}
func (h *HTTP) logout(c *gin.Context) {
	if !h.Available(c) || !CheckMutationOrigin(c) {
		return
	}
	token, _ := c.Cookie(SessionCookie)
	if err := h.Store.Logout(c.Request.Context(), token); err != nil {
		Error(c, err)
		return
	}
	cookie(c, "", 0)
	c.Status(204)
}
func (h *HTTP) users(c *gin.Context) {
	users, err := h.Store.Users(c.Request.Context())
	if err != nil {
		Error(c, err)
		return
	}
	c.JSON(200, users)
}
func userAuthority(c *gin.Context) (Principal, bool) {
	p, ok := PrincipalFromContext(c.Request.Context())
	if !ok || !p.CanAccessUser(c.Param("id")) {
		Error(c, ErrForbidden)
		return p, false
	}
	return p, true
}
func (h *HTTP) user(c *gin.Context) {
	if _, ok := userAuthority(c); !ok {
		return
	}
	u, err := h.Store.User(c.Request.Context(), c.Param("id"))
	if err != nil {
		Error(c, err)
		return
	}
	c.JSON(200, u)
}
func (h *HTTP) createUser(c *gin.Context) {
	var in UserInput
	if c.ShouldBindJSON(&in) != nil {
		Error(c, ErrInvalid)
		return
	}
	u, err := h.Store.CreateUser(c.Request.Context(), in)
	if err != nil {
		Error(c, err)
		return
	}
	c.JSON(201, u)
}
func (h *HTTP) updateUser(c *gin.Context) {
	var in UserInput
	if c.ShouldBindJSON(&in) != nil {
		Error(c, ErrInvalid)
		return
	}
	u, err := h.Store.UpdateUser(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		Error(c, err)
		return
	}
	c.JSON(200, u)
}
func (h *HTTP) deleteUser(c *gin.Context) {
	if err := h.Store.DeleteUser(c.Request.Context(), c.Param("id")); err != nil {
		Error(c, err)
		return
	}
	c.Status(204)
}
func (h *HTTP) password(c *gin.Context) {
	p, ok := userAuthority(c)
	if !ok {
		return
	}
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		Error(c, ErrInvalid)
		return
	}
	if err := h.Store.ChangePassword(c.Request.Context(), c.Param("id"), in.CurrentPassword, in.Password, p.IsAdmin()); err != nil {
		Error(c, err)
		return
	}
	c.Status(204)
}
func (h *HTTP) mintKey(c *gin.Context) {
	p, ok := userAuthority(c)
	if !ok {
		return
	}
	var in struct {
		Label      string   `json:"label"`
		AccountIDs []string `json:"accountIds"`
		ExpiresAt  *int64   `json:"expiresAt"`
	}
	if c.ShouldBindJSON(&in) != nil {
		Error(c, ErrInvalid)
		return
	}
	k, secret, err := h.Store.MintKey(c.Request.Context(), p, c.Param("id"), in.Label, in.AccountIDs, in.ExpiresAt)
	if err != nil {
		Error(c, err)
		return
	}
	c.JSON(201, gin.H{"key": k, "secret": secret})
}
func (h *HTTP) revokeKey(c *gin.Context) {
	p, ok := userAuthority(c)
	if !ok {
		return
	}
	if err := h.Store.RevokeKey(c.Request.Context(), p, c.Param("id"), c.Param("keyId")); err != nil {
		Error(c, err)
		return
	}
	c.Status(204)
}
func (h *HTTP) core(c *gin.Context) {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	host = strings.SplitN(host, ".", 2)[0]
	p, _ := PrincipalFromContext(c.Request.Context())
	scopes := []string{"models:read", "usage:read"}
	if p.IsAdmin() {
		scopes = append(scopes, "organization:admin")
	} else if p.Role == RoleOwner {
		scopes = append(scopes, "accounts:owned")
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	c.JSON(200, gin.H{"linked": true, "endpoint": scheme + "://" + c.Request.Host, "identity": "neoproxy@" + host, "version": buildinfo.Version, "host": host, "heartbeatAt": h.Store.now().UnixMilli(), "scopes": scopes})
}
