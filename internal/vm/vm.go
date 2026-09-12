// Package vm drives Apple's Virtualization.framework through Code-Hex/vz.
//
// Only the pieces Onyx needs are exposed: boot a Linux kernel with a raw
// root disk, attach extra raw volumes as virtio-blk devices, NAT networking,
// a vsock channel to the guest agent, and a serial console.
package vm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Code-Hex/vz/v3"
)

// Config describes one VM to launch.
type Config struct {
	Kernel     string   // path to uncompressed arm64 Linux Image
	Initrd     string   // path to initramfs
	Cmdline    string   // kernel command line
	RootDisk   string   // raw root disk image
	Volumes    []string // additional raw disk images, attached in order as /dev/vdb, /dev/vdc, ...
	MAC        string   // NIC hardware address; random if empty
	CPUs       uint
	MemoryMB   uint64
	Console    *os.File // serial console; nil disables
	ConsoleIn  *os.File
	SharedHome string // unused for now (D8: no host mounts)
}

// Machine is a running or ready-to-run VM.
type Machine struct {
	vm  *vz.VirtualMachine
	cfg Config

	mu      sync.Mutex
	state   vz.VirtualMachineState
	subs    map[chan vz.VirtualMachineState]struct{}
	stopped chan struct{} // closed once the machine reaches Stopped or Error
	savable bool
}

// New validates cfg and builds the underlying VM. It does not start it.
func New(cfg Config) (*Machine, error) {
	if cfg.CPUs == 0 {
		cfg.CPUs = 2
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = 2048
	}

	boot, err := vz.NewLinuxBootLoader(cfg.Kernel,
		vz.WithInitrd(cfg.Initrd),
		vz.WithCommandLine(cfg.Cmdline),
	)
	if err != nil {
		return nil, fmt.Errorf("bootloader: %w", err)
	}

	vmc, err := vz.NewVirtualMachineConfiguration(boot, cfg.CPUs, cfg.MemoryMB*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("vm config: %w", err)
	}

	// Serial console. (ONYX_NO_* switches exist only for `onyx probe-restore`.)
	if cfg.Console != nil && os.Getenv("ONYX_NO_CONSOLE") == "" {
		in := cfg.ConsoleIn
		if in == nil {
			in = cfg.Console
		}
		attach, err := vz.NewFileHandleSerialPortAttachment(in, cfg.Console)
		if err != nil {
			return nil, fmt.Errorf("serial attachment: %w", err)
		}
		serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attach)
		if err != nil {
			return nil, fmt.Errorf("serial config: %w", err)
		}
		vmc.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})
	}

	// Block devices: root first, then volumes.
	var disks []vz.StorageDeviceConfiguration
	for _, path := range append([]string{cfg.RootDisk}, cfg.Volumes...) {
		att, err := vz.NewDiskImageStorageDeviceAttachment(path, false)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", path, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return nil, fmt.Errorf("virtio-blk %s: %w", path, err)
		}
		disks = append(disks, blk)
	}
	vmc.SetStorageDevicesVirtualMachineConfiguration(disks)

	// NAT networking.
	if os.Getenv("ONYX_NO_NET") == "" {
		nic, err := natNIC(cfg.MAC)
		if err != nil {
			return nil, err
		}
		vmc.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})
	}

	// Entropy, memory balloon, vsock.
	if os.Getenv("ONYX_NO_ENTROPY") == "" {
		entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
		if err != nil {
			return nil, fmt.Errorf("entropy: %w", err)
		}
		vmc.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})
	}

	if os.Getenv("ONYX_NO_BALLOON") == "" {
		balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
		if err != nil {
			return nil, fmt.Errorf("balloon: %w", err)
		}
		vmc.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloon})
	}

	if os.Getenv("ONYX_NO_VSOCK") == "" {
		vsock, err := vz.NewVirtioSocketDeviceConfiguration()
		if err != nil {
			return nil, fmt.Errorf("vsock: %w", err)
		}
		vmc.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsock})
	}

	if ok, err := vmc.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("invalid vm config: %w", err)
	}
	savable, err := vmc.ValidateSaveRestoreSupport()
	if err != nil {
		slog.Debug("vm: save/restore unsupported", "err", err)
	}

	machine, err := vz.NewVirtualMachine(vmc)
	if err != nil {
		return nil, fmt.Errorf("new vm: %w", err)
	}
	m := &Machine{
		vm:      machine,
		cfg:     cfg,
		state:   machine.State(),
		subs:    map[chan vz.VirtualMachineState]struct{}{},
		stopped: make(chan struct{}),
		savable: savable,
	}
	go m.pump()
	return m, nil
}

// Savable reports whether Virtualization can save/restore this machine.
func (m *Machine) Savable() bool { return m.savable }

// Pause suspends execution; Resume continues it.
func (m *Machine) Pause() error  { return m.vm.Pause() }
func (m *Machine) Resume() error { return m.vm.Resume() }

