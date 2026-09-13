package core

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

// crashMarkers are what the guest kernel prints when it dies.
var crashMarkers = [][]byte{[]byte("Internal error: Oops"), []byte("Kernel panic"), []byte("BUG: ")}

// archiveGuestCrash copies a VM's console log into the crashes directory
// when it holds a kernel oops or panic, so the trace survives the VM's
// removal. Returns the archive path.
func archiveGuestCrash(root store.Root, name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root.VMDir(name), "console.log")) // #nosec G304 -- fixed path under the Onyx root
	if err != nil {
		return "", false
	}
	found := false
	for _, m := range crashMarkers {
		if bytes.Contains(b, m) {
			found = true
			break
		}
	}
	if !found {
		return "", false
	}
	if err := os.MkdirAll(root.CrashesDir(), 0o750); err != nil {
		slog.Warn("core: crash archive", "err", err)
		return "", false
	}
	path := filepath.Join(root.CrashesDir(), name+"-"+time.Now().UTC().Format("20060102-150405")+".log")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		slog.Warn("core: crash archive", "err", err)
		return "", false
	}
	return path, true
}
