package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinavalos/onyx/internal/store"
)

// A guest kernel oops is rare and the console log that holds it is
// deleted with the VM (session VMs go right after they stop), so the
// trace must be copied aside when the VM stops.
func TestArchiveGuestCrash(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	dir := root.VMDir("s1")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "console.log")
	if err := os.WriteFile(log, []byte("boot\r\n[   33.3] Internal error: Oops: 0000000096000006 [#1] SMP\r\nCall trace:\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, ok := archiveGuestCrash(root, "s1")
	if !ok {
		t.Fatal("oops not detected")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil || !strings.Contains(string(b), "Internal error: Oops") {
		t.Fatalf("archive %s: %v %q", path, err, b)
	}
	if !strings.HasPrefix(path, root.CrashesDir()) || !strings.Contains(filepath.Base(path), "s1") {
		t.Fatalf("archive at %s, want under %s named after the VM", path, root.CrashesDir())
	}

	if err := os.WriteFile(log, []byte("boot\r\nclean shutdown\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := archiveGuestCrash(root, "s1"); ok {
		t.Fatal("clean log archived")
	}
	if _, ok := archiveGuestCrash(root, "missing"); ok {
		t.Fatal("missing log archived")
	}
}
