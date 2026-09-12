package core

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// SSHKeyPath is the per-install ed25519 key `onyx ssh` authenticates with.
// It is generated on first use and its public half is installed as the
// work user's only authorized key at every VM start.
func (c *Core) SSHKeyPath() string { return filepath.Join(c.root.Dir, "ssh", "id_ed25519") }

// sshPublicKey returns the public key line, generating the pair if needed.
func (c *Core) sshPublicKey(ctx context.Context) (string, error) {
	priv := c.SSHKeyPath()
	pub := priv + ".pub"
	if _, err := os.Stat(pub); err != nil {
		if err := os.MkdirAll(filepath.Dir(priv), 0o700); err != nil {
			return "", err
		}
		_ = os.Remove(priv)
		// ssh-keygen writes the standard formats and ships with macOS;
		// -N "" is deliberate: the key only opens a vsock tunnel to a VM
		// on this machine, so a passphrase would buy nothing.
		cmd := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "onyx", "-f", priv) // #nosec G204 -- fixed path under the Onyx root
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	b, err := os.ReadFile(pub) // #nosec G304 -- fixed path under the Onyx root
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// deliverSSHKey authorizes the install's key in the guest.
func (c *Core) deliverSSHKey(ctx context.Context, inst *instance) error {
	key, err := c.sshPublicKey(ctx)
	if err != nil {
		return err
	}
	if _, err := inst.call(vsockproto.Request{Op: "sshkey", SSHKey: key}); err != nil {
		return fmt.Errorf("deliver ssh key: %w", err)
	}
	return nil
}

// Tunnel opens a raw stream to a vsock port in a running VM (SSHPort for
// `onyx ssh`). The caller owns the connection.
func (c *Core) Tunnel(ctx context.Context, name string, port uint32) (net.Conn, error) {
	inst, err := c.instance(name)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return inst.machine.DialGuest(dialCtx, port)
}
