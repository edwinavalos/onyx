package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/edwinavalos/onyx/internal/client"
	"golang.org/x/term"
)

// detachKey is Ctrl-] (like telnet). Typing it on the console detaches
// without stopping the VM.
const detachKey = 0x1d

// attachConsole puts the local terminal in raw mode and bridges it to the
// VM console until the VM goes away or the user presses the detach key.
// It returns errDetached when the user detached.
func attachConsole(ctx context.Context, cl *client.Client, name string) error {
	conn, err := cl.Console(ctx, name)
	if err != nil {
		return err
	}
	defer conn.Close()

	fd := int(os.Stdin.Fd())
	isTTY := term.IsTerminal(fd)
	if isTTY {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer func() { _ = term.Restore(fd, old) }()
		fmt.Fprintf(os.Stderr, "onyx: attached to %s (Ctrl-] to detach)\r\n", name)
		syncSize(ctx, cl, name)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Terminal resizes → guest.
	if isTTY {
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for {
				select {
				case <-winch:
					syncSize(ctx, cl, name)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Guest → terminal.
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(os.Stdout, conn)
		done <- err
	}()
	// Terminal → guest, watching for the detach key.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if i := indexByte(buf[:n], detachKey); i >= 0 && isTTY {
					if i > 0 {
						_, _ = conn.Write(buf[:i])
					}
					done <- errDetached
					return
				}
				if _, werr := conn.Write(buf[:n]); werr != nil {
					done <- werr
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case err := <-done:
		if isTTY {
			fmt.Fprint(os.Stderr, "\r\n")
		}
		if errors.Is(err, errDetached) {
			fmt.Fprintln(os.Stderr, "onyx: detached")
			return errDetached
		}
		var ne net.Error
		if err == nil || errors.Is(err, io.EOF) || errors.As(err, &ne) {
			fmt.Fprintln(os.Stderr, "onyx: console closed")
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

var errDetached = errors.New("detached")

func syncSize(ctx context.Context, cl *client.Client, name string) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || rows <= 0 || cols <= 0 {
		return
	}
	_ = cl.Resize(ctx, name, uint16(rows), uint16(cols)) // #nosec G115 -- terminal sizes are small
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
