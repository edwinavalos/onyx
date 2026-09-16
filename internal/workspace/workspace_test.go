package workspace

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	got := Path("/root", "default")
	want := filepath.Join("/root", "default")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestEnsureIdempotent(t *testing.T) {
	dir := t.TempDir()

	p1, err := Ensure(dir, "default")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if p1 != Path(dir, "default") {
		t.Fatalf("Ensure returned %q, want %q", p1, Path(dir, "default"))
	}

	// Marker file from the first call must not make the second call look
	// like a conflict, and must not be recreated with a different timestamp.
	meta1, err := Meta(dir, "default")
	if err != nil {
		t.Fatalf("Meta after first Ensure: %v", err)
	}

	p2, err := Ensure(dir, "default")
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if p2 != p1 {
		t.Fatalf("second Ensure returned %q, want %q", p2, p1)
	}

	meta2, err := Meta(dir, "default")
	if err != nil {
		t.Fatalf("Meta after second Ensure: %v", err)
	}
	if meta1.Created != meta2.Created {
		t.Fatalf("Ensure recreated the marker: created %v then %v", meta1.Created, meta2.Created)
	}
}

func TestEnsureCreatesMarker(t *testing.T) {
	dir := t.TempDir()

	if _, err := Ensure(dir, "default"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	meta, err := Meta(dir, "default")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.Name != "default" {
		t.Fatalf("meta.Name = %q, want %q", meta.Name, "default")
	}
	if meta.Created.IsZero() {
		t.Fatalf("meta.Created is zero")
	}
	if meta.AutoAttach {
		t.Fatalf("meta.AutoAttach should default to false")
	}
}

func TestSetAutoAttachPersists(t *testing.T) {
	dir := t.TempDir()

	if _, err := Ensure(dir, "default"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if err := SetAutoAttach(dir, "default", true); err != nil {
		t.Fatalf("SetAutoAttach(true): %v", err)
	}
	meta, err := Meta(dir, "default")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if !meta.AutoAttach {
		t.Fatalf("meta.AutoAttach = false, want true after SetAutoAttach(true)")
	}

	if err := SetAutoAttach(dir, "default", false); err != nil {
		t.Fatalf("SetAutoAttach(false): %v", err)
	}
	meta, err = Meta(dir, "default")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.AutoAttach {
		t.Fatalf("meta.AutoAttach = true, want false after SetAutoAttach(false)")
	}
}

func TestSetAutoAttachRequiresExistingWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := SetAutoAttach(dir, "missing", true); err == nil {
		t.Fatalf("SetAutoAttach on a non-existent workspace: want error, got nil")
	}
}

func TestTag(t *testing.T) {
	got := Tag("default")
	want := "onyx-ws-default"
	if got != want {
		t.Fatalf("Tag = %q, want %q", got, want)
	}
}
