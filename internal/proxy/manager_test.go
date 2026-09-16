package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"testing"
)

// In-process mode is the same socket protocol with the server in the
// core's process; guest bytes arrive through Splice.
func TestManagerInProcessSplice(t *testing.T) {
	up, _ := upstream(t)
	dir := t.TempDir()
	m := &Manager{Root: dir, InProcess: true, Source: fakeSource{"k": "v"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Configure(ctx, "vm", VMConfig{Routes: []Route{{Name: "k", Key: "k", Upstream: up.URL, Auth: "header:X-Key"}}}); err != nil {
		t.Fatal(err)
	}
	guest, host := net.Pipe()
	go m.Splice(ctx, host, "vm", "forward", 0)
	req, _ := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+up.Listener.Addr().String(), nil)
	req.Host = up.Listener.Addr().String()
	// CONNECT reaches the forward proxy through the splice; injection
	// itself is covered by the handler tests.
	if err := req.Write(guest); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(guest), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT status %d", resp.StatusCode)
	}
	_ = guest.Close()
	if err := m.Remove(ctx, "vm"); err != nil {
		t.Fatal(err)
	}
}
