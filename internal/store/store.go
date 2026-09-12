// Package store owns Onyx's on-disk layout under the application support
// directory:
//
//	images/<name>/{vmlinux,initramfs,rootfs.img}   base guest images
//	volumes/<name>.img                             user volumes (D8)
//	vms/<name>/config.json                         VM definitions
//	vms/<name>/root.img                            per-VM root disk (APFS clone of the image)
//	vms/<name>/console.log                         serial console output
//	onyx.sock                                      core API socket
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Root is the base directory for all Onyx state.
type Root struct{ Dir string }

// Default returns the per-user application support root.
func Default() (Root, error) {
	if d := os.Getenv("ONYX_HOME"); d != "" {
		return Root{Dir: d}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Root{}, err
	}
	return Root{Dir: filepath.Join(home, "Library", "Application Support", "Onyx")}, nil
}

func (r Root) ImagesDir() string  { return filepath.Join(r.Dir, "images") }
func (r Root) VolumesDir() string { return filepath.Join(r.Dir, "volumes") }
func (r Root) VMsDir() string     { return filepath.Join(r.Dir, "vms") }

// Socket returns the core API socket path. ONYX_SOCKET overrides it, which
// matters because macOS limits Unix socket paths to 104 bytes.
func (r Root) Socket() string {
	if s := os.Getenv("ONYX_SOCKET"); s != "" {
		return s
	}
	return filepath.Join(r.Dir, "onyx.sock")
}

func (r Root) ImageDir(name string) string   { return filepath.Join(r.ImagesDir(), name) }
func (r Root) VolumePath(name string) string { return filepath.Join(r.VolumesDir(), name+".img") }
func (r Root) VMDir(name string) string      { return filepath.Join(r.VMsDir(), name) }

// Init creates the directory skeleton.
func (r Root) Init() error {
	for _, d := range []string{r.ImagesDir(), r.VolumesDir(), r.VMsDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	return nil
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// ValidName reports whether name is safe to use as a path component.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid name %q: use letters, digits, '.', '_' or '-'", name)
	}
	return nil
}

// VolumeMount attaches a named volume at a guest path.
type VolumeMount struct {
	Volume string `json:"volume"`
	Target string `json:"target"`
}

// VMConfig is the persisted definition of a VM.
type VMConfig struct {
	Name     string        `json:"name"`
	Image    string        `json:"image"`
	CPUs     uint          `json:"cpus"`
	MemoryMB uint64        `json:"memory_mb"`
	Volumes  []VolumeMount `json:"volumes,omitempty"`
	Cmdline  string        `json:"cmdline,omitempty"`
}

// DefaultCmdline is the kernel command line used when a VM config has none.
const DefaultCmdline = "console=hvc0 root=/dev/vda rootfstype=ext4 rw modules=ext4,virtio_blk,virtio_pci quiet"

// ErrNotFound is returned when a named object does not exist.
var ErrNotFound = errors.New("not found")

// LoadVM reads vms/<name>/config.json.
func (r Root) LoadVM(name string) (VMConfig, error) {
	var c VMConfig
	b, err := os.ReadFile(filepath.Join(r.VMDir(name), "config.json")) // #nosec G304 -- name validated
	if errors.Is(err, os.ErrNotExist) {
		return c, fmt.Errorf("vm %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// SaveVM writes vms/<name>/config.json.
func (r Root) SaveVM(c VMConfig) error {
	if err := ValidName(c.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(r.VMDir(c.Name), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.VMDir(c.Name), "config.json"), b, 0o600)
}

// ListVMs returns the names of all defined VMs.
func (r Root) ListVMs() ([]string, error) { return listDirs(r.VMsDir()) }

// ListImages returns the names of all installed images.
func (r Root) ListImages() ([]string, error) { return listDirs(r.ImagesDir()) }

// ListVolumes returns the names of all volumes.
func (r Root) ListVolumes() ([]string, error) {
	ents, err := os.ReadDir(r.VolumesDir())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if n, ok := cutSuffix(e.Name(), ".img"); ok && !e.IsDir() {
			out = append(out, n)
		}
	}
	return out, nil
}

func listDirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func cutSuffix(s, suf string) (string, bool) {
	if len(s) > len(suf) && s[len(s)-len(suf):] == suf {
		return s[:len(s)-len(suf)], true
	}
	return s, false
}
