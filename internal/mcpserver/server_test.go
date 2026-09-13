package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect serves the tools over an in-memory transport with no core behind
// them; only tools that work off the state root can be called.
func connect(t *testing.T) (*mcp.ClientSession, store.Root) {
	t.Helper()
	ctx := context.Background()
	root := store.Root{Dir: t.TempDir()}
	s := New(client.New("/nonexistent.sock"), root)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, root
}

// MCP clients (Claude Code among them) require structuredContent to be a
// JSON object, so every tool's output schema must have an object root:
// a bare array is rejected client-side and the tool becomes unusable.
func TestToolOutputSchemasAreObjects(t *testing.T) {
	ctx := context.Background()
	cs, _ := connect(t)

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range res.Tools {
		if tool.OutputSchema == nil {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		// "type" may be a string or a list such as ["array","null"].
		var schema struct {
			Type any `json:"type"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" {
			t.Errorf("%s: output schema root is %v, want object (%s)", tool.Name, schema.Type, raw)
		}
	}
}

// Harnesses render structuredContent when a tool declares an output
// schema, so text a tool produces must be in there too, not only in the
// unstructured content, or the caller sees "{}".
func TestTextToolsReturnStructuredOutput(t *testing.T) {
	ctx := context.Background()
	cs, root := connect(t)

	dir := root.VMDir("v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "console.log"), []byte("hello from the console\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "console_log", Arguments: map[string]any{"name": "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("console_log failed: %v", res.Content)
	}
	got, ok := res.StructuredContent.(map[string]any)
	if !ok || got["output"] != "hello from the console\n" {
		t.Errorf("structuredContent = %v, want {output: ...}", res.StructuredContent)
	}

	// exec needs a core to call, but its declared output must carry the
	// same field.
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "exec" {
			continue
		}
		raw, _ := json.Marshal(tool.OutputSchema)
		if !strings.Contains(string(raw), `"output"`) {
			t.Errorf("exec output schema lacks an output field: %s", raw)
		}
	}
}

// exec with a user runs the command in that user's login shell, so the
// delivered secrets, proxies and PATH are in place, like ossh does. The
// argv must survive the two shell layers (su -c, bash -lc) intact.
func TestLoginArgv(t *testing.T) {
	ctx := context.Background()
	argv := []string{"printf", "%s\n", "a b", "it's", "$HOME", "`x`"}
	got := loginArgv("dev", argv)
	if got[0] != "su" || got[1] != "dev" || got[2] != "-c" || len(got) != 4 {
		t.Fatalf("loginArgv = %q", got)
	}
	// HOME is empty so no profile of the host user runs.
	cmd := exec.CommandContext(ctx, "bash", "-c", got[3]) // #nosec G204 -- the quoting under test
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if want := "a b\nit's\n$HOME\n`x`\n"; string(out) != want {
		t.Errorf("output %q, want %q", out, want)
	}
}

// The size defaults an agent reads in the tool schema must be the ones the
// core actually applies; the numbers live in the struct tags, so pin them.
func TestSizeDefaultsInSchemasMatchStore(t *testing.T) {
	ctx := context.Background()
	cs, _ := connect(t)
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "create_vm" && tool.Name != "start_session" {
			continue
		}
		b, _ := json.Marshal(tool.InputSchema)
		s := string(b)
		for _, want := range []string{
			fmt.Sprintf("virtual CPUs; default %d", store.DefaultCPUs),
			fmt.Sprintf("memory in MB; default %d", store.DefaultMemoryMB),
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s schema lacks %q", tool.Name, want)
			}
		}
	}
}

// start_session mounts the work volume at /home/dev/work/<volume> and runs
// the command there; an explicit dir still wins (issue #2).
func TestStartSessionPlanUsesPerVolumeWorkDir(t *testing.T) {
	name, mounts, sess, err := planSession(sessionIn{Name: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "s1" {
		t.Fatalf("name = %q", name)
	}
	if len(mounts) != 2 || mounts[0] != (store.VolumeMount{Volume: "s1-work", Target: "/home/dev/work/s1-work"}) ||
		mounts[1] != (store.VolumeMount{Volume: "claude-state", Target: "/home/dev/.claude"}) {
		t.Errorf("mounts = %+v", mounts)
	}
	if sess.Dir != "/home/dev/work/s1-work" || sess.Cmd != "claude" || sess.OnExit != "poweroff" {
		t.Errorf("session = %+v", sess)
	}
	_, mounts, sess, err = planSession(sessionIn{Name: "s1", Work: "proj", NoState: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != "/home/dev/work/proj" || sess.Dir != "/home/dev/work/proj" {
		t.Errorf("named work volume: mounts=%+v dir=%q", mounts, sess.Dir)
	}
	if _, _, sess, err := planSession(sessionIn{Name: "s1", Dir: "/tmp/x"}); err != nil || sess.Dir != "/tmp/x" {
		t.Errorf("explicit dir: %q", sess.Dir)
	}
	if name, _, _, err := planSession(sessionIn{}); err != nil || !strings.HasPrefix(name, "session-") {
		t.Errorf("default name: %q", name)
	}
}

func TestPlanSessionUsesCodexAdapter(t *testing.T) {
	_, mounts, sess, err := planSession(sessionIn{Name: "s1", Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Cmd != "codex" {
		t.Errorf("command = %q, want codex", sess.Cmd)
	}
	if len(mounts) != 2 || mounts[1] != (store.VolumeMount{Volume: "codex-state", Target: "/home/dev/.codex"}) {
		t.Errorf("mounts = %+v", mounts)
	}
}

// The tool description and the dir default must not promise the old
// shared path.
func TestStartSessionDescribesPerVolumeWorkDir(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "start_session" {
			continue
		}
		dir := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["dir"].(map[string]any)["description"].(string)
		if !strings.Contains(tool.Description, "/home/dev/work/<volume>") || !strings.Contains(dir, "/home/dev/work/<work_volume>") {
			t.Errorf("start_session still documents the shared path:\n%s\n%s", tool.Description, dir)
		}
		return
	}
	t.Fatal("start_session not listed")
}
