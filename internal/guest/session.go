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

func writeSession(s vsockproto.Session) error {
	var b strings.Builder
	fmt.Fprintf(&b, "ONYX_SESSION_DIR='%s'\n", shellQuote(s.Dir))
	fmt.Fprintf(&b, "ONYX_SESSION_CMD='%s'\n", shellQuote(s.Cmd))
	if s.Rows > 0 && s.Cols > 0 {
		fmt.Fprintf(&b, "ONYX_ROWS=%d\nONYX_COLS=%d\n", s.Rows, s.Cols)
	}
	if err := os.MkdirAll(filepath.Dir(SessionFile), envDirPerm); err != nil {
		return err
	}
	return os.WriteFile(SessionFile, []byte(b.String()), 0o644) // #nosec G306 -- read by the work user's profile
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
