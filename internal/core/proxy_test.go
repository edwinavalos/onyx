package core

import (
	"context"
	"testing"

	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/store"
)

// Proxy-mode secrets become routes in pack order — the order fixes each
// one's vsock port — and env/file secrets are not the proxy's business.
func TestProxyRoutesFollowPackOrder(t *testing.T) {
	c, _ := newTestCore(t)
	for _, p := range []pack.Pack{
		{Name: "a", Secrets: []pack.Secret{
			{Key: "tok", Mode: pack.ModeProxy, Upstream: "https://api.example.com", Auth: "bearer"},
			{Key: "tok", Mode: pack.ModeEnv, Name: "TOK"},
		}},
		{Name: "b", Secrets: []pack.Secret{{Key: "gh", Mode: pack.ModeProxy, Upstream: "https://github.com"}}},
	} {
		if err := c.Packs().Save(p); err != nil {
			t.Fatal(err)
		}
	}
	routes, owners, err := c.proxyRoutes([]string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].Name != "gh" || routes[1].Name != "tok" || routes[1].Auth != "bearer" {
		t.Fatalf("routes = %+v", routes)
	}
	if owners[0] != "b" || owners[1] != "a" {
		t.Fatalf("owners = %v", owners)
	}
}

// A NAT VM with no proxy-mode secrets gets no proxy at all: nothing to
// bridge, no HTTPS_PROXY in the guest.
func TestSetupProxiesNothingToDo(t *testing.T) {
	c, _ := newTestCore(t)
	if err := c.Packs().Save(pack.Pack{Name: "env-only", Secrets: []pack.Secret{{Key: "k", Mode: pack.ModeEnv}}}); err != nil {
		t.Fatal(err)
	}
	inst := &instance{cfg: store.VMConfig{Name: "vm1", Network: store.NetworkNAT}}
	items, egress, err := c.setupProxies(context.Background(), inst, []string{"env-only"})
	if err != nil {
		t.Fatal(err)
	}
	if items != nil || egress != nil {
		t.Fatalf("items=%v egress=%v, want none", items, egress)
	}
}
