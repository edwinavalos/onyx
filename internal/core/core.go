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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/edwinavalos/onyx/internal/proxy"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vm"
	"github.com/edwinavalos/onyx/internal/volume"
	"github.com/edwinavalos/onyx/internal/vsockproto"
	"github.com/edwinavalos/onyx/internal/workspace"
)

// Core holds all state for one Onyx instance.
type Core struct {
	root store.Root

	mu      sync.Mutex
	running map[string]*instance

	proxy *proxy.Manager // the credential proxy process (D20)
}

type instance struct {
	cfg     store.VMConfig
	machine *vm.Machine // nil while StartVM is still building it
	console *console
	started time.Time
	ready   bool               // StartVM finished: agent up, volumes mounted, packs delivered
	cancel  context.CancelFunc // aborts a start in progress (StopVM on a starting VM)

	termMu   sync.Mutex
	termRows uint16 // size the attached terminal last reported (Resize); 0 = none
	termCols uint16

	proxyMu   sync.Mutex
	listeners []*hostListener // vsock ports spliced into the proxy process
}

// New creates a Core over root, initialising the directory layout.
func New(root store.Root) (*Core, error) {
	if err := root.Init(); err != nil {
		return nil, err
	}
	c := &Core{root: root, running: map[string]*instance{}}
	c.proxy = &proxy.Manager{Root: root.Dir, InProcess: os.Getenv("ONYX_PROXY_INPROC") != ""}
	if err := c.proxy.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("credential proxy: %w", err)
	}
	return c, nil
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

// RemoveImage deletes an installed image. Existing VMs keep their own root
// disk clones, so nothing running depends on it.
func (c *Core) RemoveImage(name string) error {
	if err := store.ValidName(name); err != nil {
		return err
	}
	dir := c.root.ImageDir(name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("image %q: %w", name, store.ErrNotFound)
	}
	if vmName, err := c.imageUsedByDefinition(name); err != nil {
		return err
	} else if vmName != "" {
		return fmt.Errorf("image %q is used by vm %q; remove the VM definition first", name, vmName)
	}
	return os.RemoveAll(dir)
}

