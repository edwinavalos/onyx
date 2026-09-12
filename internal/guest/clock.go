package guest

import (
	"fmt"
	"syscall"
	"time"
)

// setClock sets the guest wall clock. A VM restored from a snapshot wakes up
// believing no time has passed; the host tells it otherwise.
func setClock(unixNanos int64) error {
	if unixNanos <= 0 {
		return fmt.Errorf("clock: time required")
	}
	t := time.Unix(0, unixNanos)
	tv := syscall.NsecToTimeval(t.UnixNano())
	if err := syscall.Settimeofday(&tv); err != nil {
		return fmt.Errorf("settimeofday: %w", err)
	}
	return nil
}
