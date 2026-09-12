package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/mcpserver"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runMCP serves the Model Context Protocol on stdin/stdout so coding
// harnesses (Claude Code, Codex, Cursor, ...) can drive Onyx directly.
func runMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// stdout is the protocol channel; keep logs on stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	root, err := store.Default()
	if err != nil {
		return err
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	// No core running (no app, no `onyx serve`)? Start one that lives as
	// long as this MCP server does, i.e. as long as the harness session.
	if err := cl.Ping(ctx); err != nil {
		if err := spawnCore(ctx, root); err != nil {
			return err
		}
		for i := 0; i < 40; i++ {
			if cl.Ping(ctx) == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err := cl.Ping(ctx); err != nil {
			return fmt.Errorf("started a core but it did not come up: %w", err)
		}
	}
	mcpserver.Version = version
	return mcpserver.New(cl, root).Run(ctx, &mcp.StdioTransport{})
}

// spawnCore launches `onyx serve -with-parent` as a child; it shuts its VMs
// down when this process exits.
func spawnCore(ctx context.Context, root store.Root) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(root.Dir, "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- fixed path
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "serve", "-with-parent") // #nosec G204 -- our own binary
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start core: %w", err)
	}
	slog.Warn("onyx mcp: started a core; VMs will stop when this MCP server exits", "pid", cmd.Process.Pid)
	go func() { _ = cmd.Wait() }()
	return nil
}