// SaveState writes the paused machine's full state to path.
func (m *Machine) SaveState(path string) error {
	if !m.savable {
		return fmt.Errorf("this machine configuration cannot be saved")
	}
	return m.vm.SaveMachineStateToPath(path)
}

// RestoreState loads state saved by SaveState into a freshly built machine.
// The machine is left paused; call Resume to continue.
func (m *Machine) RestoreState(path string) error {
	return m.vm.RestoreMachineStateFromURL(path)
}

// pump is the single consumer of vz's state channel (it is one shared
// channel, so only one goroutine may read it) and fans out to subscribers.
func (m *Machine) pump() {
	for s := range m.vm.StateChangedNotify() {
		slog.Debug("vm: state", "state", s)
		m.mu.Lock()
		m.state = s
		for ch := range m.subs {
			select {
			case ch <- s:
			default: // slow subscriber; it can poll State()
			}
		}
		terminal := s == vz.VirtualMachineStateStopped || s == vz.VirtualMachineStateError
		if terminal {
			select {
			case <-m.stopped:
			default:
				close(m.stopped)
			}
		}
		m.mu.Unlock()
		if terminal {
			return
		}
	}
}

// Start boots the VM.
func (m *Machine) Start() error {
	return m.vm.Start()
}

// State returns the current Vz state.
func (m *Machine) State() vz.VirtualMachineState {
	return m.vm.State()
}

// Subscribe returns a channel of state transitions and a function to stop
// receiving. Events are dropped for subscribers that do not keep up.
func (m *Machine) Subscribe() (<-chan vz.VirtualMachineState, func()) {
	ch := make(chan vz.VirtualMachineState, 8)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

// Stopped is closed once the machine has reached Stopped or Error.
func (m *Machine) Stopped() <-chan struct{} { return m.stopped }

// Stop requests a graceful shutdown if the guest supports it, waits for ctx,
// then force-stops. A machine that is already stopped is not an error.
func (m *Machine) Stop(ctx context.Context) error {
	if m.isDone() {
		return nil
	}
	if m.vm.CanRequestStop() {
		if _, err := m.vm.RequestStop(); err == nil {
			select {
			case <-ctx.Done():
			case <-m.stopped:
				return nil
			}
		}
	}
	if m.isDone() {
		return nil
	}
	if err := m.vm.Stop(); err != nil {
		if m.isDone() {
			return nil
		}
		return err
	}
	return nil
}

func (m *Machine) isDone() bool {
	select {
	case <-m.stopped:
		return true
	default:
		s := m.vm.State()
		return s == vz.VirtualMachineStateStopped || s == vz.VirtualMachineStateError
	}
}

func natNIC(macStr string) (*vz.VirtioNetworkDeviceConfiguration, error) {
	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("nat: %w", err)
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("virtio-net: %w", err)
	}
	var mac *vz.MACAddress
	if macStr != "" {
		hw, err := net.ParseMAC(macStr)
		if err != nil {
			return nil, fmt.Errorf("mac %q: %w", macStr, err)
		}
		if mac, err = vz.NewMACAddress(hw); err != nil {
			return nil, fmt.Errorf("mac: %w", err)
		}
	} else if mac, err = vz.NewRandomLocallyAdministeredMACAddress(); err != nil {
		return nil, fmt.Errorf("mac: %w", err)
	}
	nic.SetMACAddress(mac)
	return nic, nil
}

// ListenHost opens a host-side vsock listener the guest can reach at CID 2.
//
// The returned listener is safe to Close after the machine has stopped:
// Virtualization's removeSocketListenerForPort never returns once the VM
// is gone, so Close skips it in that case and only unblocks Accept.
func (m *Machine) ListenHost(port uint32) (net.Listener, error) {
	devs := m.vm.SocketDevices()
	if len(devs) == 0 {
		return nil, fmt.Errorf("vm has no vsock device")
	}
	l, err := devs[0].Listen(port)
	if err != nil {
		return nil, err
	}
	return &hostListener{inner: l, stopped: m.stopped, done: make(chan struct{})}, nil
}

type hostListener struct {
	inner   net.Listener
	stopped <-chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (h *hostListener) Accept() (net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := h.inner.Accept()
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-h.done:
		return nil, net.ErrClosed
	case <-h.stopped:
		return nil, net.ErrClosed
	}
}

func (h *hostListener) Close() error {
	h.once.Do(func() {
		close(h.done)
		select {
		case <-h.stopped:
			// VM is gone; the listener dies with it.
		default:
			_ = h.inner.Close()
		}
	})
	return nil
}

func (h *hostListener) Addr() net.Addr { return h.inner.Addr() }

// DialGuest connects to port on the guest over vsock, retrying until the
// guest agent answers or ctx expires.
func (m *Machine) DialGuest(ctx context.Context, port uint32) (net.Conn, error) {
	devs := m.vm.SocketDevices()
	if len(devs) == 0 {
		return nil, fmt.Errorf("vm has no vsock device")
	}
	dev := devs[0]
	var lastErr error
	for {
		conn, err := dev.Connect(port)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial guest vsock port %d: %w (last: %w)", port, ctx.Err(), lastErr)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
