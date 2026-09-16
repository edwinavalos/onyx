package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/edwinavalos/onyx/internal/proxy"
)

// runProxy is the credential proxy process (decisions.md D20). The core
// spawns it with its stdin held open; EOF on stdin means the core is gone
// and the proxy exits, taking the only in-memory copies of proxied
// credentials with it.
func runProxy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	root := fs.String("root", "", "Onyx state dir (CA, logs)")
	socket := fs.String("socket", "", "Unix socket to serve the core on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" || *socket == "" {
		return flag.ErrHelp
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ca, err := proxy.LoadOrCreateCA(*root)
	if err != nil {
		return err
	}
	_ = os.Remove(*socket)
	l, err := (&net.ListenConfig{}).Listen(ctx, "unix", *socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		slog.Info("proxy: core closed stdin, exiting")
		cancel()
	}()
	s := &proxy.Server{CA: ca, Source: &proxy.Cached{Source: proxy.Keychain{}}, LogDir: *root}
	return s.Serve(ctx, l)
}
