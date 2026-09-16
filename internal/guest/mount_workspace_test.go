package guest

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
)

func TestMountWorkspaceRequiresTagAndTarget(t *testing.T) {
	ctx := context.Background()
	if err := mountWorkspace(ctx, "", "/mnt/x"); err == nil {
		t.Fatal("empty tag: want error, got nil")
	}
	if err := mountWorkspace(ctx, "onyx-ws-default", ""); err == nil {
		t.Fatal("empty target: want error, got nil")
	}
}

// mountWorkspace must not attempt to mount (and so must not require a real
// virtiofs share) when the target is already mounted — this is the only
// path exercisable without a guest kernel and an actual virtiofs device.
func TestMountWorkspaceIdempotent(t *testing.T) {
	target := firstMountedTarget(t)
	if err := mountWorkspace(context.Background(), "onyx-ws-default", target); err != nil {
		t.Fatalf("mountWorkspace on an already-mounted target: %v", err)
	}
}

func firstMountedTarget(t *testing.T) string {
	t.Helper()
	f, err := os.Open("/proc/mounts")
	if err != nil {
		t.Skipf("no /proc/mounts: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[1] == "/" {
			return fields[1]
		}
	}
	t.Skip("no root mount found in /proc/mounts")
	return ""
}
