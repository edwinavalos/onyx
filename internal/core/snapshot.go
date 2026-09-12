package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

// Suspend = Vz snapshot: the host saves the guest's memory and device state
// with Virtualization.framework and stops the VM; the next StartVM restores
// it instead of booting. The state only restores into an identical
// configuration, so a fingerprint guards against restoring onto changed
// definitions, and volumes of a suspended VM cannot be attached elsewhere
// (decisions.md D14, docs/suspend-guide.md).

const (
	snapshotFile = "state.vzs"
	snapshotMeta = "state.json"
)

type snapshotInfo struct {
	Fingerprint string    `json:"fingerprint"`
	SavedAt     time.Time `json:"saved_at"`
}

// SuspendVM pauses the VM, saves its state next to its config and stops it.
func (c *Core) SuspendVM(ctx context.Context, name string) error {
	inst, err := c.instance(name)
	if err != nil {
		return err
	}
	if !inst.machine.Savable() {
		return errors.New("virtualization refused save/restore for this VM configuration")
	}
	dir := c.root.VMDir(name)
	path := filepath.Join(dir, snapshotFile)
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := inst.machine.Pause(); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	if err := inst.machine.SaveState(tmp); err != nil {
		_ = os.Remove(tmp)
		_ = inst.machine.Resume()
		return fmt.Errorf("save state: %w", err)
	}
	fp, err := c.snapshotFingerprint(inst.cfg)
	if err != nil {
		_ = os.Remove(tmp)
		_ = inst.machine.Resume()
		return err
	}
	meta, _ := json.Marshal(snapshotInfo{Fingerprint: fp, SavedAt: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(dir, snapshotMeta), meta, 0o600); err != nil {
		_ = os.Remove(tmp)
		_ = inst.machine.Resume()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		_ = inst.machine.Resume()
		return err
	}
	stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := inst.machine.Stop(stopCtx); err != nil {
		return fmt.Errorf("stop after snapshot: %w", err)
	}
	slog.Info("core: vm suspended", "name", name, "state", path)
	return nil
}

// suspendedHolder returns the name of a suspended VM that has volume
// attached, or "".
func (c *Core) suspendedHolder(volume string) string {
	names, _ := c.root.ListVMs()
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(c.root.VMDir(n), snapshotFile)); err != nil {
			continue
		}
		cfg, err := c.root.LoadVM(n)
		if err != nil {
			continue
		}
		for _, m := range cfg.Volumes {
			if m.Volume == volume {
				return n
			}
		}
	}
	return ""
}

// pendingSnapshot returns the snapshot path if one exists and still matches
// the VM's configuration; a stale one is deleted.
func (c *Core) pendingSnapshot(cfg store.VMConfig) string {
	dir := c.root.VMDir(cfg.Name)
	path := filepath.Join(dir, snapshotFile)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	var info snapshotInfo
	b, err := os.ReadFile(filepath.Join(dir, snapshotMeta)) // #nosec G304 -- name validated
	if err == nil {
		err = json.Unmarshal(b, &info)
	}
	fp, fperr := c.snapshotFingerprint(cfg)
	if err != nil || fperr != nil || info.Fingerprint != fp {
		slog.Warn("core: discarding snapshot; VM configuration changed since it was taken", "name", cfg.Name)
		c.discardSnapshot(cfg.Name)
		return ""
	}
	return path
}

func (c *Core) discardSnapshot(name string) {
	dir := c.root.VMDir(name)
	_ = os.Remove(filepath.Join(dir, snapshotFile))
	_ = os.Remove(filepath.Join(dir, snapshotMeta))
}

// snapshotFingerprint hashes everything a restore must find unchanged:
// the definition (image, CPUs, memory, MAC, cmdline, volume list) and the
// machine identifier.
func (c *Core) snapshotFingerprint(cfg store.VMConfig) (string, error) {
	id, err := os.ReadFile(filepath.Join(c.root.VMDir(cfg.Name), "machine-id.bin")) // #nosec G304 -- name validated
	if err != nil {
		return "", fmt.Errorf("machine id: %w", err)
	}
	def, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(def)
	h.Write(id)
	return hex.EncodeToString(h.Sum(nil)), nil
}
