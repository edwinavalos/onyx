package core

import (
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

// ReserveStarting puts name into the state StartVM leaves it in while the
// machine is still being built (slot taken, machine nil). Test-only.
func (c *Core) ReserveStarting(name string) {
	c.mu.Lock()
	c.running[name] = &instance{cfg: store.VMConfig{Name: name}, started: time.Now()}
	c.mu.Unlock()
}
