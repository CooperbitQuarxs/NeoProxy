package helps

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// GuardCredentialRedirects returns a copy of client that removes the named
// credential headers from every redirect hop whose origin (scheme, host and
// port) differs from the initial request's. net/http copies the initial
// request's headers onto each hop and strips only its own sensitive list
// (Authorization, Cookie, WWW-Authenticate), so a gateway's own credential
// header would otherwise follow a redirect to another origin, an https to
// http downgrade included. Each hop is compared with the initial origin, as
// the headers are recopied from the initial request on every hop. The
// client's own redirect policy still runs afterwards; without one the
// default ten-hop limit applies.
func GuardCredentialRedirects(client *http.Client, headers ...string) *http.Client {
	if client == nil || len(headers) == 0 {
		return client
	}
	guarded := *client
	next := client.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
			for _, name := range headers {
				req.Header.Del(name)
			}
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &guarded
}

// sameOrigin reports whether two URLs share scheme, host and effective port.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}
