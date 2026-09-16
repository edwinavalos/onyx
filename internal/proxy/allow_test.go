package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseAllow(t *testing.T) {
	rules, err := ParseAllow([]string{"GitHub.com", " *.npmjs.org ", "proxy.example:8080", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("got %d rules", len(rules))
	}
	if rules[0] != (Rule{host: "github.com"}) || rules[1] != (Rule{host: "npmjs.org", suffix: true}) || rules[2] != (Rule{host: "proxy.example", port: 8080}) {
		t.Fatalf("rules: %+v", rules)
	}
	for _, bad := range []string{"*", "a*b.com", "host:0", "host:abc", "http://x"} {
		if _, err := ParseAllow([]string{bad}); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestAllowed(t *testing.T) {
	rules, _ := ParseAllow([]string{"github.com", "*.npmjs.org", "proxy.example:8080"})
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
		if got := Allowed(rules, c.host, c.port); got != c.want {
			t.Errorf("%s:%d = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}

// Plain HTTP through the proxy (absolute-URI requests, as curl/npm send
// them) is admitted by the allowlist and forwarded untouched.
func TestForwardPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "plain:"+r.URL.Path+":"+r.Header.Get("Authorization"))
	}))
	defer plain.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("denied upstream was reached")
	}))
	defer denied.Close()
	ca, _ := LoadOrCreateCA(t.TempDir())
	rules, _ := ParseAllow([]string{strings.TrimPrefix(plain.URL, "http://")})
	ps := httptest.NewServer(NewForward(ForwardConfig{CA: ca, Source: fakeSource{}, Restricted: true, Allow: rules}))
	defer ps.Close()
	pu, _ := url.Parse(ps.URL)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, plain.URL+"/a", nil)
	req.Header.Set("Authorization", "mine")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "plain:/a:mine" {
		t.Fatalf("plain: %d %q", resp.StatusCode, b)
	}
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, denied.URL+"/c", nil)
	resp, err = c.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "allowlist") {
		t.Fatalf("denied: %d %q", resp.StatusCode, b)
	}
}
