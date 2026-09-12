package core

import (
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// console owns the host side of a VM's serial port: a pty whose slave end is
// handed to Virtualization and whose master end is fanned out to a log file
// and any number of attached clients.
type console struct {
	master *os.File
	slave  *os.File
	log    *os.File

	mu      sync.Mutex
	clients map[io.Writer]struct{}
	recent  []byte // tail of output for late attachers
}

const recentMax = 64 * 1024

func newConsole(logPath string) (*console, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}
	// The pty is a transport, not a terminal: the guest owns line discipline.
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- path under the VM dir
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, err
	}
	c := &console{master: master, slave: slave, log: logf, clients: map[io.Writer]struct{}{}}
	go c.pump()
	return c, nil
}

func (c *console) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := c.master.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = c.log.Write(chunk)
			c.mu.Lock()
			c.recent = append(c.recent, chunk...)
			if len(c.recent) > recentMax {
				c.recent = c.recent[len(c.recent)-recentMax:]
			}
			for w := range c.clients {
				if _, err := w.Write(chunk); err != nil {
					delete(c.clients, w)
				}
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// attach starts streaming output to w (after replaying recent output) and
// returns a writer for input plus a detach func.
func (c *console) attach(w io.Writer) (io.Writer, func()) {
	c.mu.Lock()
	_, _ = w.Write(c.recent)
	c.clients[w] = struct{}{}
	c.mu.Unlock()
	return c.master, func() {
		c.mu.Lock()
		delete(c.clients, w)
		c.mu.Unlock()
	}
}

func (c *console) setSize(rows, cols uint16) {
	if err := pty.Setsize(c.master, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		slog.Debug("console: setsize", "err", err)
	}
}

// close tears the console down and hangs up on every attached client so
// their streams end even if Virtualization still holds the pty open.
func (c *console) close() {
	c.mu.Lock()
	clients := c.clients
	c.clients = map[io.Writer]struct{}{}
	c.mu.Unlock()
	for w := range clients {
		if cl, ok := w.(io.Closer); ok {
			_ = cl.Close()
		}
	}
	_ = c.slave.Close()
	_ = c.master.Close()
	_ = c.log.Close()
}
