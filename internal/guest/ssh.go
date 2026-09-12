package guest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sshdAddr is where the image's sshd listens (loopback only; see
// rootfs-overlay/etc/ssh/sshd_config).
const sshdAddr = "127.0.0.1:22"

// sshUser is the work user whose authorized_keys the host manages.
const sshUser = "dev"

// ServeSSH bridges each connection accepted on l (the vsock SSHPort) to the
// guest's sshd. The tunnel is the only route to sshd: it never listens on
// the NIC, and restricted VMs have none.
func ServeSSH(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			s, err := dialSSHD()
			if err != nil {
				slog.Warn("guest: ssh bridge: sshd unreachable", "err", err)
				return
			}
			defer s.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(s, c); _ = s.Close(); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, s); _ = c.Close(); done <- struct{}{} }()
			<-done
		}()
	}
}

// authorizeKey makes key the sole entry in the work user's authorized_keys.
// Onyx owns that file: it is one host key per install, replaced on every
// start so a rotated host key never leaves a stale one behind.
func authorizeKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\n\r") {
		return fmt.Errorf("sshkey: want one public key line")
	}
	u, err := user.Lookup(sshUser)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	dir := filepath.Join(u.HomeDir, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		return err
	}
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}

// dialSSHD connects to sshd, waiting briefly for it: openrc may still be
// bringing it up when the host connects right after boot.
func dialSSHD() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", sshdAddr)
		if err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(250 * time.Millisecond):
		}
	}
}
