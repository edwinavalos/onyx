package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/api"
	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/store"
)

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	tcp := fs.String("http", "", "also serve on this loopback TCP address (e.g. 127.0.0.1:0); a bearer token is generated and written to <state dir>/serve.json")
	withParent := fs.Bool("with-parent", false, "shut down (stopping all VMs) when the parent process exits")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lvl := slog.LevelInfo
	if os.Getenv("ONYX_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	if *withParent {
		// The GUI owns the VMs (decisions.md D13): if it dies for any reason,
		// including a SIGKILL that skips its own cleanup, follow it down.
		ctx = watchParent(ctx)
	}

	root, err := store.Default()
	if err != nil {
		return err
	}
	c, err := core.New(root)
	if err != nil {
		return err
	}
	srv := api.NewServer(c)
	if *tcp != "" {
		tok := make([]byte, 32)
		if _, err := rand.Read(tok); err != nil {
			return err
		}
		token := hex.EncodeToString(tok)
		addr, err := srv.ListenAndServeTCP(ctx, *tcp, token)
		if err != nil {
			return err
		}
		info, _ := json.Marshal(map[string]string{"addr": addr, "token": token, "socket": root.Socket()})
		infoPath := filepath.Join(root.Dir, "serve.json")
		if err := os.WriteFile(infoPath, info, 0o600); err != nil {
			return err
		}
		defer func() { _ = os.Remove(infoPath) }()
	}
	err = srv.ListenAndServe(ctx, root.Socket())

	slog.Info("serve: shutting down, stopping vms")
	c.StopAll(context.Background())
	return err
}

// watchParent returns a context cancelled once this process is reparented
// (its original parent exited).
func watchParent(ctx context.Context) context.Context {
	parent := os.Getppid()
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if os.Getppid() != parent {
					slog.Info("serve: parent exited, shutting down")
					return
				}
			}
		}
	}()
	return ctx
}
