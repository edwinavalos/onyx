package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/edwinavalos/onyx/internal/store"
)

// A VM defined without a size gets the small default: sandboxes share a
// laptop with everything else, and the host is what pays for oversizing.
func TestCreateVMFillsSmallDefaults(t *testing.T) {
	c, _ := newTestCore(t)
	// A tiny stand-in rootfs is enough for CreateVM to clone.
	img := c.root.ImageDir("base")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(img, "rootfs.img"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateVM(context.Background(), store.VMConfig{Name: "small", Image: "base"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.root.LoadVM("small")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CPUs != store.DefaultCPUs || cfg.MemoryMB != store.DefaultMemoryMB {
		t.Fatalf("got %d vCPU / %d MB, want %d / %d", cfg.CPUs, cfg.MemoryMB, store.DefaultCPUs, store.DefaultMemoryMB)
	}
	if store.DefaultCPUs != 1 || store.DefaultMemoryMB != 512 {
		t.Fatalf("defaults are %d vCPU / %d MB, want 1 / 512", store.DefaultCPUs, store.DefaultMemoryMB)
	}
}
