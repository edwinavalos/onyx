// Package core is the Onyx engine: it owns VM definitions, running machines,
// volumes and images, and is the only thing that touches Virtualization.
// Frontends (CLI, SwiftUI) talk to it through internal/api.
package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vm"
	"github.com/edwinavalos/onyx/internal/volume"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Core holds all state for one Onyx instance.
type Core struct {
	root store.Root

	mu      sync.Mutex
	running map[string]*instance
}

type instance struct {
	cfg     store.VMConfig
	machine *vm.Machine
	console *os.File
	stdin   *os.File
	started time.Time

	agentMu sync.Mutex
	agent   net.Conn
	agentRd *bufio.Reader
}

// New creates a Core over root, initialising the directory layout.
func New(root store.Root) (*Core, error) {
	if err := root.Init(); err != nil {
		return nil, err
	}
	return &Core{root: root, running: map[string]*instance{}}, nil
}

// Root exposes the storage root (for the API layer).
func (c *Core) Root() store.Root { return c.root }

// ---- Images ---------------------------------------------------------------

// ImportImage copies kernel/initrd/rootfs from dir into images/<name>.
func (c *Core) ImportImage(ctx context.Context, name, dir string) error {
	if err := store.ValidName(name); err != nil {
		return err
	}
	dst := c.root.ImageDir(name)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("image %q already exists", name)
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	for _, f := range []string{"vmlinux", "initramfs", "rootfs.img"} {
		if err := store.CloneFile(ctx, filepath.Join(dir, f), filepath.Join(dst, f)); err != nil {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("import %s: %w", f, err)
		}
	}
	return nil
}

// ---- Volumes --------------------------------------------------------------

// CreateVolume makes a new sparse volume.
func (c *Core) CreateVolume(name string, sizeMB int64) (string, error) {
	if err := store.ValidName(name); err != nil {
		return "", err
	}
	if _, err := os.Stat(c.root.VolumePath(name)); err == nil {
		return "", fmt.Errorf("volume %q already exists", name)
	}
	return volume.Ensure(c.root.VolumesDir(), name, sizeMB)
}

// RemoveVolume deletes a volume that is not attached to a running VM.
func (c *Core) RemoveVolume(name string) error {
	if err := store.ValidName(name); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for vmName, inst := range c.running {
		for _, m := range inst.cfg.Volumes {
			if m.Volume == name {
				return fmt.Errorf("volume %q is attached to running vm %q", name, vmName)
			}
		}
	}
	err := os.Remove(c.root.VolumePath(name))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("volume %q: %w", name, store.ErrNotFound)
	}
	return err
}

// ---- VMs ------------------------------------------------------------------

// VMStatus is what the API reports about a VM.
type VMStatus struct {
	store.VMConfig
	State   string    `json:"state"`
	Started time.Time `json:"started,omitempty"`
}

// CreateVM validates and persists a definition and clones its root disk.
func (c *Core) CreateVM(ctx context.Context, cfg store.VMConfig) error {
	if err := store.ValidName(cfg.Name); err != nil {
		return err
	}
	if _, err := c.root.LoadVM(cfg.Name); err == nil {
		return fmt.Errorf("vm %q already exists", cfg.Name)
	}
	if cfg.Image == "" {
		cfg.Image = "base"
	}
	if cfg.CPUs == 0 {
		cfg.CPUs = 2
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = 2048
	}
	if cfg.Cmdline == "" {
		cfg.Cmdline = store.DefaultCmdline
	}
	imgRoot := filepath.Join(c.root.ImageDir(cfg.Image), "rootfs.img")
	if _, err := os.Stat(imgRoot); err != nil {
		return fmt.Errorf("image %q: %w", cfg.Image, store.ErrNotFound)
	}
	for _, m := range cfg.Volumes {
		if err := store.ValidName(m.Volume); err != nil {
			return err
		}
		if _, err := os.Stat(c.root.VolumePath(m.Volume)); err != nil {
			return fmt.Errorf("volume %q: %w", m.Volume, store.ErrNotFound)
		}
		if m.Target == "" || !filepath.IsAbs(m.Target) {
			return fmt.Errorf("volume %q: target must be an absolute guest path", m.Volume)
		}
	}
	for _, p := range cfg.Packs {
		if _, err := c.Packs().Load(p); err != nil {
			return err
		}
	}
	if err := c.root.SaveVM(cfg); err != nil {
		return err
	}
	if err := store.CloneFile(ctx, imgRoot, filepath.Join(c.root.VMDir(cfg.Name), "root.img")); err != nil {
		_ = os.RemoveAll(c.root.VMDir(cfg.Name))
		return fmt.Errorf("clone root disk: %w", err)
	}
	return nil
}

