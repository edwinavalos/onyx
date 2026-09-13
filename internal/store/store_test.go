package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"dev", "session-20260912-010203", "a.b_c"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "-x", "a/b", "a b", "x\x00"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) accepted", bad)
		}
	}
}

func TestVMRoundTripAndListing(t *testing.T) {
	r := Root{Dir: t.TempDir()}
	if err := r.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LoadVM("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadVM missing = %v, want ErrNotFound", err)
	}
	cfg := VMConfig{Name: "dev", Image: "base", CPUs: 2, MemoryMB: 1024,
		Volumes: []VolumeMount{{Volume: "work", Target: "/home/dev/work"}}, Packs: []string{"claude"}}
	if err := r.SaveVM(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := r.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "dev" || got.Volumes[0].Target != "/home/dev/work" || got.Packs[0] != "claude" {
		t.Fatalf("LoadVM = %+v", got)
	}
	if err := r.SaveVM(VMConfig{Name: "../escape"}); err == nil {
		t.Fatal("SaveVM accepted a path-traversal name")
	}

	vms, err := r.ListVMs()
	if err != nil || len(vms) != 1 || vms[0] != "dev" {
		t.Fatalf("ListVMs = %v, %v", vms, err)
	}

	for _, f := range []string{"a.img", "b.img", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(r.VolumesDir(), f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	vols, err := r.ListVolumes()
	if err != nil || len(vols) != 2 || vols[0] != "a" || vols[1] != "b" {
		t.Fatalf("ListVolumes = %v, %v", vols, err)
	}
}

func TestSocketOverride(t *testing.T) {
	t.Setenv("ONYX_SOCKET", "/tmp/o.sock")
	if got := (Root{Dir: "/x"}).Socket(); got != "/tmp/o.sock" {
		t.Fatalf("Socket = %q", got)
	}
	t.Setenv("ONYX_SOCKET", "")
	if got := (Root{Dir: "/x"}).Socket(); got != "/x/onyx.sock" {
		t.Fatalf("Socket = %q", got)
	}
}

func TestCloneFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := CloneFile(t.Context(), src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst) // #nosec G304 -- temp path
	if err != nil || string(b) != "hello" {
		t.Fatalf("dst = %q, %v", b, err)
	}
}

// Session work volumes mount under a per-volume path so Claude Code keys
// its memory per project instead of under one shared /home/dev/work
// (issue #2).
func TestWorkMountTarget(t *testing.T) {
	if got := WorkMountTarget("dev-work"); got != "/home/dev/work/dev-work" {
		t.Errorf("WorkMountTarget = %q", got)
	}
	if WorkRoot != "/home/dev/work" {
		t.Errorf("WorkRoot = %q", WorkRoot)
	}
}
