//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Screen: a session command's output is what the user sees after boot,
// with a prompt under it, and no OpenRC service chatter lands on the grid
// once the session has begun (8c0c94a: the default runlevel used to print
// over Claude Code).
func TestBootToSessionCommandOnScreen(t *testing.T) {
	h := need(t)
	name := h.vm(t, nil)
	const cols, rows = 80, 24
	sess := &vsockproto.Session{
		Dir:    "/home/dev",
		Cmd:    `echo "E2E-MARK-$((40+2))"; stty size`,
		Rows:   rows,
		Cols:   cols,
		OnExit: "shell",
	}
	h.start(t, name, sess)
	s := h.attach(t, name, cols, rows)

	lines := s.waitText(t, "E2E-MARK-42", 15*time.Second)
	mark := rowWith(lines, "E2E-MARK-42")
	// The session shell announces the command right above its output.
	if cmd := rowWith(lines, "onyx: echo"); cmd < 0 || cmd > mark {
		t.Errorf("session command line not announced before its output (row %d, mark row %d)", cmd, mark)
	}
	// The guest tty took the size the session asked for (dc5fa17).
	if rowWith(lines, fmt.Sprintf("%d %d", rows, cols)) < 0 {
		t.Errorf("stty size did not report %dx%d", rows, cols)
	}
	// After the command exits the shell prompt appears under it, and the
	// cursor sits on that row.
	lines = s.waitFor(t, "prompt after the command", 5*time.Second, func(l []string) bool {
		last := lastNonEmpty(l)
		return last > mark && strings.HasSuffix(l[last], "$")
	})
	last := lastNonEmpty(lines)
	if row, _ := s.cursor(); row != last {
		t.Errorf("cursor on row %d, prompt on row %d", row, last)
	}
	for i := mark; i < len(lines); i++ {
		if openrcLine.MatchString(lines[i]) {
			t.Errorf("OpenRC output on the grid after the session began: row %d %q", i, lines[i])
		}
	}
	// The prompt is live: typing runs a command whose output appears.
	s.send(t, "echo E2E-TYPED-$((1+1))\n")
	s.waitText(t, "E2E-TYPED-2", 5*time.Second)
}

// API: the lifecycle a VM definition goes through, and what each step
// leaves on disk. Volumes outlive the VM that mounted them.
func TestVMLifecycle(t *testing.T) {
	h := need(t)
	ctx := context.Background()
	vol := fmt.Sprintf("e2e-vol-%d", os.Getpid())
	if err := h.cl.CreateVolume(ctx, vol, 64); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.cl.RemoveVolume(context.Background(), vol) })

	name := h.vm(t, func(c *store.VMConfig) {
		c.Volumes = []store.VolumeMount{{Volume: vol, Target: "/mnt/e2e"}}
	})
	root := store.Root{Dir: h.dir}

	st, err := h.cl.GetVM(ctx, name)
	if err != nil || st.State != "stopped" {
		t.Fatalf("after create: state %q err %v", st.State, err)
	}
	if st.CPUs != store.DefaultCPUs || st.MemoryMB != store.DefaultMemoryMB {
		t.Errorf("size defaulted to %d/%d, want %d/%d", st.CPUs, st.MemoryMB, store.DefaultCPUs, store.DefaultMemoryMB)
	}
	if _, err := os.Stat(filepath.Join(root.VMDir(name), "root.img")); err != nil {
		t.Errorf("root disk not cloned on create: %v", err)
	}

	before := time.Now()
	st = h.start(t, name, nil)
	if st.Started.Before(before.Add(-time.Second)) {
		t.Errorf("started %v not set by start (now %v)", st.Started, before)
	}
	// Listed as running; the volume is mounted and writable in the guest.
	vms, err := h.cl.ListVMs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vms {
		if v.Name == name {
			found = v.State == "running"
		}
	}
	if !found {
		t.Errorf("%s not listed as running: %+v", name, vms)
	}
	if got := h.sh(t, name, "findmnt -no FSTYPE /mnt/e2e && echo kept > /mnt/e2e/marker && cat /mnt/e2e/marker"); got != "ext4\nkept" {
		t.Errorf("volume in guest: %q", got)
	}
	// Starting again is refused while running.
	if _, err := h.cl.StartVM(ctx, name, nil); err == nil {
		t.Error("second start of a running VM succeeded")
	}

	if _, err := h.cl.StopVM(ctx, name); err != nil {
		t.Fatal(err)
	}
	h.waitState(t, name, "stopped", stopTimeout)
	// Data survives a stop/start cycle: the volume was not reformatted.
	h.start(t, name, nil)
	if got := h.sh(t, name, "cat /mnt/e2e/marker"); got != "kept" {
		t.Errorf("volume data after restart: %q", got)
	}
	if _, err := h.cl.StopVM(ctx, name); err != nil {
		t.Fatal(err)
	}
	h.waitState(t, name, "stopped", stopTimeout)

	if err := h.cl.RemoveVM(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cl.GetVM(ctx, name); !isNotFound(err) {
		t.Errorf("get after remove: %v", err)
	}
	if _, err := os.Stat(root.VMDir(name)); !os.IsNotExist(err) {
		t.Errorf("vm dir still present after remove: %v", err)
	}
	vols, err := h.cl.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(vols, vol) {
		t.Errorf("volume %s removed with the VM: %v", vol, vols)
	}
}

