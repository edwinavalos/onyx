package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// sshUser is the guest work user (matches images/build-alpine.sh).
const sshUser = "dev"

// runSSH implements `onyx ssh <vm> [cmd...]` (also reachable as `ossh`):
// a real ssh session into the VM, over a vsock tunnel rather than the
// network, so it works for restricted and none VMs too. A stopped or
// suspended VM is started first.
func runSSH(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: onyx ssh <vm> [cmd...]  (or: ossh <vm> [cmd...])")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	if err := ensureRunning(ctx, cl, args[0]); err != nil {
		return err
	}
	var remote []string
	if len(args) > 1 {
		// sshd runs a one-shot command in a non-login shell, which would
		// skip ~/.profile and so the delivered secrets and proxies; run it
		// the way the interactive session would.
		remote = []string{"bash", "-lc", shellQuote(strings.Join(args[1:], " "))}
	}
	return execSSH(ctx, args[0], remote, false)
}

// runClaude implements `onyx claude <vm> [claude args...]` (also
// `oclaude`): ssh in and start Claude Code in the work directory with the
// delivered secrets and proxies in its environment.
func runClaude(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: onyx claude <vm> [claude args...]  (or: oclaude <vm> [claude args...])")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	if err := ensureRunning(ctx, cl, args[0]); err != nil {
		return err
	}
	// A login shell sources ~/.profile, which loads /run/onyx/env and
	// routes Claude Code through the credential proxy when there is one.
	// Work volumes mount at ~/work/<volume> (D14): land in the volume when
	// there is exactly one, else in ~/work, else in ~.
	remote := []string{"bash", "-lc", shellQuote(`cd "$HOME"/work/*/ 2>/dev/null || cd "$HOME/work" 2>/dev/null || cd "$HOME"; exec claude "$@"`), "claude"}
	for _, a := range args[1:] {
		remote = append(remote, shellQuote(a))
	}
	return execSSH(ctx, args[0], remote, true)
}

// ensureRunning starts the VM if it is stopped or suspended.
func ensureRunning(ctx context.Context, cl *client.Client, name string) error {
	st, err := cl.GetVM(ctx, name)
	if err != nil {
		return err
	}
	switch st.State {
	case "running":
		return nil
	case "stopped", "suspended":
		fmt.Fprintf(os.Stderr, "onyx: starting %s...\n", name)
		_, err := cl.StartVM(ctx, name, nil)
		return err
	}
	return fmt.Errorf("vm %q is %s", name, st.State)
}

// execSSH replaces this process with ssh. The ProxyCommand is this same
// binary running `vm dial`, so no port is ever opened on the host.
func execSSH(ctx context.Context, name string, remote []string, tty bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// `ossh`/`oclaude` are symlinks; the ProxyCommand must run as onyx.
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	root, err := store.Default()
	if err != nil {
		return err
	}
	key := filepath.Join(root.Dir, "ssh", "id_ed25519")
	if _, err := os.Stat(key); err != nil {
		return fmt.Errorf("ssh key %s not found: has this VM been started by this core?", key)
	}
	sshArgs := []string{
		"-o", "ProxyCommand=" + shellQuote(self) + " vm dial %h " + strconv.Itoa(int(vsockproto.SSHPort)),
		// The tunnel is host-local vsock; there is nobody to spoof a host key.
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-i", key,
	}
	if tty {
		sshArgs = append(sshArgs, "-t")
	}
	sshArgs = append(sshArgs, sshUser+"@"+name)
	if len(remote) > 0 {
		sshArgs = append(sshArgs, "--")
		sshArgs = append(sshArgs, remote...)
	}
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...) // #nosec G204 -- arguments are ours
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	return err
}

// runDial implements `onyx vm dial <vm> <port>`: stdio ↔ a guest vsock
// port. It exists to be ssh's ProxyCommand.
func runDial(ctx context.Context, cl *client.Client, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: onyx vm dial <vm> <port>")
	}
	port, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil {
		return fmt.Errorf("port: %w", err)
	}
	conn, err := cl.Tunnel(ctx, args[0], uint32(port))
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(conn, os.Stdin); done <- struct{}{} }()
	go func() { _, _ = io.Copy(os.Stdout, conn); done <- struct{}{} }()
	<-done
	return nil
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