// imageUsedByDefinition returns one VM definition that still needs image's
// kernel and initramfs. A VM owns a clone of rootfs.img, but boot artifacts
// remain in images/<image> until the definition is removed.
func (c *Core) imageUsedByDefinition(image string) (string, error) {
	names, err := c.root.ListVMs()
	if err != nil {
		return "", err
	}
	for _, name := range names {
		cfg, err := c.root.LoadVM(name)
		if err != nil {
			return "", err
		}
		if cfg.Image == image {
			return name, nil
		}
	}
	return "", nil
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

// VolumeInfo is one volume, its sizes, and the definitions that attach it.
type VolumeInfo struct {
	store.VolumeInfo
	VMs []string `json:"vms"` // definitions naming it; empty means nothing uses it
}

// ListVolumes lists volumes with the VMs whose definitions attach them, so
// a leftover session volume is visible as one no VM references.
func (c *Core) ListVolumes() ([]VolumeInfo, error) {
	infos, err := c.root.ListVolumeInfo()
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	vms, err := c.root.ListVMs()
	if err != nil {
		return nil, err
	}
	for _, vmName := range vms {
		cfg, err := c.root.LoadVM(vmName)
		if err != nil {
			continue
		}
		for _, m := range cfg.Volumes {
			users[m.Volume] = append(users[m.Volume], vmName)
		}
	}
	out := make([]VolumeInfo, 0, len(infos))
	for _, vi := range infos {
		info := VolumeInfo{VolumeInfo: vi, VMs: []string{}}
		if u := users[vi.Name]; u != nil {
			info.VMs = u
		}
		out = append(out, info)
	}
	return out, nil
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
	if holder := c.suspendedHolder(name); holder != "" {
		return fmt.Errorf("volume %q belongs to suspended vm %q", name, holder)
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
// With createVolumesMB > 0 the volumes the definition names that do not
// exist yet are created at that size and recorded in cfg.OwnedVolumes:
// they belong to the definition until it has run once, and RemoveVM
// deletes them with it (D18). Volumes that already exist are never owned.
func (c *Core) CreateVM(ctx context.Context, cfg store.VMConfig, createVolumesMB int64) error {
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
		cfg.CPUs = store.DefaultCPUs
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = store.DefaultMemoryMB
	}
	if cfg.Cmdline == "" {
		cfg.Cmdline = store.DefaultCmdline
	}
	if err := store.ValidNetwork(cfg.Network); err != nil {
		return err
	}
	if cfg.Network == "" {
		cfg.Network = store.NetworkNAT
	}
	if cfg.Network != store.NetworkRestricted && len(cfg.Allow) > 0 {
		return fmt.Errorf("allow list needs network %s", store.NetworkRestricted)
	}
	if _, err := proxy.ParseAllow(cfg.Allow); err != nil {
		return err
	}
	if cfg.MAC == "" {
		mac, err := vz.NewRandomLocallyAdministeredMACAddress()
		if err != nil {
			return fmt.Errorf("mac: %w", err)
		}
		cfg.MAC = mac.String()
	}
	imgRoot := filepath.Join(c.root.ImageDir(cfg.Image), "rootfs.img")
	if _, err := os.Stat(imgRoot); err != nil {
		return fmt.Errorf("image %q: %w", cfg.Image, store.ErrNotFound)
	}
	cfg.OwnedVolumes = nil // only the core decides ownership
	seen := map[string]bool{}
	for _, m := range cfg.Volumes {
		if err := store.ValidName(m.Volume); err != nil {
			return err
		}
		if _, err := os.Stat(c.root.VolumePath(m.Volume)); err != nil {
			if createVolumesMB <= 0 {
				return fmt.Errorf("volume %q: %w", m.Volume, store.ErrNotFound)
			}
			if !seen[m.Volume] {
				cfg.OwnedVolumes = append(cfg.OwnedVolumes, m.Volume)
			}
		}
		seen[m.Volume] = true
		if holder := c.suspendedHolder(m.Volume); holder != "" {
			return fmt.Errorf("volume %q belongs to suspended vm %q; resuming it later would corrupt the volume if another VM writes to it first", m.Volume, holder)
		}
		if m.Target == "" || !filepath.IsAbs(m.Target) {
			return fmt.Errorf("volume %q: target must be an absolute guest path", m.Volume)
		}
	}
	for _, m := range cfg.Workspaces {
		if err := store.ValidName(m.Workspace); err != nil {
			return err
		}
		if m.Target == "" || !filepath.IsAbs(m.Target) {
			return fmt.Errorf("workspace %q: target must be an absolute guest path", m.Workspace)
		}
	}
	for _, p := range cfg.Packs {
		if _, err := c.Packs().Load(p); err != nil {
			return err
		}
	}
	// Workspaces are never auto-deleted with a VM (unlike OwnedVolumes, D18),
	// so they can be created unconditionally: nothing to undo on failure.
	for _, m := range cfg.Workspaces {
		if _, err := workspace.Ensure(c.root.WorkspacesDir(), m.Workspace); err != nil {
			return fmt.Errorf("workspace %q: %w", m.Workspace, err)
		}
	}
	// The definition goes to disk before its volumes exist: a core that
	// dies in between leaves a VM naming a volume it owns but never made,
	// which `vm rm` cleans up, rather than a volume nothing accounts for.
	if err := c.root.SaveVM(cfg); err != nil {
		return err
	}
	undo := func(err error) error {
		c.removeOwnedVolumes(cfg)
		_ = os.RemoveAll(c.root.VMDir(cfg.Name))
		return err
	}
	for _, v := range cfg.OwnedVolumes {
		if _, err := volume.Ensure(c.root.VolumesDir(), v, createVolumesMB); err != nil {
			return undo(err)
		}
	}
	if err := store.CloneFile(ctx, imgRoot, filepath.Join(c.root.VMDir(cfg.Name), "root.img")); err != nil {
		return undo(fmt.Errorf("clone root disk: %w", err))
	}
	return nil
}

// workspaceShares maps a VM's workspace mounts to the vm.WorkspaceShare
// values vm.New needs: the host directory and the virtiofs tag the guest
// mounts by (see internal/workspace, decisions.md D21).
func workspaceShares(root store.Root, mounts []store.WorkspaceMount) []vm.WorkspaceShare {
	shares := make([]vm.WorkspaceShare, 0, len(mounts))
	for _, m := range mounts {
		shares = append(shares, vm.WorkspaceShare{
			Tag:  workspace.Tag(m.Workspace),
			Path: root.WorkspacePath(m.Workspace),
		})
	}
	return shares
}

// removeOwnedVolumes deletes the volumes cfg still owns. One that is gone
// already (a create that died halfway) is nothing to report; one another
// VM holds meanwhile is left alone and logged.
func (c *Core) removeOwnedVolumes(cfg store.VMConfig) {
	for _, v := range cfg.OwnedVolumes {
		used, err := c.volumeUsedByAnotherDefinition(v, cfg.Name)
		if err != nil {
			// This is cleanup after a failed session. Preserve the disk rather
			// than risk deleting one that a definition references when we cannot
			// establish its ownership safely.
			slog.Warn("core: keep owned volume; cannot inspect definitions", "vm", cfg.Name, "volume", v, "err", err)
			continue
		}
		if used {
			slog.Info("core: keep owned volume referenced by another vm", "vm", cfg.Name, "volume", v)
			continue
		}
		err = c.RemoveVolume(v)
		switch {
		case err == nil:
			slog.Info("core: removed volume owned by vm that never ran", "vm", cfg.Name, "volume", v)
		case errors.Is(err, store.ErrNotFound):
		default:
			slog.Warn("core: keep owned volume", "vm", cfg.Name, "volume", v, "err", err)
		}
	}
}

// volumeUsedByAnotherDefinition reports whether a persisted VM definition
// other than except names volume. A stopped VM still needs every volume in its
// definition for its next start, so it is a holder just as much as a running
// VM is.
func (c *Core) volumeUsedByAnotherDefinition(volume, except string) (bool, error) {
	names, err := c.root.ListVMs()
	if err != nil {
		return false, err
	}
	for _, name := range names {
		if name == except {
			continue
		}
		cfg, err := c.root.LoadVM(name)
		if err != nil {
			return false, err
		}
		for _, m := range cfg.Volumes {
			if m.Volume == volume {
				return true, nil
			}
		}
	}
	return false, nil
}

// RemoveVM deletes a stopped VM and its root disk. Volumes are kept, except
// those the definition created for itself and never ran with (D18).
func (c *Core) RemoveVM(name string) error {
	if err := store.ValidName(name); err != nil {
		return err
	}
	c.mu.Lock()
	inst, up := c.running[name]
	c.mu.Unlock()
	if up {
		if inst.machine == nil || !inst.ready {
			return fmt.Errorf("vm %q is starting", name)
		}
		return fmt.Errorf("vm %q is running", name)
	}
	cfg, err := c.root.LoadVM(name)
	if err != nil {
		return err
	}
	c.removeOwnedVolumes(cfg)
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
	if _, err := os.Stat(filepath.Join(c.root.VMDir(name), snapshotFile)); err == nil {
		s.State = "suspended"
	}
	c.mu.Lock()
	defer c.mu.Unlock() // never leave c.mu held if something below panics
	if inst, ok := c.running[name]; ok {
		s.Started = inst.started
		// StartVM reserves the slot before the machine exists.
		if inst.machine == nil || !inst.ready {
			s.State = "starting"
		} else {
			s.State = stateString(inst.machine.State())
		}
	}
	return s, nil
}

// StartVM boots a defined VM, waits for the guest agent, mounts its
// volumes, delivers packs, and finally hands the console its session: the
// one given, or a plain shell so the console never sits waiting.
func (c *Core) StartVM(ctx context.Context, name string, sess *vsockproto.Session) error {
	cfg, err := c.root.LoadVM(name)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if prev, ok := c.running[name]; ok {
		c.mu.Unlock()
		if prev.machine == nil || !prev.ready {
			return fmt.Errorf("vm %q is already starting", name)
		}
		return fmt.Errorf("vm %q already running", name)
	}
	// Virtualization opens each disk exclusively; a volume held by another
	// running (or starting) VM would fail late with an opaque VZError.
	for _, m := range cfg.Volumes {
		for other, inst := range c.running {
			for _, om := range inst.cfg.Volumes {
				if om.Volume == m.Volume {
					c.mu.Unlock()
					return fmt.Errorf("volume %q is attached to running vm %q", m.Volume, other)
				}
			}
		}
	}
	// Reserve the slot so concurrent starts fail fast. The start can be
	// cancelled from StopVM until it completes.
	ctx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	inst := &instance{cfg: cfg, started: time.Now(), cancel: cancelStart}
	c.running[name] = inst
	c.mu.Unlock()

	tl := c.newTimeline("start", name)
	fail := func(err error) error {
		tl.done(err.Error())
		c.mu.Lock()
		if c.running[name] == inst { // a cancelled start may already be gone
			delete(c.running, name)
		}
		con := inst.console
		c.mu.Unlock()
		if con != nil {
			con.close()
		}
		return err
	}

	dir := c.root.VMDir(name)
	con, err := newConsole(filepath.Join(dir, "console.log"))
	if err != nil {
		return fail(err)
	}
	c.mu.Lock()
	if c.running[name] != inst {
		c.mu.Unlock()
		return fail(fmt.Errorf("start cancelled"))
	}
	inst.console = con
	c.mu.Unlock()
	tl.mark("console")

	vols := make([]string, 0, len(cfg.Volumes))
	for _, m := range cfg.Volumes {
		vols = append(vols, c.root.VolumePath(m.Volume))
	}
	cmdline := strings.ReplaceAll(cfg.Cmdline, " resume=/dev/vdb", "") // configs from the hibernation era
	img := c.root.ImageDir(cfg.Image)
	m, err := vm.New(vm.Config{
		Kernel:     filepath.Join(img, "vmlinux"),
		Initrd:     filepath.Join(img, "initramfs"),
		RootDisk:   filepath.Join(dir, "root.img"),
		Volumes:    vols,
		Cmdline:    cmdline,
		MAC:        cfg.MAC,
		NoNetwork:  cfg.Network == store.NetworkRestricted || cfg.Network == store.NetworkNone,
		MachineID:  filepath.Join(dir, "machine-id.bin"),
		CPUs:       cfg.CPUs,
		MemoryMB:   cfg.MemoryMB,
		Console:    con.slave,
		ConsoleIn:  con.slave,
		Workspaces: workspaceShares(c.root, cfg.Workspaces),
	})
	if err != nil {
		return fail(err)
	}
	c.mu.Lock()
	if c.running[name] != inst {
		c.mu.Unlock()
		return fail(fmt.Errorf("start cancelled"))
	}
	inst.machine = m
	c.mu.Unlock()
	tl.mark("machine")
	restored := false
	if snap := c.pendingSnapshot(cfg); snap != "" {
		if err := m.RestoreState(snap); err != nil {
			return fail(fmt.Errorf("restore snapshot: %w", err))
		}
		if err := m.Resume(); err != nil {
			return fail(fmt.Errorf("resume snapshot: %w", err))
		}
		c.discardSnapshot(name) // single use: the disks move on from here
		restored = true
		slog.Info("core: vm restored from snapshot", "name", name)
	} else if err := m.Start(); err != nil {
		return fail(fmt.Errorf("start: %w", err))
	}
	c.mu.Lock()
	inst.started = time.Now()
	c.mu.Unlock()
	go c.reap(name, inst)
	tl.mark("vz_start")
	tl.set("restored", restored)

	// Wait for the guest agent to come up.
	dialCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	pong, err := inst.callCtx(dialCtx, vsockproto.Request{Op: "ping"}, agentTimeout)
	if err != nil {
		_ = m.Stop(context.Background())
		if ctx.Err() != nil {
			return fail(fmt.Errorf("start cancelled"))
		}
		return fail(err)
	}
	tl.mark("agent")
	// The agent reports its uptime: kernel + OpenRC time up to the agent.
	if ms := parseUptimeMS(pong.Output); ms > 0 && !restored {
		tl.set("guest_boot_ms", ms)
	}
	if restored {
		// Guest memory is exactly as it was: mounts, secrets and bridges are
		// in place. It does not know how long it was away, and host-side
		// listeners died with the old VM object.
		if _, err := inst.call(vsockproto.Request{Op: "clock", UnixNanos: time.Now().UnixNano()}); err != nil {
			slog.Warn("core: set guest clock", "name", name, "err", err)
		}
		if _, _, err := c.setupProxies(ctx, inst, cfg.Packs); err != nil {
			_ = m.Stop(context.Background())
			tl.done(err.Error())
			return err
		}
		tl.mark("host_listeners")
		c.markReady(inst)
		tl.done("")
		slog.Info("core: vm started", "name", name, "restored", true)
		return nil
	}

	for i, mnt := range cfg.Volumes {
		dev := fmt.Sprintf("/dev/vd%c", 'b'+i)
		if _, err := inst.call(vsockproto.Request{Op: "mount", Device: dev, Target: mnt.Target}); err != nil {
			_ = m.Stop(context.Background())
			return fail(fmt.Errorf("mount volume %q: %w", mnt.Volume, err))
		}
	}
	for _, mnt := range cfg.Workspaces {
		tag := workspace.Tag(mnt.Workspace)
		if _, err := inst.call(vsockproto.Request{Op: "mount_workspace", Tag: tag, Target: mnt.Target}); err != nil {
			_ = m.Stop(context.Background())
			return fail(fmt.Errorf("mount workspace %q: %w", mnt.Workspace, err))
		}
	}
	tl.mark("mounts")
	if len(cfg.Packs) > 0 {
		if err := c.deliverSecrets(ctx, inst, cfg.Packs); err != nil {
			_ = m.Stop(context.Background())
			return fail(err)
		}
	}
	tl.mark("packs")
	// reap runs from here on, so make the instance's own cleanup the
	// failure path rather than the local one.
	if err := c.deliverSSHKey(ctx, inst); err != nil {
		if !guestLacksOp(err) {
			_ = m.Stop(context.Background())
			tl.done(err.Error())
			return err
		}
		slog.Warn("core: guest image predates ssh support; onyx ssh unavailable for this VM", "name", name)
	}
	tl.mark("ssh_key")
	if err := c.deliverProxies(ctx, inst, cfg.Packs); err != nil {
		_ = m.Stop(context.Background())
		tl.done(err.Error())
		return err
	}
	tl.mark("proxies")
	if !restoredSessionPending(inst) {
		s := sessionForStart(sess, inst)
		if s.Rows > 0 && s.Cols > 0 {
			inst.console.setSize(s.Rows, s.Cols)
		}
		if _, err := inst.call(vsockproto.Request{Op: "session", Session: &s}); err != nil {
			_ = m.Stop(context.Background())
			tl.done(err.Error())
			return err
		}
	}
	tl.mark("session")
	c.markReady(inst)
	tl.done("")
	slog.Info("core: vm started", "name", name)
	return nil
}

// sessionForStart is the session a fresh start delivers: the one asked
// for (or a plain shell), sized to the terminal that attached during the
// start when there is one — its size is what the guest must draw for, not
// the nominal rows/cols the request carried.
func sessionForStart(s *vsockproto.Session, inst *instance) vsockproto.Session {
	out := vsockproto.Session{OnExit: "shell"}
	if s != nil {
		out = *s
	}
	if rows, cols, ok := inst.termSize(); ok {
		out.Rows, out.Cols = rows, cols
	}
	return out
}

func (inst *instance) setTermSize(rows, cols uint16) {
	inst.termMu.Lock()
	defer inst.termMu.Unlock()
	inst.termRows, inst.termCols = rows, cols
}

func (inst *instance) termSize() (rows, cols uint16, ok bool) {
	inst.termMu.Lock()
	defer inst.termMu.Unlock()
	return inst.termRows, inst.termCols, inst.termRows > 0 && inst.termCols > 0
}

// restoredSessionPending is a hook for restores, whose console already has
// its session (guest memory is intact). Fresh starts always deliver one.
func restoredSessionPending(*instance) bool { return false }

// cancelStart aborts a start in progress and reports whether there was
// one. With no machine yet the slot is released here; otherwise the
// machine is stopped and reap releases it.
func (c *Core) cancelStart(name string) bool {
	c.mu.Lock()
	inst, ok := c.running[name]
	if !ok || inst.ready {
		c.mu.Unlock()
		return false
	}
	if inst.cancel != nil {
		inst.cancel()
	}
	m := inst.machine
	if m == nil {
		delete(c.running, name)
	}
	c.mu.Unlock()
	if m != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = m.Stop(stopCtx)
	}
	slog.Info("core: start cancelled", "name", name)
	return true
}

// guestLacksOp reports a guest agent that does not know an op — an older
// image — as opposed to a failure of the op itself.
func guestLacksOp(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown op")
}

// markReady flips the instance to running. The VM has now used its
// volumes, so any it created for itself become ordinary, persistent ones.
func (c *Core) markReady(inst *instance) {
	c.mu.Lock()
	cfg := inst.cfg
	c.mu.Unlock()
	if len(cfg.OwnedVolumes) > 0 {
		cfg.OwnedVolumes = nil
		if err := c.root.SaveVM(cfg); err != nil {
			slog.Warn("core: release owned volumes", "name", cfg.Name, "err", err)
		}
	}
	c.mu.Lock()
	inst.cfg = cfg
	inst.ready = true
	c.mu.Unlock()
}

// reap removes the instance when the machine stops for any reason.
func (c *Core) reap(name string, inst *instance) {
	<-inst.machine.Stopped()
	c.mu.Lock()
	if c.running[name] == inst {
		delete(c.running, name)
	}
	c.mu.Unlock()
	c.closeProxies(inst)
	inst.console.close()
	if path, ok := archiveGuestCrash(c.root, name); ok {
		slog.Warn("core: guest kernel crashed; console log archived", "name", name, "path", path)
	}
	slog.Info("core: vm stopped", "name", name)
}

// StopVM shuts a VM down, asking the guest first and forcing after timeout.
func (c *Core) StopVM(ctx context.Context, name string) error {
	if c.cancelStart(name) {
		return nil
	}
	inst, err := c.instance(name)
	if err != nil {
		return err
	}
	// Ask the guest agent for a clean poweroff; fall back to Vz stop. The
	// guest may die before answering, so do not wait long.
	_, _ = inst.callTimeout(vsockproto.Request{Op: "exec", Argv: []string{"poweroff"}}, 3*time.Second)
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return inst.machine.Stop(stopCtx)
}

// Exec runs argv inside a running VM via the guest agent.
func (c *Core) Exec(name string, argv []string) (string, error) {
	inst, err := c.instance(name)
	if err != nil {
		return "", err
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

// agentTimeout bounds a normal guest call; exec of a long command can take
// a while, so it is generous. Calls that may not be answered (poweroff)
// pass their own.
const agentTimeout = 5 * time.Minute

func (i *instance) call(req vsockproto.Request) (vsockproto.Response, error) {
	return i.callTimeout(req, agentTimeout)
}

func (i *instance) callTimeout(req vsockproto.Request, d time.Duration) (vsockproto.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return i.callCtx(ctx, req, d)
}

// callCtx makes one request on a fresh vsock connection so a slow call
// (a long exec) never blocks lifecycle operations behind it. dialCtx bounds
// the connect; d bounds the exchange.
func (i *instance) callCtx(dialCtx context.Context, req vsockproto.Request, d time.Duration) (vsockproto.Response, error) {
	var resp vsockproto.Response
	conn, err := i.machine.DialGuest(dialCtx, vsockproto.Port)
	if err != nil {
		return resp, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(d))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return resp, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
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
