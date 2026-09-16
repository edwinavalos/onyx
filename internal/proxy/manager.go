package proxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// SocketName is the proxy process's Unix socket under the Onyx root.
const SocketName = "proxy.sock"

// SocketPath is where the proxy for root listens: ONYX_PROXY_SOCKET, else
// <root>/proxy.sock — unless that exceeds what macOS allows for a Unix
// socket path (104 bytes; temp roots under /var/folders do), in which
// case a short per-root name under /tmp.
func SocketPath(root string) string {
	if s := os.Getenv("ONYX_PROXY_SOCKET"); s != "" {
		return s
	}
	p := filepath.Join(root, SocketName)
	if len(p) < 100 {
		return p
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join("/tmp", fmt.Sprintf("onyx-proxy-%x.sock", sum[:6]))
}

// Manager runs the proxy for the core: as a child process (`onyx proxy`)
// that it spawns, watches and restarts, replaying every VM's configuration
// after a restart; or in-process when InProcess is set (tests,
// ONYX_PROXY_INPROC=1). The core never sees a proxied credential either
// way — with a child it also never holds one in its address space.
type Manager struct {
	Root      string // state dir: socket, CA, logs
	Exe       string // binary to spawn; "" means the running executable
	InProcess bool
	// Source is the in-process credential source; nil means the Keychain.
	Source SecretSource

	client Client
	mu     sync.Mutex
	want   map[string]VMConfig
	stdin  io.WriteCloser // child's stdin; closing it tells the child to exit
}

// Start brings the proxy up and returns once it answers.
func (m *Manager) Start(ctx context.Context) error {
	m.client = Client{Socket: SocketPath(m.Root)}
	_ = os.Remove(m.client.Socket)
	if m.InProcess {
		ca, err := LoadOrCreateCA(m.Root)
		if err != nil {
			return err
		}
		src := m.Source
		if src == nil {
			src = &Cached{Source: Keychain{}}
		}
		l, err := (&net.ListenConfig{}).Listen(ctx, "unix", m.client.Socket)
		if err != nil {
			return err
		}
		s := &Server{CA: ca, Source: src, LogDir: m.Root}
		go func() { _ = s.Serve(ctx, l) }()
		return m.waitReady(ctx)
	}
	if err := m.spawn(ctx); err != nil {
		return err
	}
	return m.waitReady(ctx)
}

func (m *Manager) spawn(ctx context.Context) error {
	exe := m.Exe
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, exe, "proxy", "-root", m.Root, "-socket", m.client.Socket) // #nosec G204 -- our own binary
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start proxy process: %w", err)
	}
	m.mu.Lock()
	m.stdin = stdin
	m.mu.Unlock()
	slog.Info("core: proxy process started", "pid", cmd.Process.Pid)
	go m.watch(ctx, cmd)
	return nil
}

// watch restarts the child if it dies while the core is still running and
// replays the VMs it should be serving.
func (m *Manager) watch(ctx context.Context, cmd *exec.Cmd) {
	err := cmd.Wait()
	if ctx.Err() != nil {
		return
	}
	slog.Warn("core: proxy process exited; restarting", "err", err)
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 10*time.Second) {
		if err := m.spawn(ctx); err == nil {
			if err := m.waitReady(ctx); err == nil {
				m.replay(ctx)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (m *Manager) replay(ctx context.Context) {
	m.mu.Lock()
	want := make(map[string]VMConfig, len(m.want))
	for k, v := range m.want {
		want[k] = v
	}
	m.mu.Unlock()
	for vm, cfg := range want {
		if _, err := m.client.Configure(ctx, vm, cfg); err != nil {
			slog.Warn("core: proxy replay failed", "vm", vm, "err", err)
		}
	}
}

func (m *Manager) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		err := m.client.Ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("proxy process did not answer: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stop asks a child to exit. In-process servers stop with their context.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stdin != nil {
		_ = m.stdin.Close()
		m.stdin = nil
	}
}

// Configure installs a VM's proxies (remembered for replay) and returns
// the CA the guest should trust.
func (m *Manager) Configure(ctx context.Context, vm string, cfg VMConfig) ([]byte, error) {
	m.mu.Lock()
	if m.want == nil {
		m.want = map[string]VMConfig{}
	}
	m.want[vm] = cfg
	m.mu.Unlock()
	return m.client.Configure(ctx, vm, cfg)
}

// Remove forgets a VM.
func (m *Manager) Remove(ctx context.Context, vm string) error {
	m.mu.Lock()
	delete(m.want, vm)
	m.mu.Unlock()
	return m.client.Remove(ctx, vm)
}

// Splice serves guest through the named handler: bytes are copied both
// ways until either side closes. It returns when done.
func (m *Manager) Splice(ctx context.Context, guest net.Conn, vm, kind string, index int) {
	defer guest.Close()
	p, err := m.client.Serve(ctx, vm, kind, index)
	if err != nil {
		slog.Warn("core: proxy unavailable", "vm", vm, "kind", kind, "err", err)
		return
	}
	defer p.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(p, guest); closeWrite(p); done <- struct{}{} }()
	go func() { _, _ = io.Copy(guest, p); closeWrite(guest); done <- struct{}{} }()
	<-done
	<-done
}

// ServeListener accepts guest connections on l (a host vsock listener)
// and splices each through the named handler until l closes.
func (m *Manager) ServeListener(ctx context.Context, l net.Listener, vm, kind string, index int) {
	for {
		c, err := l.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				slog.Debug("core: proxy listener closed", "vm", vm, "err", err)
			}
			return
		}
		go m.Splice(ctx, c, vm, kind, index)
	}
}
