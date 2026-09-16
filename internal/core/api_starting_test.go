package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/api"
	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/store"
)

// Regression for the wedged core of 2026-09-12: with one VM mid-start the
// app's list poll panicked inside the core with its mutex held, after which
// delete did nothing and a newly created VM never appeared. Drive the real
// HTTP server so the whole path is covered.
func TestAPIWhileVMStarting(t *testing.T) {
	t.Setenv("ONYX_PROXY_INPROC", "1") // no child process under go test
	root := store.Root{Dir: t.TempDir()}
	c, err := core.New(root)
	if err != nil {
		t.Fatal(err)
	}
	img := root.ImageDir("base")
	if err := os.MkdirAll(img, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"vmlinux", "initramfs", "rootfs.img"} {
		if err := os.WriteFile(filepath.Join(img, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(os.TempDir(), "onyx-core-test-"+filepath.Base(root.Dir)+".sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = api.NewServer(c).ListenAndServe(ctx, sock) }()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	do := func(method, path string, body any) (int, string, error) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequestWithContext(ctx, method, "http://onyx"+path, &buf)
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), nil
	}
	call := func(method, path string, body any) (int, string) {
		t.Helper()
		code, b, err := do(method, path, body)
		if err != nil {
			t.Fatalf("%s %s: %v (a timeout here means the core is wedged)", method, path, err)
		}
		return code, b
	}
	for i := 0; ; i++ {
		if code, _, err := do("GET", "/v1/ping", nil); err == nil && code == 200 {
			break
		}
		if i > 100 {
			t.Fatal("api did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if code, _ := call("POST", "/v1/vms", store.VMConfig{Name: "stuck"}); code != 200 {
		t.Fatalf("create stuck: %d", code)
	}
	if code, _ := call("POST", "/v1/vms", store.VMConfig{Name: "old"}); code != 200 {
		t.Fatalf("create old: %d", code)
	}
	c.ReserveStarting("stuck")

	// The list poll the app runs every few seconds.
	code, body := call("GET", "/v1/vms", nil)
	if code != 200 || !strings.Contains(body, `"state":"starting"`) {
		t.Fatalf("list with starting vm: %d %s", code, body)
	}
	// Delete of the stuck VM is refused with a reason, not ignored.
	if code, body := call("DELETE", "/v1/vms/stuck", nil); code != 400 || !strings.Contains(body, "starting") {
		t.Fatalf("delete starting vm: %d %s", code, body)
	}
	// Delete of another VM still works, and a new VM still shows up.
	if code, _ := call("DELETE", "/v1/vms/old", nil); code != 200 {
		t.Fatalf("delete old: %d", code)
	}
	if code, _ := call("POST", "/v1/vms", store.VMConfig{Name: "fresh"}); code != 200 {
		t.Fatalf("create fresh: %d", code)
	}
	code, body = call("GET", "/v1/vms", nil)
	if code != 200 || !strings.Contains(body, `"name":"fresh"`) || strings.Contains(body, `"name":"old"`) {
		t.Fatalf("list after changes: %d %s", code, body)
	}
	// Actions on the starting VM answer promptly.
	for _, a := range []string{"pause", "suspend"} {
		if code, body := call("POST", "/v1/vms/stuck/"+a, nil); code != 400 || !strings.Contains(body, "starting") {
			t.Fatalf("%s starting vm: %d %s", a, code, body)
		}
	}
	// Stop cancels the start (the app's Cancel button); the VM is then
	// stopped and can be deleted.
	if code, body := call("POST", "/v1/vms/stuck/stop", nil); code != 200 || !strings.Contains(body, `"state":"stopped"`) {
		t.Fatalf("cancel start: %d %s", code, body)
	}
	if code, _ := call("DELETE", "/v1/vms/stuck", nil); code != 200 {
		t.Fatalf("delete after cancel: %d", code)
	}
}
