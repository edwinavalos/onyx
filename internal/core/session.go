package core

import (
	"fmt"
	"io"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// SetSession tells the guest what the console login should run.
func (c *Core) SetSession(vmName string, s vsockproto.Session) error {
	inst, err := c.instance(vmName)
	if err != nil {
		return err
	}
	if s.Rows > 0 && s.Cols > 0 {
		inst.console.setSize(s.Rows, s.Cols)
	}
	_, err = inst.call(vsockproto.Request{Op: "session", Session: &s})
	return err
}

// Resize updates the console size on both ends. During a start there is
// no guest to tell yet; the size is kept and shapes the session the start
// delivers (the terminal attaches, and reports its size, while the VM is
// still booting).
func (c *Core) Resize(vmName string, rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return fmt.Errorf("resize: rows and cols required")
	}
	c.mu.Lock()
	inst, ok := c.running[vmName]
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("vm %q is not running", vmName)
	}
	inst.setTermSize(rows, cols)
	if inst.machine == nil || !inst.ready {
		return nil // StartVM applies it with the session
	}
	inst.console.setSize(rows, cols)
	_, err := inst.call(vsockproto.Request{Op: "winsize", Rows: rows, Cols: cols})
	return err
}

// AttachConsole streams the VM console to w and returns the input writer
// and a detach function.
func (c *Core) AttachConsole(vmName string, w io.Writer) (io.Writer, func(), error) {
	// Unlike other operations this is allowed during a start: the console
	// exists before the machine, and watching the boot is the point.
	c.mu.Lock()
	inst, ok := c.running[vmName]
	c.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("vm %q is not running", vmName)
	}
	if inst.console == nil {
		return nil, nil, fmt.Errorf("vm %q is starting; console not ready", vmName)
	}
	in, detach := inst.console.attach(w)
	return in, detach, nil
}

func (c *Core) instance(name string) (*instance, error) {
	c.mu.Lock()
	inst, ok := c.running[name]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("vm %q is not running", name)
	}
	// StartVM reserves the slot before building the machine; nothing may
	// touch the instance until then.
	if inst.machine == nil {
		return nil, fmt.Errorf("vm %q is starting", name)
	}
	return inst, nil
}
