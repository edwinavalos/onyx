package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/edwinavalos/onyx/internal/keychain"
	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Packs returns the pack store.
func (c *Core) Packs() pack.Store { return pack.Store{Dir: filepath.Join(c.root.Dir, "packs")} }

// resolvePack reads every secret in p from the Keychain and returns the
// items to send to a guest. Values are never logged.
func resolvePack(ctx context.Context, p pack.Pack) ([]vsockproto.SecretItem, error) {
	items := make([]vsockproto.SecretItem, 0, len(p.Secrets))
	for _, s := range p.Secrets {
		if s.Mode == pack.ModeProxy {
			continue // handled by startProxies; the value never leaves the host
		}
		v, err := keychain.Get(ctx, s.Key)
		if err != nil {
			return nil, fmt.Errorf("pack %s: %w", p.Name, err)
		}
		it := vsockproto.SecretItem{Mode: string(s.Mode), Value: v}
		switch s.Mode {
		case pack.ModeEnv:
			it.Name = s.Name
			if it.Name == "" {
				it.Name = s.Key
			}
		case pack.ModeFile:
			it.Path = s.Path
			it.Perm = 0o600
			if s.Perm != "" {
				perm, err := strconv.ParseUint(s.Perm, 8, 12)
				if err != nil || perm > 0o777 {
					return nil, fmt.Errorf("pack %s: secret %s: bad perm %q", p.Name, s.Key, s.Perm)
				}
				it.Perm = uint32(perm) // #nosec G115 -- bounded to 12 bits above
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// deliverProxies starts host-side credential proxies for the VM's packs
// and tells the guest to bridge them.
func (c *Core) deliverProxies(ctx context.Context, inst *instance, packs []string) error {
	items, err := c.startProxies(ctx, inst, packs)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if _, err := inst.call(vsockproto.Request{Op: "proxies", Proxies: items}); err != nil {
		return fmt.Errorf("deliver proxies: %w", err)
	}
	return nil
}

// DeliverPacks resolves each named pack and sends it to the running VM.
// Every secret delivered is recorded in the audit log by name, never value.
func (c *Core) DeliverPacks(ctx context.Context, vmName string, packs []string) error {
	inst, err := c.instance(vmName)
	if err != nil {
		return err
	}
	for _, name := range packs {
		p, err := c.Packs().Load(name)
		if err != nil {
			return err
		}
		items, err := resolvePack(ctx, p)
		if err != nil {
			return err
		}
		if _, err := inst.call(vsockproto.Request{Op: "secrets", Secrets: items}); err != nil {
			return fmt.Errorf("deliver pack %s to %s: %w", name, vmName, err)
		}
		for _, s := range p.Secrets {
			if s.Mode != pack.ModeProxy {
				c.audit(vmName, name, s.Key, string(s.Mode))
			}
		}
	}
	return nil
}

// audit appends one line to audit.log. Failures are logged, not fatal.
func (c *Core) audit(vmName, packName, key, mode string) {
	line := fmt.Sprintf("%s vm=%s pack=%s secret=%s mode=%s\n", time.Now().UTC().Format(time.RFC3339), vmName, packName, key, mode)
	f, err := os.OpenFile(filepath.Join(c.root.Dir, "audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("core: audit open", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		slog.Warn("core: audit write", "err", err)
	}
	slog.Info("core: secret delivered", "vm", vmName, "pack", packName, "secret", key, "mode", mode)
}
