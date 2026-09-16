package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

// The core configures a VM over the socket and then hands the proxy
// process raw connections; each is served by the handler it names.
func TestServerServesReverseOverSocket(t *testing.T) {
	up, upPool := upstream(t)
	dir := t.TempDir()
	ca, _ := LoadOrCreateCA(dir)
	srv := &Server{CA: ca, Source: fakeSource{"tok": "s3"}, LogDir: dir,
		UpstreamTLS: &tls.Config{RootCAs: upPool, MinVersion: tls.VersionTLS12}}
	sock := filepath.Join(dir, "p.sock")
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, l) }()

	cl := &Client{Socket: sock}
	if err := cl.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	caPEM, err := cl.Configure(ctx, "vm1", VMConfig{Routes: []Route{{Name: "tok", Key: "tok", Upstream: up.URL, Auth: "bearer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(caPEM) != string(ca.PEM()) {
		t.Fatal("configure did not return the CA")
	}

	conn, err := cl.Serve(ctx, "vm1", "reverse", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/x", nil)
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("X-Seen-Auth"); got != "Bearer s3" {
		t.Fatalf("Authorization %q", got)
	}

	// An unconfigured handler answers in-band rather than hanging.
	conn2, err := cl.Serve(ctx, "vm1", "reverse", 7)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if err := req.Write(conn2); err != nil {
		t.Fatal(err)
	}
	resp2, err := http.ReadResponse(bufio.NewReader(conn2), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp2.StatusCode)
	}
	if err := cl.Remove(ctx, "vm1"); err != nil {
		t.Fatal(err)
	}
}

// A route whose credential cannot be read fails configure, so the VM
// start reports the missing secret instead of serving 401s later.
func TestServerConfigureChecksSecrets(t *testing.T) {
	dir := t.TempDir()
	ca, _ := LoadOrCreateCA(dir)
	s := &Server{CA: ca, Source: fakeSource{}}
	err := s.configure("vm", &VMConfig{Routes: []Route{{Name: "gone", Key: "gone", Upstream: "https://x.example"}}})
	if err == nil || !errors.Is(err, ErrNoSecret) {
		t.Fatalf("err = %v, want ErrNoSecret", err)
	}
}
