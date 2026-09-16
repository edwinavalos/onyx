package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeSource is a SecretSource with fixed values.
type fakeSource map[string]string

func (f fakeSource) Get(_ context.Context, key string) (string, error) {
	v, ok := f[key]
	if !ok {
		return "", ErrNoSecret
	}
	return v, nil
}

// upstream is a TLS server that reports the Authorization header it saw.
func upstream(t *testing.T) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Auth", r.Header.Get("Authorization"))
		w.Header().Set("X-Seen-Host", r.Host)
		_, _ = io.WriteString(w, "ok "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, pool
}

// forwardClient is an HTTP client that uses the forward proxy at addr and
// trusts the Onyx CA plus the test upstream's certificate.
func forwardClient(t *testing.T, proxyURL string, ca *CA, extra *x509.CertPool) *http.Client {
	t.Helper()
	pu, _ := url.Parse(proxyURL)
	pool := extra.Clone()
	pool.AppendCertsFromPEM(ca.PEM())
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
}

func TestCAPersistsAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.PEM()) != string(b.PEM()) {
		t.Fatal("second load produced a different CA")
	}
	leaf, err := a.Leaf("api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.Leaf.VerifyHostname("api.github.com"); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(a.PEM())
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "api.github.com"}); err != nil {
		t.Fatalf("leaf does not chain to the CA: %v", err)
	}
}

// A CONNECT to an intercepted host is terminated with a minted leaf, the
// guest's placeholder credential is stripped and the real one injected.
func TestForwardInterceptsAndInjects(t *testing.T) {
	up, upPool := upstream(t)
	ca, _ := LoadOrCreateCA(t.TempDir())
	var mu sync.Mutex
	var logged []string
	f := NewForward(ForwardConfig{
		CA:     ca,
		Source: fakeSource{"gh-token": "real-secret"},
		Routes: []Route{{Name: "gh-token", Key: "gh-token", Upstream: up.URL, Auth: "bearer"}},
		LogRequest: func(route string, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			logged = append(logged, route+" "+r.Method+" "+r.URL.Path)
		},
		UpstreamTLS: &tls.Config{RootCAs: upPool, MinVersion: tls.VersionTLS12},
	})
	ps := httptest.NewServer(f)
	defer ps.Close()

	c := forwardClient(t, ps.URL, ca, upPool)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, up.URL+"/user", nil)
	req.Header.Set("Authorization", "token placeholder")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok /user" {
		t.Fatalf("body %q", body)
	}
	if got := resp.Header.Get("X-Seen-Auth"); got != "Bearer real-secret" {
		t.Fatalf("upstream saw Authorization %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 || logged[0] != "gh-token GET /user" {
		t.Fatalf("log = %v", logged)
	}
}

// A CONNECT to any other host is a plain tunnel: the proxy neither
// terminates TLS nor touches headers.
func TestForwardTunnelsOtherHosts(t *testing.T) {
	up, upPool := upstream(t)
	ca, _ := LoadOrCreateCA(t.TempDir())
	f := NewForward(ForwardConfig{CA: ca, Source: fakeSource{}, Routes: []Route{
		{Name: "x", Key: "x", Upstream: "https://example.invalid", Auth: "bearer"},
	}})
	ps := httptest.NewServer(f)
	defer ps.Close()

	c := forwardClient(t, ps.URL, ca, upPool)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, up.URL+"/p", nil)
	req.Header.Set("Authorization", "token mine")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Seen-Auth"); got != "token mine" {
		t.Fatalf("tunnel altered Authorization: %q", got)
	}
}

// In restricted mode a host that is neither intercepted nor allowed is
// refused before any connection leaves the host.
func TestForwardRestrictedDeniesUnlisted(t *testing.T) {
	up, upPool := upstream(t)
	ca, _ := LoadOrCreateCA(t.TempDir())
	rules, err := ParseAllow([]string{"allowed.example"})
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []string
	f := NewForward(ForwardConfig{CA: ca, Source: fakeSource{}, Allow: rules, Restricted: true,
		LogEgress: func(verdict, target string) { verdicts = append(verdicts, verdict+" "+target) }})
	ps := httptest.NewServer(f)
	defer ps.Close()

	c := forwardClient(t, ps.URL, ca, upPool)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, up.URL+"/p", nil)
	resp, err := c.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("want a 403 from the proxy, got %v", err)
	}
	if len(verdicts) != 1 || !strings.HasPrefix(verdicts[0], "deny ") {
		t.Fatalf("verdicts = %v", verdicts)
	}
}

// Intercepted hosts are reachable in restricted mode without being on the
// allowlist: naming the upstream in a pack is the grant.
func TestForwardRestrictedAllowsIntercepted(t *testing.T) {
	up, upPool := upstream(t)
	ca, _ := LoadOrCreateCA(t.TempDir())
	f := NewForward(ForwardConfig{CA: ca, Source: fakeSource{"k": "v"}, Restricted: true,
		Routes:      []Route{{Name: "k", Key: "k", Upstream: up.URL, Auth: "header:X-Api-Key"}},
		UpstreamTLS: &tls.Config{RootCAs: upPool, MinVersion: tls.VersionTLS12}})
	ps := httptest.NewServer(f)
	defer ps.Close()

	c := forwardClient(t, ps.URL, ca, upPool)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, up.URL+"/p", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// The reverse proxy (loopback URL rewrite for git and ANTHROPIC_BASE_URL)
// shares the injection code.
func TestReverseInjects(t *testing.T) {
	up, upPool := upstream(t)
	r := NewReverse(Route{Name: "tok", Key: "tok", Upstream: up.URL, Auth: "basic:x-access-token"},
		fakeSource{"tok": "s3"}, nil, &tls.Config{RootCAs: upPool, MinVersion: tls.VersionTLS12})
	ps := httptest.NewServer(r)
	defer ps.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ps.URL+"/repo.git/info/refs", nil)
	req.Header.Set("Authorization", "Basic placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Seen-Auth"); got != "Basic eC1hY2Nlc3MtdG9rZW46czM=" {
		t.Fatalf("Authorization %q", got)
	}
	if got := resp.Header.Get("X-Seen-Host"); got != strings.TrimPrefix(up.URL, "https://") {
		t.Fatalf("Host %q", got)
	}
}

// A missing secret leaves the request unauthenticated with a diagnostic
// header rather than failing closed on the guest's placeholder.
func TestInjectMissingSecret(t *testing.T) {
	up, upPool := upstream(t)
	r := NewReverse(Route{Name: "tok", Key: "tok", Upstream: up.URL, Auth: "bearer"},
		fakeSource{}, nil, &tls.Config{RootCAs: upPool, MinVersion: tls.VersionTLS12})
	ps := httptest.NewServer(r)
	defer ps.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ps.URL+"/x", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Seen-Auth"); got != "" {
		t.Fatalf("Authorization %q, want none", got)
	}
}
