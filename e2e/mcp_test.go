//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPStdioSessions talks to the onyx mcp subprocess over its real stdio
// transport. The session command deliberately does not invoke an agent: this
// validates all agent layouts without requiring any provider credentials.
func TestMCPStdioSessions(t *testing.T) {
	h := need(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.repo+"/bin/onyx", "mcp") // #nosec G204 -- our signed test binary
	cmd.Env = append(os.Environ(), "ONYX_HOME="+h.dir, "ONYX_SOCKET="+h.socket)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "onyx-e2e", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect MCP subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	for _, tc := range []struct {
		agent string
		state string
	}{
		{agent: "claude", state: "claude-state"},
		{agent: "codex", state: "codex-state"},
		{agent: "pi", state: "pi-state"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			name := fmt.Sprintf("mcp-%s-%d", tc.agent, h.seq.Add(1))
			res := mcpCall(t, ctx, cs, "start_session", map[string]any{
				"name": name, "agent": tc.agent, "cmd": "sleep 30",
			})
			if got := mcpString(t, res, "name"); got != name {
				t.Fatalf("start_session name = %q, want %q", got, name)
			}
			t.Cleanup(func() { removeMCPVM(t, cs, name) })

			st, err := h.cl.GetVM(ctx, name)
			if err != nil {
				t.Fatalf("get VM after MCP start: %v", err)
			}
			if st.State != "running" {
				t.Fatalf("VM state = %q, want running", st.State)
			}
			want := store.VolumeMount{Volume: tc.state, Target: agentStateDir(tc.agent)}
			if !hasMount(st.Volumes, want) {
				t.Errorf("state mount missing from %#v; want %#v", st.Volumes, want)
			}

			if tc.agent == "claude" {
				execRes := mcpCall(t, ctx, cs, "exec", map[string]any{"name": name, "argv": []string{"printf", "mcp-ok"}})
				if got := mcpString(t, execRes, "output"); got != "mcp-ok" {
					t.Errorf("MCP exec output = %q, want mcp-ok", got)
				}
			}
		})
	}

	t.Run("explicit_empty_state_disables_mount", func(t *testing.T) {
		name := fmt.Sprintf("mcp-no-state-%d", h.seq.Add(1))
		mcpCall(t, ctx, cs, "start_session", map[string]any{
			"name": name, "agent": "codex", "cmd": "sleep 30", "state_volume": "",
		})
		t.Cleanup(func() { removeMCPVM(t, cs, name) })
		st, err := h.cl.GetVM(ctx, name)
		if err != nil {
			t.Fatalf("get VM after MCP start: %v", err)
		}
		if hasMount(st.Volumes, store.VolumeMount{Volume: "codex-state", Target: agentStateDir("codex")}) {
			t.Errorf("explicit empty state_volume still mounted codex state: %#v", st.Volumes)
		}
	})
}

func mcpCall(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("MCP %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("MCP %s returned an error: %v", name, res.Content)
	}
	return res
}

func mcpString(t *testing.T, res *mcp.CallToolResult, key string) string {
	t.Helper()
	value, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v, want object", res.StructuredContent)
	}
	got, ok := value[key].(string)
	if !ok {
		t.Fatalf("structured content %q = %#v, want string", key, value[key])
	}
	return got
}

func removeMCPVM(t *testing.T, cs *mcp.ClientSession, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	mcpCall(t, ctx, cs, "stop_vm", map[string]any{"name": name})
	mcpCall(t, ctx, cs, "remove_vm", map[string]any{"name": name})
}

func hasMount(mounts []store.VolumeMount, want store.VolumeMount) bool {
	for _, mount := range mounts {
		if mount == want {
			return true
		}
	}
	return false
}

func agentStateDir(name string) string {
	return map[string]string{
		"claude": "/home/dev/.claude",
		"codex":  "/home/dev/.codex",
		"pi":     "/home/dev/.pi",
	}[name]
}
