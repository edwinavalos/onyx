package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// EnvFile is where env-mode secrets are written, as `export NAME='value'`
// lines. /run is tmpfs on the base image so nothing reaches the disk.
// Login shells source it via /etc/profile.d/onyx.sh.
const EnvFile = "/run/onyx/env"

const envDirPerm = 0o755

var (
	envMu  sync.Mutex
	envMap = map[string]string{}
)

// applySecrets installs items and rewrites EnvFile.
func applySecrets(items []vsockproto.SecretItem) error {
	envMu.Lock()
	defer envMu.Unlock()
	for _, it := range items {
		switch it.Mode {
		case "env":
			if it.Name == "" {
				return fmt.Errorf("env secret without a name")
			}
			envMap[it.Name] = it.Value
		case "file":
			if !filepath.IsAbs(it.Path) {
				return fmt.Errorf("file secret %q: path must be absolute", it.Path)
			}
			perm := os.FileMode(it.Perm)
			if perm == 0 {
				perm = 0o600
			}
			if err := os.MkdirAll(filepath.Dir(it.Path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(it.Path, []byte(it.Value), perm); err != nil {
				return fmt.Errorf("write %s: %w", it.Path, err)
			}
			// WriteFile honours umask; force the requested mode.
			if err := os.Chmod(it.Path, perm); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown secret mode %q", it.Mode)
		}
	}
	return writeEnvFile()
}

func writeEnvFile() error {
	names := make([]string, 0, len(envMap))
	for n := range envMap {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "export %s='%s'\n", n, strings.ReplaceAll(envMap[n], "'", `'\''`))
	}
	// Directory is world-searchable so the (non-root) work user can source
	// the env file; the file is the boundary, not the directory.
	if err := os.MkdirAll(filepath.Dir(EnvFile), envDirPerm); err != nil {
		return err
	}
	return os.WriteFile(EnvFile, []byte(b.String()), 0o644) // #nosec G306 -- readable by the work user; the VM is the boundary
}

// envForExec returns the current process environment with delivered
// env-mode secrets applied.
func envForExec() []string {
	envMu.Lock()
	defer envMu.Unlock()
	env := os.Environ()
	for n, v := range envMap {
		env = append(env, n+"="+v)
	}
	return env
}
