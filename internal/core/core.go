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
	machine *vm.Machine // nil while StartVM is still building it
	console *console
	started time.Time
	ready   bool               // StartVM finished: agent up, volumes mounted, packs delivered
	cancel  context.CancelFunc // aborts a start in progress (StopVM on a starting VM)

	termMu   sync.Mutex
	termRows uint16 // size the attached terminal last reported (Resize); 0 = none
	termCols uint16

	proxyMu sync.Mutex
	proxies []*credProxy
	egress  *egressProxy // restricted VMs only
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
	return os.RemoveAll(dir)
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
	if _, err := parseEgressRules(cfg.Allow); err != nil {
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
	for _, m := range cfg.Volumes {
		if err := store.ValidName(m.Volume); err != nil {
			return err
		}
		if _, err := os.Stat(c.root.VolumePath(m.Volume)); err != nil {
			return fmt.Errorf("volume %q: %w", m.Volume, store.ErrNotFound)
		}
		if holder := c.suspendedHolder(m.Volume); holder != "" {
			return fmt.Errorf("volume %q belongs to suspended vm %q; resuming it later would corrupt the volume if another VM writes to it first", m.Volume, holder)
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
	inst, up := c.running[name]
	c.mu.Unlock()
	if up {
		if inst.machine == nil || !inst.ready {
			return fmt.Errorf("vm %q is starting", name)
		}
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
		c.mu.Unlock()
		if inst.console != nil {
			inst.console.close()
		}
		return err
	}

	dir := c.root.VMDir(name)
	con, err := newConsole(filepath.Join(dir, "console.log"))
	if err != nil {
		return fail(err)
	}
	inst.console = con
	tl.mark("console")

	vols := make([]string, 0, len(cfg.Volumes))
	for _, m := range cfg.Volumes {
		vols = append(vols, c.root.VolumePath(m.Volume))
	}
	cmdline := strings.ReplaceAll(cfg.Cmdline, " resume=/dev/vdb", "") // configs from the hibernation era
	img := c.root.ImageDir(cfg.Image)
	m, err := vm.New(vm.Config{
		Kernel:    filepath.Join(img, "vmlinux"),
		Initrd:    filepath.Join(img, "initramfs"),
		RootDisk:  filepath.Join(dir, "root.img"),
		Volumes:   vols,
		Cmdline:   cmdline,
		MAC:       cfg.MAC,
		NoNetwork: cfg.Network == store.NetworkRestricted || cfg.Network == store.NetworkNone,
		MachineID: filepath.Join(dir, "machine-id.bin"),
		CPUs:      cfg.CPUs,
		MemoryMB:  cfg.MemoryMB,
		Console:   con.slave,
		ConsoleIn: con.slave,
	})
	if err != nil {
		return fail(err)
	}
	inst.machine = m
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
	inst.started = time.Now()
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
		if _, err := c.startEgress(inst); err != nil {
			_ = m.Stop(context.Background())
			return err
		}
		if len(cfg.Packs) > 0 {
			if _, err := c.startProxies(ctx, inst, cfg.Packs); err != nil {
				_ = m.Stop(context.Background())
				tl.done(err.Error())
				return err
			}
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
	if err := c.deliverEgress(inst); err != nil {
		_ = m.Stop(context.Background())
		tl.done(err.Error())
		return err
	}
	tl.mark("egress")
	if len(cfg.Packs) > 0 {
		if err := c.deliverProxies(ctx, inst, cfg.Packs); err != nil {
			_ = m.Stop(context.Background())
			tl.done(err.Error())
			return err
		}
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

func (c *Core) markReady(inst *instance) {
	c.mu.Lock()
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
	inst.proxyMu.Lock()
	for _, p := range inst.proxies {
		p.close()
	}
	inst.proxies = nil
	if inst.egress != nil {
		inst.egress.close()
		inst.egress = nil
	}
	inst.proxyMu.Unlock()
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
