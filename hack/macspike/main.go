// Step-4/5 probe for onyx#24: boot a Tart-provisioned macOS VM directly through
// Code-Hex/vz (no tart runtime), headless, with NAT networking and a vsock
// device. Host side listens on vsock port 5000 and dials guest port 5001.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	stdnet "net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
)

type tartConfig struct {
	HardwareModel string `json:"hardwareModel"`
	ECID          string `json:"ecid"`
	CPUCount      uint   `json:"cpuCount"`
	MemorySize    uint64 `json:"memorySize"`
	MacAddress    string `json:"macAddress"`
	OS            string `json:"os"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: macspike <tart vm dir>")
	}
	dir := os.Args[1]
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	must(err)
	var cfg tartConfig
	must(json.Unmarshal(raw, &cfg))
	log.Printf("tart config: os=%s cpus=%d mem=%dMiB mac=%s", cfg.OS, cfg.CPUCount, cfg.MemorySize>>20, cfg.MacAddress)

	hwData, err := base64.StdEncoding.DecodeString(cfg.HardwareModel)
	must(err)
	hw, err := vz.NewMacHardwareModelWithData(hwData)
	must(err)
	log.Printf("hardware model supported=%v", hw.Supported())
	idData, err := base64.StdEncoding.DecodeString(cfg.ECID)
	must(err)
	mid, err := vz.NewMacMachineIdentifierWithData(idData)
	must(err)
	aux, err := vz.NewMacAuxiliaryStorage(filepath.Join(dir, "nvram.bin"))
	must(err)
	platform, err := vz.NewMacPlatformConfiguration(
		vz.WithMacHardwareModel(hw), vz.WithMacMachineIdentifier(mid), vz.WithMacAuxiliaryStorage(aux))
	must(err)

	boot, err := vz.NewMacOSBootLoader()
	must(err)
	cpus := cfg.CPUCount
	if cpus == 0 {
		cpus = 4
	}
	mem := cfg.MemorySize
	if mem == 0 {
		mem = 4 << 30
	}
	config, err := vz.NewVirtualMachineConfiguration(boot, cpus, mem)
	must(err)
	config.SetPlatformVirtualMachineConfiguration(platform)

	// Graphics is mandatory for a macOS guest even when nobody attaches a view.
	gfx, err := vz.NewMacGraphicsDeviceConfiguration()
	must(err)
	w, h, ppi := int64(1024), int64(768), int64(80)
	if d := os.Getenv("MACSPIKE_DISPLAY"); d != "" { // WxH@PPI
		_, _ = fmt.Sscanf(d, "%dx%d@%d", &w, &h, &ppi)
	}
	log.Printf("display %dx%d@%d", w, h, ppi)
	disp, err := vz.NewMacGraphicsDisplayConfiguration(w, h, ppi)
	must(err)
	gfx.SetDisplays(disp)
	config.SetGraphicsDevicesVirtualMachineConfiguration([]vz.GraphicsDeviceConfiguration{gfx})

	var kbs []vz.KeyboardConfiguration
	var pts []vz.PointingDeviceConfiguration
	hid := os.Getenv("MACSPIKE_HID") // "" = Tart-style usb+mac, "mac", "usb", "none"
	if hid == "" || hid == "usb" {
		ukb, err := vz.NewUSBKeyboardConfiguration()
		must(err)
		kbs = append(kbs, ukb)
		upt, err := vz.NewUSBScreenCoordinatePointingDeviceConfiguration()
		must(err)
		pts = append(pts, upt)
	}
	if hid == "" || hid == "mac" {
		kb, err := vz.NewMacKeyboardConfiguration()
		must(err)
		kbs = append(kbs, kb)
		tp, err := vz.NewMacTrackpadConfiguration()
		must(err)
		pts = append(pts, tp)
	}
	config.SetKeyboardsVirtualMachineConfiguration(kbs)
	config.SetPointingDevicesVirtualMachineConfiguration(pts)
	log.Printf("hid=%q keyboards=%d pointers=%d", hid, len(kbs), len(pts))

	if os.Getenv("MACSPIKE_NOAUDIO") == "" {
		// Tart attaches a null speaker even with audio off.
		snd, err := vz.NewVirtioSoundDeviceConfiguration()
		must(err)
		out, err := vz.NewVirtioSoundDeviceHostOutputStreamConfiguration()
		must(err)
		snd.SetStreams(out)
		config.SetAudioDevicesVirtualMachineConfiguration([]vz.AudioDeviceConfiguration{snd})
	}

	disk, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(filepath.Join(dir, "disk.img"), false,
		vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFull)
	must(err)
	blk, err := vz.NewVirtioBlockDeviceConfiguration(disk)
	must(err)
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blk})

	nat, err := vz.NewNATNetworkDeviceAttachment()
	must(err)
	net, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	must(err)
	if cfg.MacAddress != "" {
		hwaddr, err := stdnet.ParseMAC(cfg.MacAddress)
		must(err)
		mac, err := vz.NewMACAddress(hwaddr)
		must(err)
		net.SetMACAddress(mac)
	}
	config.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{net})

	if os.Getenv("MACSPIKE_NOSOCK") == "" {
		sock, err := vz.NewVirtioSocketDeviceConfiguration()
		must(err)
		config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{sock})
	}

	// The Tart guest agent (org.cirruslabs.tart-guest-daemon, root, KeepAlive)
	// SIGTERMs its parent — launchd — unless a virtio console port named
	// tart-version-<x> exists, so without one the guest reboot-loops every
	// few seconds. MACSPIKE_NOVERSIONPORT=1 reproduces that.
	if os.Getenv("MACSPIKE_NOVERSIONPORT") == "" {
		port, err := vz.NewVirtioConsolePortConfiguration(vz.WithVirtioConsolePortConfigurationName("tart-version-2.32.1"))
		must(err)
		con, err := vz.NewVirtioConsoleDeviceConfiguration()
		must(err)
		con.SetVirtioConsolePortConfiguration(0, port)
		config.SetConsoleDevicesVirtualMachineConfiguration([]vz.ConsoleDeviceConfiguration{con})
	}

	ent, err := vz.NewVirtioEntropyDeviceConfiguration()
	must(err)
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{ent})

	ok, err := config.Validate()
	if !ok || err != nil {
		log.Fatalf("validate: ok=%v err=%v", ok, err)
	}
	vm, err := vz.NewVirtualMachine(config)
	must(err)
	go func() {
		for st := range vm.StateChangedNotify() {
			log.Printf("VZ state: %v", st)
		}
	}()
	if os.Getenv("MACSPIKE_STARTOPTS") != "" {
		must(vm.Start(vz.WithStartUpFromMacOSRecovery(false)))
	} else {
		must(vm.Start())
	}
	log.Printf("started; state=%v (mac %s — find the IP via `arp -an` / dhcpd leases)", vm.State(), cfg.MacAddress)

	// Step 5a: host listens on vsock 5000; guest should be able to connect to CID 2 port 5000.
	if len(vm.SocketDevices()) == 0 {
		waitSig(vm)
		return
	}
	dev := vm.SocketDevices()[0]
	ln, err := dev.Listen(5000)
	must(err)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				log.Printf("listen 5000: accept: %v", err)
				return
			}
			log.Printf("VSOCK: guest connected to host port 5000 from %s", c.RemoteAddr())
			go func() {
				b, _ := bufio.NewReader(c).ReadString('\n')
				log.Printf("VSOCK: guest said %q", b)
				_, _ = c.Write([]byte("hello from host\n"))
				c.Close()
			}()
		}
	}()
	// Step 5b: host dials guest port 5001 every 5s (MACSPIKE_NODIAL=1 disables).
	go func() {
		if os.Getenv("MACSPIKE_NODIAL") != "" {
			return
		}
		for {
			time.Sleep(5 * time.Second)
			c, err := dev.Connect(5001)
			if err != nil {
				log.Printf("dial guest 5001: %v", err)
				continue
			}
			log.Printf("VSOCK: connected to guest port 5001")
			_, _ = c.Write([]byte("ping from host\n"))
			b := make([]byte, 256)
			n, _ := c.Read(b)
			log.Printf("VSOCK: guest replied %q", b[:n])
			c.Close()
			time.Sleep(25 * time.Second)
		}
	}()

	waitSig(vm)
}

func waitSig(vm *vz.VirtualMachine) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	if os.Getenv("MACSPIKE_NOMAINLOOP") == "" {
		// Service the main run loop on the main thread until a signal arrives.
		log.Printf("running main run loop")
		go func() { <-sig; sig <- syscall.SIGINT; stopMainLoop() }()
		runMainLoop()
	}
	<-sig
	log.Printf("stopping")
	if vm.CanRequestStop() {
		_, _ = vm.RequestStop()
	}
	select {
	case <-time.After(120 * time.Second):
		log.Printf("no clean stop in 120s; forcing")
		_ = vm.Stop()
	case <-sig:
		_ = vm.Stop()
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
