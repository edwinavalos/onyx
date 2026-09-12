package core

// PauseVM freezes a running VM in place (memory stays resident).
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
