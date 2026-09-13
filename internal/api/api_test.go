package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/store"
)

// newTestServer runs the API over a Unix socket in a temp dir with a fake
// "base" image (empty files are fine: nothing is booted).
func newTestServer(t *testing.T) (*http.Client, store.Root) {
	t.Helper()
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
	sock := filepath.Join(os.TempDir(), "onyx-api-test-"+filepath.Base(root.Dir)+".sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = NewServer(c).ListenAndServe(ctx, sock) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	// Wait for the socket.
	for i := 0; i < 100; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://onyx/v1/ping", nil)
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			return client, root
		}
	}
	t.Fatal("api did not come up")
	return nil, root
}

func call(t *testing.T, c *http.Client, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, "http://onyx"+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	m, _ := out.(map[string]any)
	return resp.StatusCode, m
}

func TestVolumeAndVMDefinitions(t *testing.T) {
	c, root := newTestServer(t)

	if code, _ := call(t, c, "POST", "/v1/volumes", CreateVolumeReq{Name: "work", SizeMB: 1}); code != 200 {
		t.Fatalf("create volume: %d", code)
	}
	if code, m := call(t, c, "POST", "/v1/volumes", CreateVolumeReq{Name: "work", SizeMB: 1}); code != 400 || m["error"] == nil {
		t.Fatalf("duplicate volume: %d %v", code, m)
	}

	cfg := store.VMConfig{Name: "dev", Volumes: []store.VolumeMount{{Volume: "work", Target: "/home/dev/work"}}}
	code, m := call(t, c, "POST", "/v1/vms", cfg)
	if code != 200 || m["state"] != "stopped" || m["image"] != "base" {
		t.Fatalf("create vm: %d %v", code, m)
	}
	if _, err := os.Stat(filepath.Join(root.VMDir("dev"), "root.img")); err != nil {
		t.Fatalf("root disk not cloned: %v", err)
	}

	if code, _ := call(t, c, "POST", "/v1/vms", store.VMConfig{Name: "bad", Volumes: []store.VolumeMount{{Volume: "missing", Target: "/x"}}}); code != 404 {
		t.Fatalf("vm with missing volume: %d", code)
	}
	if code, _ := call(t, c, "POST", "/v1/vms", store.VMConfig{Name: "bad", Image: "nope"}); code != 404 {
		t.Fatalf("vm with missing image: %d", code)
	}
	if code, _ := call(t, c, "POST", "/v1/vms", store.VMConfig{Name: "bad", Packs: []string{"nope"}}); code != 404 {
		t.Fatalf("vm with missing pack: %d", code)
	}
	if code, _ := call(t, c, "POST", "/v1/vms", store.VMConfig{Name: "bad", Volumes: []store.VolumeMount{{Volume: "work", Target: "relative"}}}); code != 400 {
		t.Fatalf("vm with relative target: %d", code)
	}

	if code, _ := call(t, c, "POST", "/v1/vms/dev/stop", nil); code != 400 {
		t.Fatalf("stop stopped vm: %d", code)
	}
	if code, _ := call(t, c, "GET", "/v1/vms/ghost", nil); code != 404 {
		t.Fatalf("get missing vm: %d", code)
	}
	if code, _ := call(t, c, "DELETE", "/v1/vms/dev", nil); code != 200 {
		t.Fatalf("delete vm: %d", code)
	}
	if code, _ := call(t, c, "DELETE", "/v1/volumes/work", nil); code != 200 {
		t.Fatalf("delete volume: %d", code)
	}
	if code, _ := call(t, c, "DELETE", "/v1/volumes/work", nil); code != 404 {
		t.Fatalf("delete missing volume: %d", code)
	}
}

func TestPacksAPI(t *testing.T) {
	c, _ := newTestServer(t)
	p := map[string]any{"name": "gh", "secrets": []map[string]any{{"key": "gh-token", "mode": "env", "name": "GH_TOKEN"}}}
	if code, _ := call(t, c, "PUT", "/v1/packs", p); code != 200 {
		t.Fatalf("save pack: %d", code)
	}
	if code, m := call(t, c, "GET", "/v1/packs/gh", nil); code != 200 || m["name"] != "gh" {
		t.Fatalf("get pack: %d %v", code, m)
	}
	bad := map[string]any{"name": "bad", "secrets": []map[string]any{{"key": "k", "mode": "proxy"}}}
	if code, _ := call(t, c, "PUT", "/v1/packs", bad); code != 400 {
		t.Fatalf("proxy pack without upstream accepted: %d", code)
	}
	if code, _ := call(t, c, "DELETE", "/v1/packs/gh", nil); code != 200 {
		t.Fatalf("delete pack: %d", code)
	}
	if code, _ := call(t, c, "GET", "/v1/packs/gh", nil); code != 404 {
		t.Fatalf("get deleted pack: %d", code)
	}
}

// GET /v1/volumes carries sizes next to the names, and a stopped VM's
// console log is readable over the API (the app shows it in place of the
// terminal).
func TestVolumeSizesAndConsoleLog(t *testing.T) {
	c, root := newTestServer(t)

	if code, _ := call(t, c, "POST", "/v1/volumes", CreateVolumeReq{Name: "work", SizeMB: 3}); code != 200 {
		t.Fatalf("create volume: %d", code)
	}
	code, m := call(t, c, "GET", "/v1/volumes", nil)
	if code != 200 {
		t.Fatalf("list volumes: %d", code)
	}
	vols, _ := m["volumes"].([]any)
	if len(vols) != 1 {
		t.Fatalf("volumes = %v", m["volumes"])
	}
	v, _ := vols[0].(map[string]any)
	if v["name"] != "work" || v["size_mb"] != float64(3) {
		t.Fatalf("volume info = %v", v)
	}
	if _, ok := v["used_mb"]; !ok {
		t.Fatalf("volume info lacks used_mb: %v", v)
	}
	if names, _ := m["names"].([]any); len(names) != 1 || names[0] != "work" {
		t.Fatalf("names = %v", m["names"])
	}

	if code, _ := call(t, c, "POST", "/v1/vms", store.VMConfig{Name: "dev"}); code != 200 {
		t.Fatalf("create vm: %d", code)
	}
	if code, _ := call(t, c, "GET", "/v1/vms/dev/console_log", nil); code != 404 {
		t.Fatalf("console log before any boot: %d, want 404", code)
	}
	if err := os.WriteFile(filepath.Join(root.VMDir("dev"), "console.log"), []byte("login: ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, m := call(t, c, "GET", "/v1/vms/dev/console_log?bytes=4", nil); code != 200 || m["output"] != " ok\n" {
		t.Fatalf("console log tail: %d %v", code, m)
	}
	if code, m := call(t, c, "GET", "/v1/vms/dev/console_log", nil); code != 200 || m["output"] != "login: ok\n" {
		t.Fatalf("console log: %d %v", code, m)
	}
	if code, _ := call(t, c, "GET", "/v1/vms/ghost/console_log", nil); code != 404 {
		t.Fatalf("console log of missing vm: %d", code)
	}
}
