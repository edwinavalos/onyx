package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/edwinavalos/onyx/internal/api"
	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/store"
)

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	root, err := store.Default()
	if err != nil {
		return err
	}
	c, err := core.New(root)
	if err != nil {
		return err
	}
	srv := api.NewServer(c)
	err = srv.ListenAndServe(ctx, root.Socket())

	slog.Info("serve: shutting down, stopping vms")
	c.StopAll(context.Background())
	return err
}
