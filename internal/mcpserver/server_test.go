package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP clients (Claude Code among them) require structuredContent to be a
// JSON object, so every tool's output schema must have an object root:
// a bare array is rejected client-side and the tool becomes unusable.
func TestToolOutputSchemasAreObjects(t *testing.T) {
	ctx := context.Background()
	s := New(client.New("/nonexistent.sock"), store.Root{Dir: t.TempDir()})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

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
