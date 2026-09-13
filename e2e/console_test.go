//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Console and session scenarios from docs/e2e-test-survey.md: what a
// terminal attached to the VM sees around resizes, detaches and the end
// of the session command.

const shellPrompt = "prompt"

// waitPrompt waits for the shell prompt to be the last row with the
// cursor on it, and returns that row.
func (s *screen) waitPrompt(t *testing.T, timeout time.Duration) int {
	t.Helper()
	row := -1
	s.waitFor(t, shellPrompt, timeout, func(l []string) bool { row = s.promptRow(l); return row >= 0 })
	return row
}

// Resize before the agent is up: the terminal reports its size while the
// VM is still booting, and the session starts with that size, not the
// nominal one in the start request (dc5fa17).
func TestResizeBeforeSessionUp(t *testing.T) {
	h := need(t)
	name := h.vm(t, nil)
	const nomRows, nomCols = 24, 80
	const rows, cols = 30, 100
	sess := &vsockproto.Session{Dir: "/home/dev", Cmd: "stty size", Rows: nomRows, Cols: nomCols, OnExit: "shell"}

	started := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
		defer cancel()
		_, err := h.cl.StartVM(ctx, name, sess)
		started <- err
	}()
	// The slot is reserved as soon as the start is accepted; Resize is
	// refused before that ("not running") and kept for the session after.
	resized := false
	for !resized {
		select {
		case err := <-started:
			t.Fatalf("start finished before a resize could be sent (err %v)", err)
		default:
		}
		if err := h.cl.Resize(context.Background(), name, rows, cols); err == nil {
			resized = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := <-started; err != nil {
		t.Fatalf("start: %v", err)
	}

	s := h.attach(t, name, cols, rows)
	lines := s.waitText(t, "onyx: stty size", 15*time.Second)
	cmd := rowWith(lines, "onyx: stty size")
	lines = s.waitFor(t, "stty output", 5*time.Second, func(l []string) bool { return lastNonEmpty(l) > cmd })
	if got := strings.TrimSpace(lines[cmd+1]); got != fmt.Sprintf("%d %d", rows, cols) {
		t.Errorf("session shell saw size %q, want %d %d (nominal was %d %d)", got, rows, cols, nomRows, nomCols)
	}
	if got := h.sh(t, name, "stty -F /dev/hvc0 size"); got != fmt.Sprintf("%d %d", rows, cols) {
		t.Errorf("guest tty size %q, want %d %d", got, rows, cols)
	}
	if got := h.sh(t, name, "grep -E '^ONYX_(ROWS|COLS)=' /run/onyx/session"); got != fmt.Sprintf("ONYX_ROWS=%d\nONYX_COLS=%d", rows, cols) {
		t.Errorf("session file size:\n%s", got)
	}
	s.waitPrompt(t, 5*time.Second)
}

// Resize while a program runs: the foreground program gets SIGWINCH and
// sees the new size, the guest tty reports it, and a grid at the new size
// still has the prompt on its last row with nothing drawn twice.
func TestResizeWhileProgramRuns(t *testing.T) {
	h := need(t)
	name := h.vm(t, nil)
	const rows, cols = 24, 80
	const newRows, newCols = 40, 120
	h.start(t, name, &vsockproto.Session{Dir: "/home/dev", Rows: rows, Cols: cols, OnExit: "shell"})
	s := h.attach(t, name, cols, rows)
	s.waitPrompt(t, 15*time.Second)

	// A foreground program that reports every window change it is told
	// about (a child of the login shell: bash runs its own WINCH trap only
	// while reading a command line).
	s.send(t, `sh -c 'trap "echo E2E-WINCH \$(stty size)" WINCH; while :; do sleep 0.2; done'`+"\n")
	time.Sleep(500 * time.Millisecond) // let the trap be installed
	if err := h.cl.Resize(context.Background(), name, newRows, newCols); err != nil {
		t.Fatalf("resize: %v", err)
	}
	s.resize(newCols, newRows)
	want := fmt.Sprintf("E2E-WINCH %d %d", newRows, newCols)
	s.waitText(t, want, 5*time.Second)
	if got := h.sh(t, name, "stty -F /dev/hvc0 size"); got != fmt.Sprintf("%d %d", newRows, newCols) {
		t.Errorf("guest tty size %q, want %d %d", got, newRows, newCols)
	}
	// Interrupt the program: the prompt comes back on the last row of the
	// new grid, and the resize report was drawn once.
	s.send(t, "\x03")
	prompt := s.waitPrompt(t, 5*time.Second)
	lines := s.lines()
	if n := countRows(lines, want); n != 1 {
		t.Errorf("resize report drawn %d times:\n%s", n, s.dump())
	}
	// The grid is drawn at the new width: a line longer than the old
	// width stays on one row, both as typed and as echoed.
	long := "E2E-WIDE-" + strings.Repeat("x", cols)
	s.send(t, "echo "+long+"\n")
	lines = s.waitFor(t, "wide line echoed", 5*time.Second, func(l []string) bool { return countRows(l, long) == 2 })
	if r := rowWith(lines, long); r != prompt {
		t.Errorf("wide command typed on row %d, prompt was on row %d", r, prompt)
	}
	if p := s.waitPrompt(t, 5*time.Second); p != prompt+2 {
		t.Errorf("prompt on row %d after one wide line from row %d", p, prompt)
	}
}

// Detach and reattach: closing the console leaves the VM running; a new
// attach replays the tail and keeps streaming; two attached at once both
// see every line exactly once and either can type.
func TestDetachAndReattach(t *testing.T) {
	h := need(t)
	name := h.vm(t, nil)
	const rows, cols = 24, 80
	h.start(t, name, &vsockproto.Session{Dir: "/home/dev", Rows: rows, Cols: cols, OnExit: "shell"})
	first := h.attach(t, name, cols, rows)
	first.waitPrompt(t, 15*time.Second)
	first.send(t, "echo E2E-BEFORE-$((1+0))\n")
	first.waitText(t, "E2E-BEFORE-1", 5*time.Second)
	first.close()

	time.Sleep(300 * time.Millisecond)
	if st, err := h.cl.GetVM(context.Background(), name); err != nil || st.State != "running" {
		t.Fatalf("after detach: state %q err %v", st.State, err)
	}
	// Reattach: the replay shows what was typed before, then new output.
	second := h.attach(t, name, cols, rows)
	second.waitText(t, "E2E-BEFORE-1", 5*time.Second)
	second.waitPrompt(t, 5*time.Second)
	second.send(t, "echo E2E-AFTER-$((1+1))\n")
	second.waitText(t, "E2E-AFTER-2", 5*time.Second)

	// A third, concurrent attach is mirrored: output reaches both, input
	// from either works, and neither grid shows a line twice.
	third := h.attach(t, name, cols, rows)
	third.waitText(t, "E2E-AFTER-2", 5*time.Second)
	third.waitPrompt(t, 5*time.Second)
	third.send(t, "echo E2E-THIRD-$((1+2))\n")
	for i, s := range []*screen{second, third} {
		lines := s.waitText(t, "E2E-THIRD-3", 5*time.Second)
		s.waitPrompt(t, 5*time.Second)
		for _, mark := range []string{"E2E-BEFORE-1", "E2E-AFTER-2", "E2E-THIRD-3"} {
			if n := countRows(lines, mark); n != 1 {
				t.Errorf("screen %d: %s drawn %d times", i+2, mark, n)
			}
		}
	}
	second.send(t, "echo E2E-SECOND-$((2+2))\n")
	second.waitText(t, "E2E-SECOND-4", 5*time.Second)
	third.waitText(t, "E2E-SECOND-4", 5*time.Second)
}

// Session exit → poweroff: the command's exit shuts the VM down cleanly;
// the core reports it stopped and the work volume keeps what the session
// wrote. (The app's reaping of the VM definition is app-side and not
// visible here; the core keeps the definition.)
func TestSessionExitPoweroff(t *testing.T) {
	h := need(t)
	ctx := context.Background()
	vol := fmt.Sprintf("e2e-work-%d", os.Getpid())
	if err := h.cl.CreateVolume(ctx, vol, 64); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.cl.RemoveVolume(context.Background(), vol) })
	name := h.vm(t, func(c *store.VMConfig) {
		c.Volumes = []store.VolumeMount{{Volume: vol, Target: "/home/dev/work"}}
	})
	const rows, cols = 24, 80
	sess := &vsockproto.Session{
		Dir:    "/home/dev/work",
		Cmd:    "sleep 1; echo E2E-WORK > done.txt; echo E2E-EXITING",
		Rows:   rows,
		Cols:   cols,
		OnExit: "poweroff",
	}
	h.start(t, name, sess)
	s := h.attach(t, name, cols, rows)
	s.waitText(t, "E2E-EXITING", 15*time.Second)
	lines := s.waitText(t, "powering off", 5*time.Second)
	if rowWith(lines, "session command exited (0)") < 0 {
		t.Errorf("exit status line missing:\n%s", s.dump())
	}
	h.waitStopped(t, name)

	if _, err := h.cl.GetVM(ctx, name); err != nil {
		t.Errorf("definition gone after poweroff: %v", err)
	}
	vols, err := h.cl.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(vols, vol) {
		t.Fatalf("work volume %s gone after poweroff: %v", vol, vols)
	}
	// The stopped VM's console.log holds the whole session; a restart
	// finds the session's file on the volume.
	b, err := os.ReadFile(filepath.Join(store.Root{Dir: h.dir}.VMDir(name), "console.log"))
	if err != nil || !strings.Contains(string(b), "powering off") {
		t.Errorf("console.log after poweroff: err %v, %d bytes", err, len(b))
	}
	h.start(t, name, nil)
	if got := h.sh(t, name, "cat /home/dev/work/done.txt"); got != "E2E-WORK" {
		t.Errorf("volume after poweroff: %q", got)
	}
}

