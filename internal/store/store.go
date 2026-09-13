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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
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
func (r Root) CrashesDir() string { return filepath.Join(r.Dir, "crashes") } // guest kernel traces, kept past VM removal

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
	Packs    []string      `json:"packs,omitempty"`
	Cmdline  string        `json:"cmdline,omitempty"`
	// MAC is the NIC's hardware address, fixed at creation so the guest keeps
	// its DHCP lease and saved state restores cleanly.
	MAC string `json:"mac,omitempty"`
	// Network is how the guest reaches the outside: NetworkNAT (default),
	// NetworkRestricted or NetworkNone.
	Network string `json:"network,omitempty"`
	// Allow lists the hosts a NetworkRestricted VM may reach through the
	// host's egress proxy: "host", "*.suffix", optionally ":port" (80 and
	// 443 when omitted).
	Allow []string `json:"allow,omitempty"`
	// OwnedVolumes are volumes CreateVM made for this definition that no
	// start has used yet. RemoveVM deletes them with the VM; the first
	// successful start clears the list, after which the volumes persist
	// like any other (decisions.md D18).
	OwnedVolumes []string `json:"owned_volumes,omitempty"`
}

// Network modes for VMConfig.Network.
const (
	// NetworkNAT gives the guest a NIC behind Virtualization's NAT: full
	// internet and the host.
	NetworkNAT = "nat"
	// NetworkRestricted attaches no NIC. HTTP(S) egress goes through a
	// host-side proxy over vsock that only admits hosts in Allow; nothing
	// else leaves the VM.
	NetworkRestricted = "restricted"
	// NetworkNone attaches no NIC and no proxy. Credential proxies still
	// work (they are vsock, not IP).
	NetworkNone = "none"
)

// ValidNetwork checks a VMConfig.Network value ("" means NetworkNAT).
func ValidNetwork(mode string) error {
	switch mode {
	case "", NetworkNAT, NetworkRestricted, NetworkNone:
		return nil
	}
	return fmt.Errorf("network %q: want %s, %s or %s", mode, NetworkNAT, NetworkRestricted, NetworkNone)
}

// DefaultCPUs and DefaultMemoryMB size a VM whose config gives no size.
// Small on purpose: sandboxes run on laptops next to everything else, and
// an oversized guest is what puts the host under memory pressure.
const (
	DefaultCPUs     uint   = 1
	DefaultMemoryMB uint64 = 512
)

// Guest locations shared by every session entry point (CLI, MCP, app).
// Work volumes mount at WorkRoot/<volume> rather than at WorkRoot itself so
// coding agents that key memory by working directory keep projects apart.
const (
	GuestHome = "/home/dev"
	WorkRoot  = GuestHome + "/work"
)

// WorkMountTarget is where the named work volume is mounted in the guest
// and where a session on it starts.
func WorkMountTarget(volume string) string { return WorkRoot + "/" + volume }

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

// VolumeInfo describes one volume: the size it was created with and how
// much of it is actually allocated on the host (images are sparse).
type VolumeInfo struct {
	Name   string `json:"name"`
	SizeMB int64  `json:"size_mb"`
	UsedMB int64  `json:"used_mb"`
}

// ListVolumeInfo returns every volume with its sizes.
func (r Root) ListVolumeInfo() ([]VolumeInfo, error) {
	names, err := r.ListVolumes()
	if err != nil {
		return nil, err
	}
	out := make([]VolumeInfo, 0, len(names))
	for _, n := range names {
		st, err := os.Stat(r.VolumePath(n))
		if err != nil {
			continue // removed between the listing and the stat
		}
		const mb = 1024 * 1024
		v := VolumeInfo{Name: n, SizeMB: st.Size() / mb}
		v.UsedMB = allocatedBytes(st) / mb
		if v.UsedMB > v.SizeMB {
			v.UsedMB = v.SizeMB
		}
		out = append(out, v)
	}
	return out, nil
}

// allocatedBytes is the space a file really occupies (st_blocks, in
// 512-byte units on every platform Onyx builds for); the apparent size
// when the stat carries no block count.
func allocatedBytes(st os.FileInfo) int64 {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return sys.Blocks * 512
	}
	return st.Size()
}

// ConsoleLogTail returns the last n bytes of a VM's serial console log
// (all of it when n <= 0), which the core keeps across stops. A VM that
// never booted has no log: ErrNotFound.
func (r Root) ConsoleLogTail(name string, n int) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(r.VMDir(name), "console.log")) // #nosec G304 -- name validated
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("console log for vm %q: %w", name, ErrNotFound)
		}
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if n > 0 && st.Size() > int64(n) {
		if _, err := f.Seek(-int64(n), io.SeekEnd); err != nil {
			return "", err
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
