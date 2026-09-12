package core

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

// Suspend-to-disk is deliberately absent: Virtualization.framework accepts
// SaveMachineStateToPath for Linux guests but RestoreMachineStateFromURL
// then fails with EINVAL whatever the device set (see `onyx probe-restore`
// and docs/decisions.md D14). Pause/resume work and cover host sleep.
