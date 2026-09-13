package core

import (
	"context"
	"net/url"
	"testing"

	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/store"
)

// Re-delivering packs to a running VM (onyx pack deliver, the MCP
// deliver_packs tool, a guest agent restart) must reuse the credential
// proxies that are already serving instead of listening on their vsock
// ports a second time. The instance here has no machine, so any attempt
// to start a new proxy would panic.
func TestStartProxiesReusesRunningProxy(t *testing.T) {
	c, _ := newTestCore(t)
	if err := c.Packs().Save(pack.Pack{Name: "p", Secrets: []pack.Secret{
		{Key: "tok", Mode: pack.ModeProxy, Upstream: "https://api.example.com", Auth: "bearer"},
	}}); err != nil {
		t.Fatal(err)
	}
	up, _ := url.Parse("https://api.example.com")
	inst := &instance{cfg: store.VMConfig{Name: "vm1"}}
	inst.proxies = []*credProxy{{name: "tok", upstream: up, port: proxyPortBase}}

	items, err := c.startProxies(context.Background(), inst, []string{"p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "tok" || items[0].HostPort != proxyPortBase || items[0].Upstream != "https://api.example.com" || items[0].Auth != "bearer" {
		t.Fatalf("items = %+v", items)
	}
	if len(inst.proxies) != 1 {
		t.Fatalf("proxies = %d, want the existing one only", len(inst.proxies))
	}
}
