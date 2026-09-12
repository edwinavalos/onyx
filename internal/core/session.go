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

// Resize updates the console size on both ends.
func (c *Core) Resize(vmName string, rows, cols uint16) error {
	inst, err := c.instance(vmName)
	if err != nil {
		return err
	}
	inst.console.setSize(rows, cols)
	_, err = inst.call(vsockproto.Request{Op: "winsize", Rows: rows, Cols: cols})
	return err
}

// AttachConsole streams the VM console to w and returns the input writer
// and a detach function.
func (c *Core) AttachConsole(vmName string, w io.Writer) (io.Writer, func(), error) {
	inst, err := c.instance(vmName)
	if err != nil {
		return nil, nil, err
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
	return inst, nil
}
