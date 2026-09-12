// onyx manages local VMs that host coding agents.
//
//	onyx serve                       run the core (holds the VMs)
//	onyx image  import|ls
//	onyx volume create|ls|rm
//	onyx vm     create|ls|start|stop|rm|status|exec
//	onyx secret set|ls|rm
//	onyx pack   create|ls|show|rm|deliver
//	onyx spike                       self-contained end-to-end check
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/store"
)

// Set via -ldflags at build time; see Makefile.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Printf("onyx %s (%s)\n", version, commit)
	case "serve":
		err = runServe(ctx, os.Args[2:])
	case "image":
		err = runImage(ctx, os.Args[2:])
	case "volume":
		err = runVolume(ctx, os.Args[2:])
	case "vm":
		err = runVM(ctx, os.Args[2:])
	case "secret":
		err = runSecret(ctx, os.Args[2:])
	case "pack":
		err = runPack(ctx, os.Args[2:])
	case "spike":
		err = runSpike(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "onyx:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: onyx <command> [args]

  serve                          run the Onyx core (VMs live as long as this process)
  image import <name> <dir>      install an image from a directory with vmlinux, initramfs, rootfs.img
  image ls
  volume create <name> [-size MB]
  volume ls
  volume rm <name>
  vm create <name> [-image base] [-cpus N] [-mem MB] [-volume name:/guest/path ...] [-pack name ...]
  vm ls
  vm start <name>
  vm stop <name>
  vm rm <name>
  vm status <name>
  vm exec <name> -- <cmd...>
  secret set <key> [-stdin]      store a secret in the macOS Keychain (prompts; never on argv)
  secret ls
  secret rm <key>
  pack create <name> -secret key | key=ENV | key@/guest/path[:perm] ...
  pack ls | show <name> | rm <name>
  pack deliver <vm> <pack...>    (re)deliver packs to a running VM
  spike                          boot images/out end to end without the core
  version
`)
}

// connect returns a client for the default socket.
func connect() (*client.Client, error) {
	root, err := store.Default()
	if err != nil {
		return nil, err
	}
	return client.New(root.Socket()), nil
}
