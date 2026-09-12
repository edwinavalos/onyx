// onyx manages local VMs that host coding agents.
//
//	onyx serve                       run the core (holds the VMs)
//	onyx image  import|ls
//	onyx volume create|ls|rm
//	onyx run                         one-shot session VM running the harness
//	onyx cp                          copy files to/from a running VM
//	onyx mcp                         MCP server on stdio for coding harnesses
//	onyx vm     create|ls|start|stop|rm|status|exec|console
//	onyx secret set|link|ls|rm
//	onyx pack   create|ls|show|rm|deliver
//	onyx spike                       self-contained end-to-end check
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	if len(os.Args) < 2 && !isMultiCall() {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Multi-call: `ossh` and `oclaude` are symlinks to this binary.
	args := os.Args[1:]
	switch filepath.Base(os.Args[0]) {
	case "ossh":
		args = append([]string{"ssh"}, args...)
	case "oclaude":
		args = append([]string{"claude"}, args...)
	}

	var err error
	switch args[0] {
	case "version":
		fmt.Printf("onyx %s (%s)\n", version, commit)
	case "serve":
		err = runServe(ctx, args[1:])
	case "image":
		err = runImage(ctx, args[1:])
	case "volume":
		err = runVolume(ctx, args[1:])
	case "vm":
		err = runVM(ctx, args[1:])
	case "cp":
		err = runCp(ctx, args[1:])
	case "ssh":
		err = runSSH(ctx, args[1:])
	case "claude":
		err = runClaude(ctx, args[1:])
	case "run":
		err = runRun(ctx, args[1:])
	case "secret":
		err = runSecret(ctx, args[1:])
	case "pack":
		err = runPack(ctx, args[1:])
	case "spike":
		err = runSpike(args[1:])
	case "probe-restore": // dev aid: reproduces the Vz Linux save/restore failure
		err = runProbeRestore(args[1:])
	case "mcp":
		err = runMCP(ctx, args[1:])
	case "doctor":
		err = runDoctor(ctx)
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
  image rm <name>                (existing VMs keep their own root disks)
  volume create <name> [-size MB]
  volume ls
  volume rm <name>
  vm create <name> [-image base] [-cpus N] [-mem MB] [-volume name:/guest/path ...] [-pack name ...]
                                 [-network nat|restricted|none] [-allow host ...]
                                 restricted: no NIC; HTTP(S) only to -allow hosts through a host-side
                                 proxy (host, *.suffix, host:port). Decisions are logged to egress.log.
  vm ls
  vm start <name>
  vm stop <name>
  vm pause|resume <name>         freeze / continue a running VM
  vm suspend <name>              save the VM's memory and device state on the host and stop; the next start resumes it
  vm rm <name>
  vm status <name>
  vm dial <name> <port>          stdio to a guest vsock port (ssh's ProxyCommand)
  ssh <name> [cmd...]            ssh into a VM over vsock (works for restricted/none VMs; starts it if needed)
  claude <name> [args...]        ssh in and run Claude Code in ~/work with the delivered secrets
                                 ossh and oclaude are shorthands for these (symlinks made by make install)
  vm exec <name> -- <cmd...>
  vm console <name>              attach to the serial console (Ctrl-] detaches)
  cp <src> <dst>                 copy files in/out of a running VM; one side is vm:/abs/path
  run [-name N] [-pack P ...] [-cmd claude] [-dir /home/dev/work] [-keep]
                                 fresh VM + volumes, run the harness on the console, tear down on exit
  secret set <key> [-stdin]      store a secret in the macOS Keychain (prompts; never on argv)
  secret link <key> -claude-code | -service S [-account A] [-json PATH]
                                 resolve from another Keychain item at use time (never copied)
  secret ls
  secret rm <key>
  pack create <name> -secret key | key=ENV | key@/guest/path[:perm] | key>https://host[>auth] ...
                                 (proxy form keeps the secret on the host; git URLs are rewritten)
  pack ls | show <name> | rm <name>
  pack deliver <vm> <pack...>    (re)deliver packs to a running VM
  mcp                            serve the Model Context Protocol on stdio (for coding harnesses)
  doctor                         check platform, signing, docker, images, serve
  spike                          boot images/out end to end without the core
  version
`)
}

func isMultiCall() bool {
	b := filepath.Base(os.Args[0])
	return b == "ossh" || b == "oclaude"
}

// connect returns a client for the default socket.
func connect() (*client.Client, error) {
	root, err := store.Default()
	if err != nil {
		return nil, err
	}
	return client.New(root.Socket()), nil
}
