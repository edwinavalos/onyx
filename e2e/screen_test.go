//go:build e2e

package e2e

import (
	"context"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hinshun/vt10x"
)

// screen is the console rendered through a VT emulator: assertions are
// made on the grid a person would see, not on the byte stream. Attaching
// replays the core's recent-output buffer, so a screen attached after a
// start still shows the boot.
type screen struct {
	cols, rows int
	term       vt10x.Terminal
	conn       net.Conn
	mu         sync.Mutex
	raw        strings.Builder // everything received, for failure reports
	err        error
}

// attach opens the VM's console into a cols×rows emulator.
func (hh *harness) attach(t *testing.T, name string, cols, rows int) *screen {
	t.Helper()
	conn, err := hh.cl.Console(context.Background(), name)
	if err != nil {
		t.Fatalf("console %s: %v", name, err)
	}
	s := &screen{cols: cols, rows: rows, term: vt10x.New(vt10x.WithSize(cols, rows)), conn: conn}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.raw.Write(buf[:n])
				_, _ = s.term.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		if t.Failed() || os.Getenv("ONYX_E2E_VERBOSE") != "" {
			t.Logf("screen %s (%dx%d):\n%s", name, cols, rows, s.dump())
		}
	})
	return s
}

// lines returns the grid, one string per row, trailing blanks trimmed.
func (s *screen) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, s.rows)
	for _, l := range strings.Split(s.term.String(), "\n") {
		out = append(out, strings.TrimRight(l, " "))
	}
	for len(out) > s.rows {
		out = out[:len(out)-1]
	}
	return out
}

// cursor returns the cursor's row and column.
func (s *screen) cursor() (row, col int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.term.Cursor()
	return c.Y, c.X
}

// send types into the guest.
func (s *screen) send(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(s.conn, text); err != nil {
		t.Fatalf("console write: %v", err)
	}
}

// waitFor polls the grid until pred holds, returning it; fails on timeout.
func (s *screen) waitFor(t *testing.T, what string, timeout time.Duration, pred func(lines []string) bool) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		l := s.lines()
		if pred(l) {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("screen: %s not seen within %s", what, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitText waits until some row contains text.
func (s *screen) waitText(t *testing.T, text string, timeout time.Duration) []string {
	t.Helper()
	return s.waitFor(t, "text "+text, timeout, func(l []string) bool { return rowWith(l, text) >= 0 })
}

// dump renders the grid with row numbers plus the raw byte count.
func (s *screen) dump() string {
	var b strings.Builder
	for i, l := range s.lines() {
		b.WriteString(strings.Repeat(" ", 2-len(itoa(i))) + itoa(i) + "|" + l + "\n")
	}
	s.mu.Lock()
	b.WriteString(itoa(s.raw.Len()) + " bytes received")
	if s.err != nil {
		b.WriteString("; stream error: " + s.err.Error())
	}
	s.mu.Unlock()
	return b.String()
}

// rowWith returns the first row containing text, or -1.
func rowWith(lines []string, text string) int {
	for i, l := range lines {
		if strings.Contains(l, text) {
			return i
		}
	}
	return -1
}

// lastNonEmpty returns the index of the last row with content, or -1.
func lastNonEmpty(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			return i
		}
	}
	return -1
}

// openrcLine matches OpenRC service output ("sshd | * Starting sshd ...",
// " * Mounting /run ... [ ok ]"), which must never reach the console
// after the login (8c0c94a).
var openrcLine = regexp.MustCompile(`^\s*(\S+\s+\|\s*)?\*\s|\[ ok \]`)

func itoa(i int) string { return strconv.Itoa(i) }
