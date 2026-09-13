//go:build e2e

// Package e2e drives a real Onyx core — ./bin/onyx serve on a temp state
// dir with images/out imported — through internal/client, and looks at
// the result from three places: the API, the console rendered through a
// VT emulator (what a person sees), and the guest via exec.
//
//	make e2e
//
// Needs the signed binary and a built image; skips otherwise. Never
// touches the user's state dir. Serial: every scenario boots a VM.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// harness is the package-wide core under test.
type harness struct {
	repo     string // repository root
	dir      string // temp state dir (ONYX_HOME)
	socket   string
	cl       *client.Client
	serve    *exec.Cmd
	serveLog string
	seq      atomic.Int64
}

var (
	h          *harness
	skipReason string
)

const (
	startTimeout = 30 * time.Second
	stopTimeout  = 20 * time.Second
)

func TestMain(m *testing.M) {
	code := run(m)
	os.Exit(code)
}

func run(m *testing.M) int {
	var err error
	h, err = startCore()
	if err != nil {
		skipReason = err.Error()
		fmt.Fprintln(os.Stderr, "e2e: skipping:", skipReason)
		return m.Run()
	}
	defer h.stop()
	return m.Run()
}

// startCore builds the harness or returns the reason the suite cannot run.
func startCore() (*harness, error) {
	repo, err := filepath.Abs("..")
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(repo, "bin", "onyx")
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("%s missing (make sign)", bin)
	}
	imageDir := filepath.Join(repo, "images", "out")
	if _, err := os.Stat(filepath.Join(imageDir, "rootfs.img")); err != nil {
		return nil, fmt.Errorf("%s has no rootfs.img (make image)", imageDir)
	}
	// Socket paths cap at 104 bytes on macOS; the system temp dir is short.
	dir, err := os.MkdirTemp("", "onyx-e2e-")
	if err != nil {
		return nil, err
	}
	hh := &harness{repo: repo, dir: dir, socket: filepath.Join(dir, "s.sock"), serveLog: filepath.Join(dir, "serve.log")}
	logf, err := os.Create(hh.serveLog)
	if err != nil {
		return nil, err
	}
	hh.serve = exec.CommandContext(context.Background(), bin, "serve", "-http", "") // #nosec G204 -- our own binary under the repo
	hh.serve.Env = append(os.Environ(), "ONYX_HOME="+dir, "ONYX_SOCKET="+hh.socket)
	hh.serve.Stdout, hh.serve.Stderr = logf, logf
	if err := hh.serve.Start(); err != nil {
		return nil, err
	}
	hh.cl = client.New(hh.socket)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if err := hh.cl.Ping(ctx); err == nil {
			break
		}
		if ctx.Err() != nil {
			hh.stop()
			return nil, fmt.Errorf("core did not answer: %s", tail(hh.serveLog, 20))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := hh.cl.ImportImage(ctx, "base", imageDir); err != nil {
		hh.stop()
		return nil, fmt.Errorf("import image: %w", err)
	}
	return hh, nil
}

func (hh *harness) stop() {
	if hh.serve == nil || hh.serve.Process == nil {
		return
	}
	_ = hh.serve.Process.Signal(syscall.SIGINT)
	done := make(chan struct{})
	go func() { _ = hh.serve.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopTimeout):
		_ = hh.serve.Process.Kill()
		<-done
	}
	if os.Getenv("ONYX_E2E_KEEP") == "" {
		_ = os.RemoveAll(hh.dir)
	} else {
		fmt.Fprintln(os.Stderr, "e2e: state kept at", hh.dir)
	}
}

// need returns the harness or skips the test.
func need(t *testing.T) *harness {
	t.Helper()
	if h == nil {
		t.Skip(skipReason)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("serve.log tail:\n%s", tail(h.serveLog, 40))
		}
	})
	return h
}

// vm defines a VM named after the test with any tweaks applied, and
// removes it (stopping first) when the test ends.
func (hh *harness) vm(t *testing.T, mutate func(*store.VMConfig)) string {
	t.Helper()
	name := fmt.Sprintf("e2e-%d", hh.seq.Add(1))
	cfg := store.VMConfig{Name: name, Image: "base"}
	if mutate != nil {
		mutate(&cfg)
	}
	if _, err := hh.cl.CreateVM(context.Background(), cfg); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if st, err := hh.cl.GetVM(ctx, name); err == nil && st.State != "stopped" {
			_, _ = hh.cl.StopVM(ctx, name)
			hh.waitStopped(t, name)
		}
		_ = hh.cl.RemoveVM(ctx, name)
	})
	return name
}

// start boots a VM, with a session when sess is non-nil, and fails the
// test if the core does not report it running.
func (hh *harness) start(t *testing.T, name string, sess *vsockproto.Session) core.VMStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	st, err := hh.cl.StartVM(ctx, name, sess)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	if st.State != "running" {
		t.Fatalf("start %s: state %q after start", name, st.State)
	}
	return st
}

// waitStopped polls until the VM reports stopped or stopTimeout passes.
func (hh *harness) waitStopped(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(stopTimeout)
	for {
		st, err := hh.cl.GetVM(context.Background(), name)
		if err == nil && st.State == "stopped" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not stopped after %s (state %q, err %v)", name, stopTimeout, st.State, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// exec runs argv in the guest as root and returns its combined output.
func (hh *harness) exec(t *testing.T, name string, argv ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := hh.cl.Exec(ctx, name, argv)
	if err != nil {
		t.Fatalf("exec %s %q: %v\n%s", name, argv, err, out)
	}
	return out
}

// sh runs a shell snippet in the guest as root.
func (hh *harness) sh(t *testing.T, name, script string) string {
	t.Helper()
	return strings.TrimRight(hh.exec(t, name, "sh", "-c", script), "\n")
}

func isNotFound(err error) bool {
	return err != nil && (errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found"))
}

// tail returns the last n lines of a file, for failure reports.
func tail(path string, n int) string {
	b, err := os.ReadFile(path) // #nosec G304 -- log files under the temp state dir
	if err != nil {
		return err.Error()
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
