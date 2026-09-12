package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

// runDoctor checks the host for everything Onyx needs and prints one line
// per check. Exit status is non-zero if any hard requirement fails.
func runDoctor(ctx context.Context) error {
	failed := false
	check := func(ok bool, hard bool, name, detail string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			if !hard {
				mark = "warn"
			} else {
				failed = true
			}
		}
		fmt.Printf("%s  %-28s %s\n", mark, name, detail)
	}

	check(runtime.GOOS == "darwin" && runtime.GOARCH == "arm64", true, "platform",
		runtime.GOOS+"/"+runtime.GOARCH+" (need darwin/arm64)")

	ver := strings.TrimSpace(run(ctx, "sw_vers", "-productVersion"))
	check(ver != "" && ver[0] >= '1' && ver >= "13", true, "macOS", ver+" (need 13+)")

	exe, _ := os.Executable()
	ents := run(ctx, "codesign", "-d", "--entitlements", "-", "--xml", exe)
	check(strings.Contains(ents, "com.apple.security.virtualization"), true, "virtualization entitlement",
		"run `make sign` if missing")

	root, err := store.Default()
	if err != nil {
		return err
	}
	check(true, false, "state dir", root.Dir)

	_, dockerErr := exec.LookPath("docker")
	check(dockerErr == nil, false, "docker", "needed only to build guest images (make image)")

	imgs, _ := root.ListImages()
	check(len(imgs) > 0, false, "images", fmt.Sprintf("%d installed (import with `onyx image import base images/out`)", len(imgs)))
	for _, n := range imgs {
		for _, f := range []string{"vmlinux", "initramfs", "rootfs.img"} {
			if _, err := os.Stat(filepath.Join(root.ImageDir(n), f)); err != nil {
				check(false, false, "image "+n, "missing "+f)
			}
		}
	}

	check(len(root.Socket()) < 104, true, "socket path", fmt.Sprintf("%s (%d bytes, limit 104)", root.Socket(), len(root.Socket())))

	cl, err := connect()
	if err == nil {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = cl.Ping(pctx)
		cancel()
	}
	check(err == nil, false, "onyx serve", map[bool]string{true: "reachable", false: "not running (start with `onyx serve`)"}[err == nil])

	if failed {
		return fmt.Errorf("doctor: hard requirements missing")
	}
	return nil
}

func run(ctx context.Context, name string, args ...string) string {
	out, _ := exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- fixed argv
	return string(out)
}
