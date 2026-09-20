package guest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// SessionFile is sourced by the work user's login profile on the serial
// console. It lives on tmpfs like the env file.
var SessionFile = "/run/onyx/session"

// ConsoleTTY is the serial console device the session runs on.
const ConsoleTTY = "/dev/hvc0"

func writeSession(s vsockproto.Session) error { return writeSessionOwned(s, workUID, workGID) }

// writeSessionOwned writes the session file for the work user (uid:gid),
// who empties it once the login profile has run the session: a later
// autologin — the command was a bare exit, or the user typed exit at the
// shell it dropped to — must find a plain shell, not run it again (issue #7).
func writeSessionOwned(s vsockproto.Session, uid, gid int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "ONYX_SESSION_DIR='%s'\n", shellQuote(s.Dir))
	fmt.Fprintf(&b, "ONYX_SESSION_CMD='%s'\n", shellQuote(s.Cmd))
	if s.Rows > 0 && s.Cols > 0 {
		fmt.Fprintf(&b, "ONYX_ROWS=%d\nONYX_COLS=%d\n", s.Rows, s.Cols)
	}
	switch s.OnExit {
	case "", "shell":
		b.WriteString("ONYX_SESSION_EXIT=shell\n")
	case "poweroff":
		b.WriteString("ONYX_SESSION_EXIT=poweroff\n")
	default:
		return fmt.Errorf("session: unknown on_exit %q", s.OnExit)
	}
	if err := os.MkdirAll(filepath.Dir(SessionFile), envDirPerm); err != nil {
		return err
	}
	if err := os.WriteFile(SessionFile, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Chown(SessionFile, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", SessionFile, err)
	}
	return nil
}

func setWinsize(ctx context.Context, rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return fmt.Errorf("winsize: rows and cols required")
	}
	out, err := exec.CommandContext(ctx, "stty", "-F", ConsoleTTY, "rows", strconv.Itoa(int(rows)), "cols", strconv.Itoa(int(cols))).CombinedOutput() // #nosec G204 -- fixed argv
	if err != nil {
		return fmt.Errorf("stty: %w: %s", err, out)
	}
	return nil
}

func shellQuote(s string) string { return strings.ReplaceAll(s, "'", `'\''`) }
