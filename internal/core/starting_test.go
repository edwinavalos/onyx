package core

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// newTestCore returns a core over a temp root with one defined (never
// booted) VM. Nothing here touches Virtualization.
func newTestCore(t *testing.T) (*Core, string) {
	t.Helper()
	root := store.Root{Dir: t.TempDir()}
	c, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	const name = "vm1"
	if err := root.SaveVM(store.VMConfig{Name: name, Image: "base", CPUs: 1, MemoryMB: 256}); err != nil {
		t.Fatal(err)
	}
	return c, name
}

// reserveStarting puts the VM in the state StartVM leaves it in while the
// machine is still being built: present in c.running, machine nil.
func reserveStarting(c *Core, name string) {
	c.mu.Lock()
	c.running[name] = &instance{cfg: store.VMConfig{Name: name}, started: time.Now()}
	c.mu.Unlock()
}

// withTimeout fails the test if fn has not returned in time — a wedged
// core mutex shows up as a hang, not an error.
func withTimeout(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: hung (core mutex wedged?)", what)
	}
}

// Regression: ListVMs/GetVM panicked on a starting VM's nil machine while
// holding c.mu, wedging the core (every later request hung; the app could
// not delete or list VMs).
func TestGetVMWhileStarting(t *testing.T) {
	c, name := newTestCore(t)
	reserveStarting(c, name)

	var st VMStatus
	withTimeout(t, "GetVM", func() {
		var err error
		st, err = c.GetVM(name)
		if err != nil {
			t.Error(err)
		}
	})
	if st.State != "starting" {
		t.Errorf("state = %q, want starting", st.State)
	}
	withTimeout(t, "ListVMs", func() {
		list, err := c.ListVMs()
		if err != nil || len(list) != 1 || list[0].State != "starting" {
			t.Errorf("ListVMs = %+v, %v", list, err)
		}
	})
	// The core must still be usable afterwards.
	withTimeout(t, "GetVM again", func() { _, _ = c.GetVM(name) })
}

// Every operation that reaches into a running VM must refuse a starting
// one with an error rather than dereferencing its nil machine.
func TestOperationsWhileStarting(t *testing.T) {
	c, name := newTestCore(t)
	reserveStarting(c, name)
	ctx := context.Background()

	ops := map[string]func() error{
		"Exec":         func() error { _, err := c.Exec(name, []string{"true"}); return err },
		"PauseVM":      func() error { return c.PauseVM(name) },
		"ResumeVM":     func() error { return c.ResumeVM(name) },
		"SuspendVM":    func() error { return c.SuspendVM(ctx, name) },
		"Tunnel":       func() error { _, err := c.Tunnel(ctx, name, 22); return err },
		"DeliverPacks": func() error { return c.DeliverPacks(ctx, name, []string{"p"}) },
		"StartVM":      func() error { return c.StartVM(ctx, name, nil) },
	}
	for op, fn := range ops {
		withTimeout(t, op, func() {
			err := fn()
			if err == nil || !strings.Contains(err.Error(), "starting") && !strings.Contains(err.Error(), "already") {
				t.Errorf("%s: err = %v, want a 'starting' error", op, err)
			}
		})
	}
	// RemoveVM must not delete the directory out from under a start in progress.
	withTimeout(t, "RemoveVM", func() {
		if err := c.RemoveVM(name); err == nil {
			t.Error("RemoveVM: succeeded on a starting VM")
		}
	})
}

// A fully stopped VM can be removed and stops showing up.
func TestRemoveVM(t *testing.T) {
	c, name := newTestCore(t)
	withTimeout(t, "RemoveVM", func() {
		if err := c.RemoveVM(name); err != nil {
			t.Fatal(err)
		}
	})
	list, err := c.ListVMs()
	if err != nil || len(list) != 0 {
		t.Errorf("after remove: %+v, %v", list, err)
	}
}

// StopVM on a starting VM cancels the start instead of being refused —
// the app's Cancel button for a VM that is taking too long.
func TestStopCancelsStart(t *testing.T) {
	c, name := newTestCore(t)
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[name] = &instance{cfg: store.VMConfig{Name: name}, started: time.Now(), cancel: cancel}
	c.mu.Unlock()

	withTimeout(t, "StopVM", func() {
		if err := c.StopVM(context.Background(), name); err != nil {
			t.Errorf("StopVM on starting vm: %v", err)
		}
	})
	select {
	case <-ctx.Done():
	default:
		t.Error("start context not cancelled")
	}
	// The slot is released so the VM can be deleted or started again.
	st, _ := c.GetVM(name)
	if st.State != "stopped" {
		t.Errorf("state after cancel = %q, want stopped", st.State)
	}
	if err := c.RemoveVM(name); err != nil {
		t.Errorf("RemoveVM after cancel: %v", err)
	}
}

