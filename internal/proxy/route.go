// Package proxy is the host-side credential proxy (decisions.md D6, D20):
// it injects credentials into requests a guest makes, so the guest only
// ever holds placeholders. It has no dependency on the core and runs in
// its own process (`onyx proxy`), which is the only process that reads
// proxied credentials.
package proxy

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/edwinavalos/onyx/internal/pack"
)

// Route is one proxy-mode pack secret: requests for Upstream get the
// credential named by Key attached as Auth says. It serves both as a
// reverse proxy on its own vsock port (loopback URL rewrite in the guest)
// and as an intercept rule in the forward proxy (TLS termination for the
// upstream's host).
type Route struct {
	Name     string // pack secret key, for logging
	Key      string // Keychain key
	Upstream string // origin, e.g. https://api.github.com
	Auth     string // pack auth spec: "bearer", "basic:<user>", "header:<Name>"
}

// upstreamURL parses the route's origin.
func (r Route) upstreamURL() (*url.URL, error) {
	u, err := url.Parse(r.Upstream)
	if err != nil {
		return nil, fmt.Errorf("route %s: %w", r.Name, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("route %s: upstream %q must be an origin", r.Name, r.Upstream)
	}
	return u, nil
}

// hostKey is how intercept lookups identify a route: lower-case host with
// the port made explicit.
func hostKey(host string, defPort int) string {
	h, p := splitTarget(strings.ToLower(host), defPort)
	return fmt.Sprintf("%s:%d", strings.TrimSuffix(h, "."), p)
}

func (r Route) hostKey() (string, error) {
	u, err := r.upstreamURL()
	if err != nil {
		return "", err
	}
	def := 443
	if u.Scheme == "http" {
		def = 80
	}
	return hostKey(u.Host, def), nil
}

// credentialErrorHeader tells the guest why a request went out
// unauthenticated.
const credentialErrorHeader = "X-Onyx-Credential-Error" // #nosec G101 -- a header name

// inject strips whatever credential the guest attached and sets the real
// one. It never fails the request: with the secret unavailable the request
// goes out unauthenticated and the upstream's 401 says so.
func inject(r Route, src SecretSource, out *http.Request) {
	auth, err := pack.ParseAuth(r.Auth)
	if err != nil {
		out.Header.Set(credentialErrorHeader, err.Error())
		return
	}
	// The guest's placeholder credentials must never reach upstream,
	// whichever header it put them in.
	out.Header.Del("Authorization")
	out.Header.Del("X-Api-Key")
	out.Header.Del("X-Forwarded-For")
	value, err := src.Get(out.Context(), r.Key)
	if err != nil {
		slog.Warn("proxy: credential unavailable", "secret", r.Key, "err", err)
		out.Header.Set(credentialErrorHeader, err.Error())
		return
	}
	switch auth.Kind {
	case "bearer":
		out.Header.Set("Authorization", "Bearer "+value)
	case "basic":
		out.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(auth.Arg+":"+value)))
	default:
		out.Header.Del(auth.Arg)
		out.Header.Set(auth.Arg, value)
	}
}
