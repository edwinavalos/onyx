// Package workspace manages Onyx workspaces: Onyx-owned directories shared
// into VMs over virtiofs so multiple guests can access them concurrently
// (see docs/decisions.md D8, D21). Unlike a volume (a raw disk image
// attached as a virtio-blk device, exclusive to one running VM at a time),
// a workspace is a plain directory: virtiofs mediates access per file
// through the host process, so it is safe for more than one VM to have it
// mounted at once.
package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultName is the workspace used when a caller does not name one.
const DefaultName = "default"

// markerFile records that a directory is an Onyx-managed workspace, not an
// incidental directory something else created at that path.
const markerFile = ".onyx-workspace"

// DefaultDir is where workspaces live unless overridden.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Onyx", "workspaces"), nil
}

// Path returns the directory for a named workspace in dir.
func Path(dir, name string) string {
	return filepath.Join(dir, name)
}

// Tag returns the virtiofs tag a workspace is shared under.
func Tag(name string) string {
	return "onyx-ws-" + name
}

// Metadata is a workspace's persisted marker file content.
type Metadata struct {
	Name       string    `json:"name"`
	Created    time.Time `json:"created"`
	AutoAttach bool      `json:"auto_attach"`
}

// Ensure creates the directory for name in dir if it does not already
// exist, along with its marker file, and returns its path. It is a no-op
// on a workspace that already exists.
func Ensure(dir, name string) (string, error) {
	p := Path(dir, name)
	mp := markerPath(p)
	if _, err := os.Stat(mp); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(p, 0o750); err != nil {
		return "", fmt.Errorf("workspace %s: %w", name, err)
	}
	meta := Metadata{Name: name, Created: time.Now().UTC()}
	if err := writeMeta(mp, meta); err != nil {
		return "", fmt.Errorf("workspace %s: %w", name, err)
	}
	return p, nil
}

// Meta reads a workspace's marker file.
func Meta(dir, name string) (Metadata, error) {
	var meta Metadata
	b, err := os.ReadFile(markerPath(Path(dir, name))) // #nosec G304 -- path built from a name we own
	if err != nil {
		return meta, fmt.Errorf("workspace %s: %w", name, err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return meta, fmt.Errorf("workspace %s: %w", name, err)
	}
	return meta, nil
}

// SetAutoAttach marks a workspace to be attached to every new VM without an
// explicit per-VM mount. It errors if the workspace does not exist.
func SetAutoAttach(dir, name string, on bool) error {
	meta, err := Meta(dir, name)
	if err != nil {
		return err
	}
	meta.AutoAttach = on
	if err := writeMeta(markerPath(Path(dir, name)), meta); err != nil {
		return fmt.Errorf("workspace %s: %w", name, err)
	}
	return nil
}

func markerPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, markerFile)
}

func writeMeta(path string, meta Metadata) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640) // #nosec G306 -- guest-only metadata, not a secret
}
