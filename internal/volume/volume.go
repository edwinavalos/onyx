// Package volume manages Onyx volumes: raw disk image files attached to VMs
// as block devices (see docs/decisions.md D8).
package volume

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultDir is where volumes live unless overridden.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Onyx", "volumes"), nil
}

// Path returns the image path for a named volume in dir.
func Path(dir, name string) string {
	return filepath.Join(dir, name+".img")
}

// Ensure creates a sparse raw image of sizeMB for name in dir if it does not
// already exist, and returns its path. The guest formats it on first mount.
func Ensure(dir, name string, sizeMB int64) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	p := Path(dir, name)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path built from a name we own
	if err != nil {
		return "", fmt.Errorf("create volume %s: %w", name, err)
	}
	defer f.Close()
	if err := f.Truncate(sizeMB * 1024 * 1024); err != nil {
		return "", fmt.Errorf("size volume %s: %w", name, err)
	}
	return p, nil
}
