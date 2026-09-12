package core

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestParseEgressRules(t *testing.T) {
	rules, err := parseEgressRules([]string{"GitHub.com", " *.npmjs.org ", "proxy.example:8080", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("got %d rules", len(rules))
	}
	if rules[0] != (egressRule{host: "github.com"}) || rules[1] != (egressRule{host: "npmjs.org", suffix: true}) || rules[2] != (egressRule{host: "proxy.example", port: 8080}) {
		t.Fatalf("rules: %+v", rules)
	}
	for _, bad := range []string{"*", "a*b.com", "host:0", "host:abc", "http://x"} {
		if _, err := parseEgressRules([]string{bad}); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestEgressAllowed(t *testing.T) {
	rules, _ := parseEgressRules([]string{"github.com", "*.npmjs.org", "proxy.example:8080"})
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"github.com", 443, true},
		{"GITHUB.COM.", 80, true},
		{"github.com", 22, false},
		{"api.github.com", 443, false},
		{"registry.npmjs.org", 443, true},
		{"npmjs.org", 443, false}, // *.suffix means a strict subdomain
		{"evilnpmjs.org", 443, false},
		{"proxy.example", 8080, true},
		{"proxy.example", 443, false},
		{"example.com", 443, false},
	}
	for _, c := range cases {
		if got := egressAllowed(rules, c.host, c.port); got != c.want {
			t.Errorf("%s:%d = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}

// TestEgressProxy runs the proxy over TCP and drives it with http.Client
// the way curl/git/npm would: plain HTTP via absolute URI, HTTPS via CONNECT.
func TestEgressProxy(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "plain:"+r.URL.Path)
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls:"+r.URL.Path)
	}))
	defer secure.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("denied upstream was reached")
	}))
	defer denied.Close()

	// httptest binds 127.0.0.1:<random>, so rules carry explicit ports.
	rules, err := parseEgressRules([]string{"127.0.0.1:" + port(t, plain.URL), "127.0.0.1:" + port(t, secure.URL)})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var log []string
	p := newEgressProxy(rules, func(verdict, target string) {
		mu.Lock()
		defer mu.Unlock()
		log = append(log, verdict+" "+target)
	})
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.listener = l
	go func() { _ = p.server.Serve(l) }()
	defer p.close()

	proxyURL, _ := url.Parse("http://" + l.Addr().String())
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- httptest certificate
	}}

	do := func(u string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return client.Do(req)
	}
	get := func(u string) (int, string) {
		resp, err := do(u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if st, body := get(plain.URL + "/a"); st != 200 || body != "plain:/a" {
		t.Errorf("plain: %d %q", st, body)
	}
	if st, body := get(secure.URL + "/b"); st != 200 || body != "tls:/b" {
		t.Errorf("tls: %d %q", st, body)
	}
	if st, body := get(denied.URL + "/c"); st != http.StatusForbidden || !strings.Contains(body, "allowlist") {
		t.Errorf("denied plain: %d %q", st, body)
	}
	// A denied CONNECT surfaces as a transport error from the client.
	if resp, err := do("https://127.0.0.1:" + port(t, denied.URL) + "/d"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Errorf("denied CONNECT: err=%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"allow 127.0.0.1:" + port(t, plain.URL),
		"allow 127.0.0.1:" + port(t, secure.URL),
		"deny 127.0.0.1:" + port(t, denied.URL),
		"deny 127.0.0.1:" + port(t, denied.URL),
	}
	if strings.Join(log, "\n") != strings.Join(want, "\n") {
		t.Errorf("log:\n%s\nwant:\n%s", strings.Join(log, "\n"), strings.Join(want, "\n"))
	}
}

func port(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}
