package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// PauseVM freezes a running VM in place.
func (c *Core) PauseVM(name string) error {
	inst, err := c.instance(name)
	if err != nil {
		return err
	}
	return inst.machine.Pause()
}

// ResumeVM continues a paused VM.
func (c *Core) ResumeVM(name string) error {
	inst, err := c.instance(name)
	if err != nil {
		return err
	}
	return inst.machine.Resume()
}

// SuspendVM hibernates the guest: it writes its memory image to the VM's
// swap disk and powers off. The next StartVM boots the same kernel with
// resume=/dev/vdb and the guest continues where it left off.
//
// This is guest-side hibernation (swsusp), not Virtualization.framework
// save/restore, which does not work for Linux guests (see decisions D14).
func (c *Core) SuspendVM(ctx context.Context, name string) error {
	inst, err := c.instance(name)
	if err != nil {
		return err
	}
	if _, err := inst.call(vsockproto.Request{Op: "hibernate"}); err != nil {
		return fmt.Errorf("hibernate: %w", err)
	}
	// Writing the image can take a while for a busy guest.
	wait, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	select {
	case <-inst.machine.Stopped():
	case <-wait.Done():
		return fmt.Errorf("guest did not power off within 3 minutes; it is still running")
	}
	if err := os.WriteFile(c.hibernatedMarker(name), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	slog.Info("core: vm hibernated", "name", name)
	return nil
}

func (c *Core) hibernatedMarker(name string) string {
	return filepath.Join(c.root.VMDir(name), "hibernated")
}