// Guest state: an env-mode secret is in the guest's tmpfs env file and in
// the environment exec gives a command, the audit log names the key, and
// the value is on no block device.
func TestEnvModeSecret(t *testing.T) {
	h := need(t)
	ctx := context.Background()
	key := fmt.Sprintf("e2e-secret-%d", os.Getpid())
	value := fmt.Sprintf("s3cret-%d", time.Now().UnixNano())
	if err := h.cl.SetSecret(ctx, key, value); err != nil {
		t.Fatalf("set secret (Keychain): %v", err)
	}
	t.Cleanup(func() { _ = h.cl.RemoveSecret(context.Background(), key) })
	p := pack.Pack{Name: "e2e-env", Secrets: []pack.Secret{{Key: key, Mode: pack.ModeEnv, Name: "E2E_SECRET"}}}
	if err := h.cl.SavePack(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.cl.RemovePack(context.Background(), p.Name) })

	name := h.vm(t, func(c *store.VMConfig) { c.Packs = []string{p.Name} })
	h.start(t, name, nil)

	if got := h.sh(t, name, `echo "$E2E_SECRET"`); got != value {
		t.Errorf("exec env: got %q", got)
	}
	if got := h.sh(t, name, `. /run/onyx/env; echo "$E2E_SECRET"`); got != value {
		t.Errorf("/run/onyx/env: got %q", got)
	}
	if got := h.sh(t, name, "findmnt -no FSTYPE -T /run/onyx/env"); got != "tmpfs" {
		t.Errorf("/run/onyx/env on %q, want tmpfs", got)
	}
	// A login shell (what the console and ossh give the user) sees it too.
	if got := h.sh(t, name, `su - dev -c 'bash -lc "echo \$E2E_SECRET"'`); got != value {
		t.Errorf("login shell env: got %q", got)
	}
	// The root disk never holds the value.
	if got := h.sh(t, name, fmt.Sprintf("grep -rl %q /etc /home /root /var 2>/dev/null; true", value)); got != "" {
		t.Errorf("value found on disk at: %s", got)
	}
	audit, err := os.ReadFile(filepath.Join(h.dir, "audit.log"))
	if err != nil {
		t.Fatalf("audit.log: %v", err)
	}
	if !strings.Contains(string(audit), key) {
		t.Errorf("audit.log does not name %s:\n%s", key, audit)
	}
	if strings.Contains(string(audit), value) {
		t.Error("audit.log contains the secret value")
	}
	if b, _ := os.ReadFile(filepath.Join(store.Root{Dir: h.dir}.VMDir(name), "console.log")); strings.Contains(string(b), value) {
		t.Error("console.log contains the secret value")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
