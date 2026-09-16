package proxy

import (
	"crypto/tls"
	"net/http"
	"net/http/httputil"
	"time"
)

// NewReverse is the loopback-URL proxy for one route: the guest talks plain
// HTTP to it and it forwards to the route's upstream with the credential
// attached. logf, when set, sees every request (method + path; no bodies,
// no headers). upstreamTLS is for tests; nil uses system roots.
func NewReverse(r Route, src SecretSource, logf func(*http.Request), upstreamTLS *tls.Config) http.Handler {
	upstream, err := r.upstreamURL()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "onyx proxy: "+err.Error(), http.StatusBadGateway)
		})
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Host = upstream.Host
			inject(r, src, pr.Out)
			if logf != nil {
				logf(pr.In)
			}
		},
		Transport:      upstreamTransport(upstreamTLS),
		ModifyResponse: stripChallenge,
	}
}

// stripChallenge keeps an upstream 401 from prompting git for a password
// the guest does not have; the failure stays explicit instead.
func stripChallenge(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Header.Del("WWW-Authenticate")
	}
	return nil
}

// upstreamTransport reaches the real upstream. It honours the host's own
// proxy environment (the user's corporate proxy), never the guest's.
func upstreamTransport(tlsConf *tls.Config) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       tlsConf,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}