// RemoveVM deletes a stopped VM and its root disk. Volumes are kept.
func (c *Core) RemoveVM(name string) error {
	if err := store.ValidName(name); err != nil {
		return err
	}
	c.mu.Lock()
	_, up := c.running[name]
	c.mu.Unlock()
	if up {
		return fmt.Errorf("vm %q is running", name)
	}
	if _, err := c.root.LoadVM(name); err != nil {
		return err
	}
	return os.RemoveAll(c.root.VMDir(name))
}

// ListVMs returns status for every defined VM.
func (c *Core) ListVMs() ([]VMStatus, error) {
	names, err := c.root.ListVMs()
	if err != nil {
		return nil, err
	}
	out := make([]VMStatus, 0, len(names))
	for _, n := range names {
		s, err := c.GetVM(n)
		if err != nil {
			slog.Warn("core: skip vm", "name", n, "err", err)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// GetVM returns status for one VM.
func (c *Core) GetVM(name string) (VMStatus, error) {
	cfg, err := c.root.LoadVM(name)
	if err != nil {
		return VMStatus{}, err
	}
	s := VMStatus{VMConfig: cfg, State: "stopped"}
	c.mu.Lock()
	if inst, ok := c.running[name]; ok {
		s.State = stateString(inst.machine.State())
		s.Started = inst.started
	}
	c.mu.Unlock()
	return s, nil
}

// StartVM boots a defined VM, waits for the guest agent, and mounts its
// volumes.
func (c *Core) StartVM(ctx context.Context, name string) error {
	cfg, err := c.root.LoadVM(name)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if _, ok := c.running[name]; ok {
		c.mu.Unlock()
		return fmt.Errorf("vm %q already running", name)
	}
	// Reserve the slot so concurrent starts fail fast.
	inst := &instance{cfg: cfg}
	c.running[name] = inst
	c.mu.Unlock()

	fail := func(err error) error {
		c.mu.Lock()
		delete(c.running, name)
		c.mu.Unlock()
		if inst.console != nil {
			_ = inst.console.Close()
		}
		if inst.stdin != nil {
			_ = inst.stdin.Close()
		}
		return err
	}

	dir := c.root.VMDir(name)
	console, err := os.OpenFile(filepath.Join(dir, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- name validated
	if err != nil {
		return fail(err)
	}
	inst.console = console
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return fail(err)
	}
	inst.stdin = stdin

	vols := make([]string, 0, len(cfg.Volumes))
	for _, m := range cfg.Volumes {
		vols = append(vols, c.root.VolumePath(m.Volume))
	}
	img := c.root.ImageDir(cfg.Image)
	m, err := vm.New(vm.Config{
		Kernel:    filepath.Join(img, "vmlinux"),
		Initrd:    filepath.Join(img, "initramfs"),
		RootDisk:  filepath.Join(dir, "root.img"),
		Volumes:   vols,
		Cmdline:   cfg.Cmdline,
		CPUs:      cfg.CPUs,
		MemoryMB:  cfg.MemoryMB,
		Console:   console,
		ConsoleIn: stdin,
	})
	if err != nil {
		return fail(err)
	}
	inst.machine = m
	if err := m.Start(); err != nil {
		return fail(fmt.Errorf("start: %w", err))
	}
	inst.started = time.Now()
	go c.reap(name, inst)

	dialCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	conn, err := m.DialGuest(dialCtx, vsockproto.Port)
	if err != nil {
		_ = m.Stop(context.Background())
		return fail(err)
	}
	inst.agent = conn
	inst.agentRd = bufio.NewReader(conn)

	for i, mnt := range cfg.Volumes {
		dev := fmt.Sprintf("/dev/vd%c", 'b'+i)
		if _, err := inst.call(vsockproto.Request{Op: "mount", Device: dev, Target: mnt.Target}); err != nil {
			_ = m.Stop(context.Background())
			return fail(fmt.Errorf("mount volume %q: %w", mnt.Volume, err))
		}
	}
	if len(cfg.Packs) > 0 {
		if err := c.DeliverPacks(ctx, name, cfg.Packs); err != nil {
			_ = m.Stop(context.Background())
			return fail(err)
		}
	}
	slog.Info("core: vm started", "name", name)
	return nil
}

// reap removes the instance when the machine stops for any reason.
func (c *Core) reap(name string, inst *instance) {
	<-inst.machine.Stopped()
	c.mu.Lock()
	if c.running[name] == inst {
		delete(c.running, name)
	}
	c.mu.Unlock()
	inst.agentMu.Lock()
	if inst.agent != nil {
		_ = inst.agent.Close()
	}
	inst.agentMu.Unlock()
	_ = inst.console.Close()
	_ = inst.stdin.Close()
	slog.Info("core: vm stopped", "name", name)
}

// StopVM shuts a VM down, asking the guest first and forcing after timeout.
func (c *Core) StopVM(ctx context.Context, name string) error {
	c.mu.Lock()
	inst, ok := c.running[name]
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("vm %q is not running", name)
	}
	// Ask the guest agent for a clean poweroff; fall back to Vz stop.
	_, _ = inst.call(vsockproto.Request{Op: "exec", Argv: []string{"poweroff"}})
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return inst.machine.Stop(stopCtx)
}

// Exec runs argv inside a running VM via the guest agent.
func (c *Core) Exec(name string, argv []string) (string, error) {
	c.mu.Lock()
	inst, ok := c.running[name]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("vm %q is not running", name)
	}
	resp, err := inst.call(vsockproto.Request{Op: "exec", Argv: argv})
	if err != nil {
		return resp.Output, err
	}
	return resp.Output, nil
}

// StopAll stops every running VM; used on shutdown.
func (c *Core) StopAll(ctx context.Context) {
	c.mu.Lock()
	names := make([]string, 0, len(c.running))
	for n := range c.running {
		names = append(names, n)
	}
	c.mu.Unlock()
	for _, n := range names {
		if err := c.StopVM(ctx, n); err != nil {
			slog.Warn("core: stop", "name", n, "err", err)
		}
	}
}

func (i *instance) call(req vsockproto.Request) (vsockproto.Response, error) {
	i.agentMu.Lock()
	defer i.agentMu.Unlock()
	var resp vsockproto.Response
	if i.agent == nil {
		return resp, errors.New("guest agent not connected")
	}
	if err := json.NewEncoder(i.agent).Encode(req); err != nil {
		return resp, err
	}
	line, err := i.agentRd.ReadBytes('\n')
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return resp, err
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("guest: %s", resp.Error)
	}
	return resp, nil
}

func stateString(s vz.VirtualMachineState) string {
	switch s {
	case vz.VirtualMachineStateRunning:
		return "running"
	case vz.VirtualMachineStateStarting:
		return "starting"
	case vz.VirtualMachineStateStopping:
		return "stopping"
	case vz.VirtualMachineStateStopped:
		return "stopped"
	case vz.VirtualMachineStatePaused:
		return "paused"
	case vz.VirtualMachineStateError:
		return "error"
	default:
		return fmt.Sprintf("state(%d)", s)
	}
}