// Session exit → shell: the prompt is on the screen within a second of
// the exit line. The command is a child process: the session command is
// eval'd by the login shell itself, so a bare `exit` would end the login
// and agetty would log in and run the session again.
func TestSessionExitShell(t *testing.T) {
	h := need(t)
	name := h.vm(t, nil)
	const rows, cols = 24, 80
	h.start(t, name, &vsockproto.Session{Dir: "/home/dev", Cmd: "sh -c 'echo E2E-EXITING; exit 7'", Rows: rows, Cols: cols, OnExit: "shell"})
	s := h.attach(t, name, cols, rows)
	lines := s.waitText(t, "session command exited (7); dropping to shell", 15*time.Second)
	seen := time.Now()
	exit := rowWith(lines, "dropping to shell")
	deadline := seen.Add(3 * time.Second)
	for {
		lines = s.lines()
		if p := s.promptRow(lines); p > exit {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no prompt after the exit line:\n%s", s.dump())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d := time.Since(seen); d > time.Second {
		t.Errorf("prompt took %s after the exit line, want under 1 s", d)
	}
	if st, err := h.cl.GetVM(context.Background(), name); err != nil || st.State != "running" {
		t.Errorf("state after exit → shell: %q err %v", st.State, err)
	}
}

// Console output while nobody is attached: console.log keeps growing and
// a later attach replays what was missed, then streams the rest, with
// nothing lost or repeated.
func TestConsoleOutputWhileDetached(t *testing.T) {
	h := need(t)
	name := h.vm(t, nil)
	const rows, cols = 24, 80
	h.start(t, name, &vsockproto.Session{Dir: "/home/dev", Rows: rows, Cols: cols, OnExit: "shell"})
	first := h.attach(t, name, cols, rows)
	first.waitPrompt(t, 15*time.Second)
	first.close()

	logPath := filepath.Join(store.Root{Dir: h.dir}.VMDir(name), "console.log")
	sizeAt := func() int64 {
		fi, err := os.Stat(logPath)
		if err != nil {
			t.Fatalf("console.log: %v", err)
		}
		return fi.Size()
	}
	before := sizeAt()
	if before == 0 {
		t.Fatal("console.log empty after boot")
	}
	// A writer to the console tty, run through exec so no terminal is
	// attached while it prints.
	const ticks = 20
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := h.cl.Exec(ctx, name, []string{"sh", "-c",
			fmt.Sprintf("i=1; while [ $i -le %d ]; do echo E2E-TICK-$i-END > /dev/hvc0; sleep 0.1; i=$((i+1)); done", ticks)})
		done <- err
	}()
	time.Sleep(800 * time.Millisecond)
	mid := sizeAt()
	if mid <= before {
		t.Errorf("console.log did not grow while detached: %d -> %d", before, mid)
	}
	second := h.attach(t, name, cols, rows)
	if err := <-done; err != nil {
		t.Fatalf("ticker exec: %v", err)
	}
	last := fmt.Sprintf("E2E-TICK-%d-END", ticks)
	second.waitText(t, last, 10*time.Second)
	// The replay covered the ticks printed while detached, and the live
	// stream the rest; every tick is on the grid exactly once.
	lines := second.lines()
	for i := 1; i <= ticks; i++ {
		if n := countRows(lines, fmt.Sprintf("E2E-TICK-%d-END", i)); n != 1 {
			t.Errorf("tick %d on the grid %d times", i, n)
		}
	}
	if !strings.Contains(second.rawText(), "E2E-TICK-1-END") {
		t.Error("reattach did not replay output printed while detached")
	}
	after := sizeAt()
	if after <= mid {
		t.Errorf("console.log stopped growing: %d -> %d", mid, after)
	}
	b, err := os.ReadFile(logPath) // #nosec G304 -- under the temp state dir
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= ticks; i++ {
		if n := strings.Count(string(b), fmt.Sprintf("E2E-TICK-%d-END", i)); n != 1 {
			t.Errorf("console.log has tick %d %d times", i, n)
		}
	}
}
