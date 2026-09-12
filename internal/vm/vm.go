// Package vm drives Apple's Virtualization.framework through Code-Hex/vz.
//
// Only the pieces Onyx needs are exposed: boot a Linux kernel with a raw
// root disk, attach extra raw volumes as virtio-blk devices, NAT networking,
// a vsock channel to the guest agent, and a serial console.
package vm

import (
	"context"
	"fmt"
	"net"
	"os"
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

	// Serial console.
	if cfg.Console != nil {
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
	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("nat: %w", err)
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, fmt.Errorf("virtio-net: %w", err)
	}
	mac, err := vz.NewRandomLocallyAdministeredMACAddress()
	if err != nil {
		return nil, fmt.Errorf("mac: %w", err)
	}
	nic.SetMACAddress(mac)
	vmc.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})

	// Entropy, memory balloon, vsock.
	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("entropy: %w", err)
	}
	vmc.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("balloon: %w", err)
	}
	vmc.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloon})

	vsock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("vsock: %w", err)
	}
	vmc.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsock})

	if ok, err := vmc.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("invalid vm config: %w", err)
	}

	machine, err := vz.NewVirtualMachine(vmc)
	if err != nil {
		return nil, fmt.Errorf("new vm: %w", err)
	}
	return &Machine{vm: machine, cfg: cfg}, nil
}

// Start boots the VM.
func (m *Machine) Start() error {
	return m.vm.Start()
}

// State returns the current Vz state.
func (m *Machine) State() vz.VirtualMachineState {
	return m.vm.State()
}

// StateChanged returns a channel that receives state transitions.
func (m *Machine) StateChanged() <-chan vz.VirtualMachineState {
	return m.vm.StateChangedNotify()
}

// Stop requests a graceful ACPI-style shutdown if the guest supports it,
// otherwise force-stops.
func (m *Machine) Stop(ctx context.Context) error {
	if m.vm.CanRequestStop() {
		if _, err := m.vm.RequestStop(); err == nil {
			select {
			case <-ctx.Done():
			case <-waitState(m.vm, vz.VirtualMachineStateStopped):
				return nil
			}
		}
	}
	return m.vm.Stop()
}

func waitState(vm *vz.VirtualMachine, want vz.VirtualMachineState) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for s := range vm.StateChangedNotify() {
			if s == want {
				close(done)
				return
			}
		}
	}()
	return done
}

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
