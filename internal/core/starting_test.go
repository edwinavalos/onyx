package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
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
		"StartVM":      func() error { return c.StartVM(ctx, name) },
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
