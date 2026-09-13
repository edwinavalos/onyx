package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/edwinavalos/onyx/internal/store"
)

// fakeImage gives the core a stand-in "base" image so CreateVM can clone a
// root disk without Virtualization.
func fakeImage(t *testing.T, c *Core) {
	t.Helper()
	img := c.root.ImageDir("base")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(img, "rootfs.img"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func volumeExists(c *Core, name string) bool {
	_, err := os.Stat(c.root.VolumePath(name))
	return err == nil
}

// sessionCfg is what a session request defines: a fresh work volume plus
// the shared state volume, which already exists.
func sessionCfg() store.VMConfig {
	const name = "s1"
	return store.VMConfig{Name: name, Image: "base", Volumes: []store.VolumeMount{
		{Volume: name + "-work", Target: "/home/dev/work"},
		{Volume: "claude-state", Target: "/home/dev/.claude"},
	}}
}

// A session request asks the core to create the volumes it names that do
// not exist yet; those, and only those, are recorded as owned by the
// definition (decisions.md D18).
func TestCreateVMCreatesMissingVolumesAndOwnsThem(t *testing.T) {
	c, _ := newTestCore(t)
	fakeImage(t, c)
	if _, err := c.CreateVolume("claude-state", 1); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateVM(context.Background(), sessionCfg(), 1); err != nil {
		t.Fatal(err)
	}
	if !volumeExists(c, "s1-work") {
		t.Fatal("work volume was not created")
	}
	cfg, err := c.root.LoadVM("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OwnedVolumes) != 1 || cfg.OwnedVolumes[0] != "s1-work" {
		t.Fatalf("owned = %v, want [s1-work]", cfg.OwnedVolumes)
	}
}

// Without the flag a missing volume is still an error: plain `vm create`
// does not conjure disks.
func TestCreateVMWithoutFlagRequiresVolumes(t *testing.T) {
	c, _ := newTestCore(t)
	fakeImage(t, c)
	err := c.CreateVM(context.Background(), store.VMConfig{Name: "s1", Volumes: []store.VolumeMount{{Volume: "nope", Target: "/x"}}}, 0)
	if err == nil || volumeExists(c, "nope") {
		t.Fatalf("err = %v, volume exists = %v; want not-found and no volume", err, volumeExists(c, "nope"))
	}
}

// Removing a VM that never ran takes the volumes it created with it and
// leaves pre-existing ones alone. That is the orphan case: the start
// failed and the caller removed the definition.
func TestRemoveVMDeletesOwnedVolumesThatNeverRan(t *testing.T) {
	c, _ := newTestCore(t)
	fakeImage(t, c)
	if _, err := c.CreateVolume("claude-state", 1); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateVM(context.Background(), sessionCfg(), 1); err != nil {
		t.Fatal(err)
	}
	withTimeout(t, "RemoveVM", func() {
		if err := c.RemoveVM("s1"); err != nil {
			t.Fatal(err)
		}
	})
	if volumeExists(c, "s1-work") {
		t.Error("owned work volume survived RemoveVM")
	}
	if !volumeExists(c, "claude-state") {
		t.Error("pre-existing state volume was removed")
	}
}

// Once the VM has come up (session delivered, console live) the work
// volume holds the session's data and is the user's: ownership is dropped
// and RemoveVM keeps it, which is the D14 persistence contract.
func TestReadyReleasesOwnedVolumes(t *testing.T) {
	c, _ := newTestCore(t)
	fakeImage(t, c)
	if err := c.CreateVM(context.Background(), sessionCfg(), 1); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.root.LoadVM("s1")
	if err != nil {
		t.Fatal(err)
	}
	inst := &instance{cfg: cfg}
	c.mu.Lock()
	c.running["s1"] = inst
	c.mu.Unlock()
	c.markReady(inst)
	if len(inst.cfg.OwnedVolumes) != 0 {
		t.Errorf("in-memory owned = %v after ready", inst.cfg.OwnedVolumes)
	}
	if saved, _ := c.root.LoadVM("s1"); len(saved.OwnedVolumes) != 0 {
		t.Errorf("persisted owned = %v after ready", saved.OwnedVolumes)
	}
	c.mu.Lock()
	delete(c.running, "s1")
	c.mu.Unlock()
	if err := c.RemoveVM("s1"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"s1-work", "claude-state"} {
		if !volumeExists(c, v) {
			t.Errorf("%s was removed after the session ran", v)
		}
	}
}

// An owned volume that another VM picked up in the meantime (two sessions
// racing to create claude-state) is left in place.
func TestRemoveVMKeepsOwnedVolumeInUseElsewhere(t *testing.T) {
	c, _ := newTestCore(t)
	fakeImage(t, c)
	if err := c.CreateVM(context.Background(), sessionCfg(), 1); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.running["other"] = &instance{cfg: store.VMConfig{Name: "other", Volumes: []store.VolumeMount{{Volume: "claude-state", Target: "/x"}}}}
	c.mu.Unlock()
	withTimeout(t, "RemoveVM", func() {
		if err := c.RemoveVM("s1"); err != nil {
			t.Fatal(err)
		}
	})
	if volumeExists(c, "s1-work") {
		t.Error("s1-work survived")
	}
	if !volumeExists(c, "claude-state") {
		t.Error("claude-state was removed while attached to a running VM")
	}
}

// The definition is saved before its volumes are created, so a core that
// dies in between leaves a config naming a volume that is not there.
// RemoveVM must still succeed.
func TestRemoveVMToleratesMissingOwnedVolume(t *testing.T) {
	c, _ := newTestCore(t)
	if err := c.root.SaveVM(store.VMConfig{Name: "s1", Image: "base", OwnedVolumes: []string{"gone"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveVM("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.root.LoadVM("s1"); err == nil {
		t.Error("vm still defined")
	}
}

// When cloning the root disk fails after the volumes were made, CreateVM
// undoes its own volumes so a failed create leaves nothing behind.
func TestCreateVMFailureRemovesVolumesItMade(t *testing.T) {
	c, _ := newTestCore(t)
	// An image whose rootfs.img is a directory passes the existence check
	// and fails the clone.
	if err := os.MkdirAll(filepath.Join(c.root.ImageDir("base"), "rootfs.img"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := sessionCfg()
	if _, err := c.CreateVolume("claude-state", 1); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateVM(context.Background(), cfg, 1); err == nil {
		t.Fatal("CreateVM succeeded with a directory in the way of root.img")
	}
	if volumeExists(c, "s1-work") {
		t.Error("s1-work left behind by a failed create")
	}
	if !volumeExists(c, "claude-state") {
		t.Error("pre-existing claude-state removed by a failed create")
	}
}

// ListVolumes reports which definitions attach each volume, so an
// unattached session-*-work volume is obvious.
func TestListVolumesReportsAttachments(t *testing.T) {
	c, _ := newTestCore(t)
	fakeImage(t, c)
	if _, err := c.CreateVolume("orphan-work", 2); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateVM(context.Background(), sessionCfg(), 1); err != nil {
		t.Fatal(err)
	}
	vols, err := c.ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]VolumeInfo{}
	for _, v := range vols {
		byName[v.Name] = v
	}
	if v := byName["s1-work"]; len(v.VMs) != 1 || v.VMs[0] != "s1" || v.SizeMB != 1 {
		t.Errorf("s1-work = %+v", v)
	}
	if v := byName["claude-state"]; len(v.VMs) != 1 || v.VMs[0] != "s1" {
		t.Errorf("claude-state = %+v", v)
	}
	if v, ok := byName["orphan-work"]; !ok || len(v.VMs) != 0 || v.SizeMB != 2 {
		t.Errorf("orphan-work = %+v (present %v)", v, ok)
	}
}
