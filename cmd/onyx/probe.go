package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/vm"
)

// runProbeRestore boots the base image, saves its state to a file, rebuilds
// the machine and restores. Passes with a persisted machine identifier;
// ONYX_NO_MACHINE_ID=1 reproduces the EINVAL you get without one. Device
// toggles: ONYX_NO_{CONSOLE,NET,VSOCK,BALLOON,ENTROPY}.
func runProbeRestore(args []string) error {
	imgDir := "images/out"
	if len(args) > 0 {
		imgDir = args[0]
	}
	root := filepath.Join(os.TempDir(), "onyx-probe")
	_ = os.MkdirAll(root, 0o750)
	rootDisk := filepath.Join(root, "root.img")
	_ = os.Remove(rootDisk)
	if out, err := execCP(filepath.Join(imgDir, "rootfs.img"), rootDisk); err != nil {
		return fmt.Errorf("clone root: %w: %s", err, out)
	}
	logf, err := os.Create(filepath.Join(root, "console.log")) // #nosec G304 -- temp dir
	if err != nil {
		return err
	}
	defer logf.Close()
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	cfg := vm.Config{
		Kernel: filepath.Join(imgDir, "vmlinux"), Initrd: filepath.Join(imgDir, "initramfs"),
		RootDisk: rootDisk, Cmdline: "console=hvc0 root=/dev/vda rootfstype=ext4 rw modules=ext4,virtio_blk,virtio_pci quiet",
		CPUs: 2, MemoryMB: 1024, MAC: "52:54:00:12:34:56", Console: logf, ConsoleIn: devnull,
		MachineID: filepath.Join(root, "machine-id.bin"),
	}
	if os.Getenv("ONYX_NO_MACHINE_ID") != "" {
		cfg.MachineID = ""
	}
	_ = os.Remove(filepath.Join(root, "machine-id.bin"))
	m, err := vm.New(cfg)
	if err != nil {
		return err
	}
	fmt.Println("savable:", m.Savable())
	if err := m.Start(); err != nil {
		return err
	}
	time.Sleep(8 * time.Second)
	if err := m.Pause(); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	state := filepath.Join(root, "state.vzs")
	_ = os.Remove(state)
	if err := m.SaveState(state); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	st, _ := os.Stat(state)
	fmt.Println("saved bytes:", st.Size())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	fmt.Println("stopped; rebuilding")
	m2, err := vm.New(cfg)
	if err != nil {
		return err
	}
	if err := m2.RestoreState(state); err != nil {
		return fmt.Errorf("RESTORE FAILED: %w", err)
	}
	if err := m2.Resume(); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	fmt.Println("RESTORE OK; state:", m2.State())
	time.Sleep(2 * time.Second)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	return m2.Stop(ctx2)
}

func execCP(src, dst string) ([]byte, error) {
	return execCommand("cp", "-c", src, dst)
}
