package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/vm"
	"github.com/edwinavalos/onyx/internal/volume"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// runSpike boots the base image with one data volume, talks to the guest
// agent over vsock, and (unless -interactive) shuts the VM down again.
func runSpike(args []string) error {
	fs := flag.NewFlagSet("spike", flag.ContinueOnError)
	imgDir := fs.String("images", "images/out", "directory with vmlinux, initramfs, rootfs.img")
	volDir := fs.String("volumes", "", "volume directory (default: ~/Library/Application Support/Onyx/volumes)")
	volName := fs.String("volume", "spike-data", "data volume name")
	interactive := fs.Bool("interactive", false, "attach the serial console to this terminal and stay up")
	consoleLog := fs.String("console-log", "", "write serial console to this file (non-interactive)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *volDir == "" {
		d, err := volume.DefaultDir()
		if err != nil {
			return err
		}
		*volDir = d
	}
	volPath, err := volume.Ensure(*volDir, *volName, 1024)
	if err != nil {
		return err
	}
	fmt.Println("volume:", volPath)

	cfg := vm.Config{
		Kernel:   filepath.Join(*imgDir, "vmlinux"),
		Initrd:   filepath.Join(*imgDir, "initramfs"),
		RootDisk: filepath.Join(*imgDir, "rootfs.img"),
		Volumes:  []string{volPath},
		Cmdline:  "console=hvc0 root=/dev/vda rootfstype=ext4 rw modules=ext4,virtio_blk,virtio_pci quiet",
		CPUs:     2,
		MemoryMB: 2048,
	}
	if *interactive {
		cfg.Console = os.Stdout
		cfg.ConsoleIn = os.Stdin
	} else if *consoleLog != "" {
		f, err := os.Create(*consoleLog) // #nosec G304 -- user-supplied log path
		if err != nil {
			return err
		}
		defer f.Close()
		cfg.Console = f
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			return err
		}
		defer devnull.Close()
		cfg.ConsoleIn = devnull
	}

	m, err := vm.New(cfg)
	if err != nil {
		return err
	}
	if err := m.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Println("vm started; waiting for guest agent...")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := m.DialGuest(ctx, vsockproto.Port)
	if err != nil {
		return err
	}
	defer conn.Close()

	call := func(req vsockproto.Request) (vsockproto.Response, error) {
		if err := json.NewEncoder(conn).Encode(req); err != nil {
			return vsockproto.Response{}, err
		}
		var resp vsockproto.Response
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return resp, err
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			return resp, err
		}
		if resp.Error != "" {
			return resp, fmt.Errorf("guest: %s: %s", resp.Error, resp.Output)
		}
		return resp, nil
	}

	r, err := call(vsockproto.Request{Op: "ping"})
	if err != nil {
		return err
	}
	fmt.Println("ping:", r.OK)

	r, err = call(vsockproto.Request{Op: "mount", Device: "/dev/vdb", Target: "/mnt/data"})
	if err != nil {
		return err
	}
	fmt.Println("mount:", r.OK)

	r, err = call(vsockproto.Request{Op: "exec", Argv: []string{"sh", "-c", "df -h /mnt/data && uname -a && ip -4 addr show eth0 | grep inet"}})
	if err != nil {
		return err
	}
	fmt.Print(r.Output)

	if *interactive {
		fmt.Println("interactive: press ctrl-c to stop")
		<-m.Stopped()
		return nil
	}

	fmt.Println("stopping vm")
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStop()
	return m.Stop(stopCtx)
}
