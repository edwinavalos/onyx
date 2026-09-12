package guest

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// enableSwap makes device a swap area if it is not one yet and enables it.
// The host passes the VM's dedicated swap disk; it doubles as the
// hibernation image target (kernel cmdline resume=<device>).
func enableSwap(ctx context.Context, device string) error {
	if device == "" {
		return fmt.Errorf("swap: device required")
	}
	if swapActive(device) {
		return nil
	}
	out, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "TYPE", device).Output() // #nosec G204
	if err != nil || strings.TrimSpace(string(out)) != "swap" {
		slog.Info("guest: formatting swap", "device", device)
		if out, err := exec.CommandContext(ctx, "mkswap", device).CombinedOutput(); err != nil { // #nosec G204
			return fmt.Errorf("mkswap %s: %w: %s", device, err, out)
		}
	}
	if out, err := exec.CommandContext(ctx, "swapon", device).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("swapon %s: %w: %s", device, err, out)
	}
	return nil
}

func swapActive(device string) bool {
	f, err := os.Open("/proc/swaps")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), device+" ") || strings.HasPrefix(sc.Text(), device+"\t") {
			return true
		}
	}
	return false
}

// hibernate writes the system image to swap and powers off. Execution of
// this goroutine resumes here when the VM is restored from the image.
func hibernate() {
	time.Sleep(200 * time.Millisecond) // let the response reach the host
	_ = exec.CommandContext(context.Background(), "sync").Run()
	slog.Info("guest: hibernating")
	if err := os.WriteFile("/sys/power/state", []byte("disk"), 0o200); err != nil {
		slog.Error("guest: hibernate failed", "err", err)
		return
	}
	slog.Info("guest: resumed from hibernation")
}