// A guest image that predates an optional op must not stop the VM from
// starting; the feature is just unavailable.
func TestGuestLacksOp(t *testing.T) {
	if !guestLacksOp(errors.New(`deliver ssh key: guest: unknown op sshkey`)) {
		t.Error("unknown op not recognised")
	}
	if guestLacksOp(errors.New("guest: timeout")) || guestLacksOp(nil) {
		t.Error("false positive")
	}
}

// The app attaches the console as soon as a start begins, so the user
// watches the guest boot instead of a spinner: the console exists before
// the machine does.
func TestAttachConsoleWhileStarting(t *testing.T) {
	c, name := newTestCore(t)
	con, err := newConsole(filepath.Join(t.TempDir(), "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer con.close()
	c.mu.Lock()
	c.running[name] = &instance{cfg: store.VMConfig{Name: name}, started: time.Now(), console: con}
	c.mu.Unlock()

	var buf bytes.Buffer
	withTimeout(t, "AttachConsole", func() {
		in, detach, err := c.AttachConsole(name, &buf)
		if err != nil {
			t.Fatalf("attach while starting: %v", err)
		}
		if in == nil || detach == nil {
			t.Fatal("nil writer/detach")
		}
		detach()
	})
	// Before the console exists there is nothing to attach to.
	c.mu.Lock()
	c.running[name] = &instance{cfg: store.VMConfig{Name: name}}
	c.mu.Unlock()
	if _, _, err := c.AttachConsole(name, &buf); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Errorf("attach with no console: err = %v", err)
	}
}

// A start with no session still leaves the console usable: the guest gets
// a plain shell session rather than waiting for one that never comes.
func TestSessionForStart(t *testing.T) {
	inst := &instance{}
	if s := sessionForStart(nil, inst); s.OnExit != "shell" || s.Cmd != "" {
		t.Errorf("default session = %+v", s)
	}
	want := vsockproto.Session{Dir: "/home/dev/work", Cmd: "claude", OnExit: "poweroff"}
	if s := sessionForStart(&want, inst); s != want {
		t.Errorf("explicit session = %+v", s)
	}
}

// The app's terminal reports its size as soon as it attaches, which is
// while the VM is still starting. That size must win over the session's
// nominal rows/cols when the session is delivered, or a full-screen
// program in the guest draws for the wrong grid.
func TestResizeWhileStartingShapesTheSession(t *testing.T) {
	c, name := newTestCore(t)
	reserveStarting(c, name)

	withTimeout(t, "Resize", func() {
		if err := c.Resize(name, 50, 160); err != nil {
			t.Fatalf("resize during start: %v", err)
		}
	})
	c.mu.Lock()
	inst := c.running[name]
	c.mu.Unlock()
	s := sessionForStart(&vsockproto.Session{Cmd: "claude", Rows: 40, Cols: 120}, inst)
	if s.Rows != 50 || s.Cols != 160 || s.Cmd != "claude" {
		t.Fatalf("session = %+v, want the terminal's 50x160 with the command kept", s)
	}
}

func TestSessionKeepsItsSizeWithoutATerminal(t *testing.T) {
	c, name := newTestCore(t)
	reserveStarting(c, name)
	c.mu.Lock()
	inst := c.running[name]
	c.mu.Unlock()
	s := sessionForStart(&vsockproto.Session{Rows: 40, Cols: 120}, inst)
	if s.Rows != 40 || s.Cols != 120 {
		t.Fatalf("session = %+v, want 40x120", s)
	}
	if d := sessionForStart(nil, inst); d.OnExit != "shell" {
		t.Fatalf("default session = %+v", d)
	}
}

// A volume attached to a running VM cannot be attached to a second one
// (Virtualization opens the disk exclusively); refuse before touching
// Virtualization, and name the holder instead of surfacing VZErrorDomain
// "The storage device attachment is invalid".
func TestStartRefusesVolumeHeldByRunningVM(t *testing.T) {
	c, name := newTestCore(t)
	if err := c.root.SaveVM(store.VMConfig{Name: name, Image: "base", CPUs: 1, MemoryMB: 256,
		Volumes: []store.VolumeMount{{Volume: "shared", Target: "/mnt"}}}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.running["other"] = &instance{cfg: store.VMConfig{Name: "other",
		Volumes: []store.VolumeMount{{Volume: "shared", Target: "/data"}}}, ready: true, started: time.Now()}
	c.mu.Unlock()

	var err error
	withTimeout(t, "StartVM", func() { err = c.StartVM(context.Background(), name, nil) })
	if err == nil || !strings.Contains(err.Error(), `volume "shared" is attached to running vm "other"`) {
		t.Fatalf("StartVM error = %v", err)
	}
	c.mu.Lock()
	_, reserved := c.running[name]
	c.mu.Unlock()
	if reserved {
		t.Fatal("refused start left the VM reserved as running")
	}
}
