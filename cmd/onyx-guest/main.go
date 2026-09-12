// onyx-guest is the agent that runs inside every Onyx VM. It listens on
// vsock for the host daemon and performs privileged setup on its behalf.
package main

import (
	"log/slog"
	"os"

	"github.com/edwinavalos/onyx/internal/guest"
	"github.com/edwinavalos/onyx/internal/vsockproto"
	"github.com/mdlayher/vsock"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	l, err := vsock.Listen(vsockproto.Port, nil)
	if err != nil {
		slog.Error("guest: listen vsock", "port", vsockproto.Port, "err", err)
		os.Exit(1)
	}
	fl, err := vsock.Listen(vsockproto.FilePort, nil)
	if err != nil {
		slog.Error("guest: listen vsock", "port", vsockproto.FilePort, "err", err)
		os.Exit(1)
	}
	go func() {
		if err := guest.ServeFiles(fl); err != nil {
			slog.Error("guest: serve files", "err", err)
		}
	}()
	slog.Info("guest: listening", "port", vsockproto.Port, "files", vsockproto.FilePort)
	if err := guest.Serve(l); err != nil {
		slog.Error("guest: serve", "err", err)
		os.Exit(1)
	}
}
