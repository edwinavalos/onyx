// Package guest implements the in-VM Onyx agent. It runs as an OpenRC
// service inside the guest and serves requests from the host over vsock.
package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Serve accepts connections on l and dispatches requests until l is closed.
func Serve(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go handle(c)
	}
}

func handle(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req vsockproto.Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(vsockproto.Response{Error: "bad request: " + err.Error()})
			continue
		}
		resp := dispatch(req)
		if err := enc.Encode(resp); err != nil {
			slog.Warn("guest: write response", "err", err)
			return
		}
	}
}

// execTimeout bounds every command the agent runs on the host's behalf.
const execTimeout = 5 * time.Minute

func dispatch(req vsockproto.Request) vsockproto.Response {
	slog.Info("guest: request", "op", req.Op)
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	switch req.Op {
	case "ping":
		host, _ := os.Hostname()
		// Uptime lets the host attribute start latency to the guest boot.
		up, _ := os.ReadFile("/proc/uptime")
		return vsockproto.Response{OK: "pong from " + host, Output: string(up)}
	case "mount":
		if err := mountVolume(ctx, req.Device, req.Target); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: "mounted " + req.Device + " at " + req.Target}
	case "exec":
		if len(req.Argv) == 0 {
			return vsockproto.Response{Error: "exec: empty argv"}
		}
		cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...) // #nosec G204 -- host is trusted
		cmd.Env = envForExec()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return vsockproto.Response{Error: err.Error(), Output: string(out)}
		}
		return vsockproto.Response{OK: "exec", Output: string(out)}
	case "clock":
		if err := setClock(req.UnixNanos); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: "clock set"}
	case "session":
		if req.Session == nil {
			return vsockproto.Response{Error: "session: missing body"}
		}
		if err := writeSession(*req.Session); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: "session set"}
	case "winsize":
		if err := setWinsize(ctx, req.Rows, req.Cols); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: "winsize set"}
	case "proxies":
		if err := applyProxies(req.Proxies); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: fmt.Sprintf("bridged %d proxies", len(req.Proxies))}
	case "sshkey":
		if err := authorizeKey(req.SSHKey); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: "ssh key authorized"}
	case "egress":
		if err := applyEgress(req.Egress); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: "egress bridged"}
	case "secrets":
		if err := applySecrets(req.Secrets); err != nil {
			return vsockproto.Response{Error: err.Error()}
		}
		return vsockproto.Response{OK: fmt.Sprintf("applied %d secrets", len(req.Secrets))}
	default:
		return vsockproto.Response{Error: "unknown op " + req.Op}
	}
}

// mountVolume formats device as ext4 if it has no filesystem, then mounts it
// at target. Idempotent: an already-mounted target is left alone.
func mountVolume(ctx context.Context, device, target string) error {
	if device == "" || target == "" {
		return fmt.Errorf("mount: device and target required")
	}
	if mounted(target) {
		return nil
	}
	out, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "TYPE", device).Output() // #nosec G204
	if err != nil || strings.TrimSpace(string(out)) == "" {
		slog.Info("guest: formatting volume", "device", device)
		if out, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", device).CombinedOutput(); err != nil { // #nosec G204
			return fmt.Errorf("mkfs.ext4 %s: %w: %s", device, err, out)
		}
	}
	if err := mkdirOwned(target, workHome, workUID, workGID); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "mount", device, target).CombinedOutput(); err != nil { // #nosec G204
		return fmt.Errorf("mount %s %s: %w: %s", device, target, err, out)
	}
	// Hand a fresh volume to the work user so it can write to it.
	if ents, err := os.ReadDir(target); err == nil && len(ents) <= 1 { // only lost+found
		if err := os.Chown(target, workUID, workGID); err != nil {
			return fmt.Errorf("chown %s: %w", target, err)
		}
	}
	return nil
}

// workUID/workGID identify the image's work user (see images/build-alpine.sh);
// workHome is its home directory.
const (
	workUID  = 1000
	workGID  = 1000
	workHome = "/home/dev"
)

// mkdirOwned creates target and any missing parents. Directories it creates
// under home are handed to the work user, so a nested mount point such as
// /home/dev/work/<volume> is reachable by that user: a plain MkdirAll as
// root would leave a root-owned /home/dev/work in the way (issue #2).
// Existing directories are left as they are.
func mkdirOwned(target, home string, uid, gid int) error {
	if !strings.HasPrefix(target, home+string(filepath.Separator)) {
		return os.MkdirAll(target, 0o750)
	}
	var missing []string
	for p := target; strings.HasPrefix(p, home+string(filepath.Separator)); p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			break
		}
		missing = append(missing, p)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		if err := os.Chown(missing[i], uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", missing[i], err)
		}
	}
	return nil
}

func mounted(target string) bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[1] == target {
			return true
		}
	}
	return false
}
