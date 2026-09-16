package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

// Every VM start records how long each phase took, so slowness is
// visible after the fact (metrics.jsonl) and not only while it happens.
func TestStartTimelineRecordsPhases(t *testing.T) {
	root := store.Root{Dir: t.TempDir()}
	t.Setenv("ONYX_PROXY_INPROC", "1") // no child process under go test
	c, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	tl := c.newTimeline("start", "vm1")
	tl.mark("console")
	time.Sleep(2 * time.Millisecond)
	tl.mark("machine")
	tl.set("guest_boot_ms", 1234)
	tl.done("")

	b, err := os.ReadFile(filepath.Join(root.Dir, "metrics.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(b))
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("want one line, got %q", line)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["event"] != "start" || rec["vm"] != "vm1" || rec["ok"] != true {
		t.Errorf("record: %v", rec)
	}
	phases, _ := rec["phases_ms"].(map[string]any)
	if _, ok := phases["console"]; !ok {
		t.Errorf("phase console missing: %v", rec)
	}
	if m, _ := phases["machine"].(float64); m < 2 {
		t.Errorf("machine phase = %v ms, want >= 2", m)
	}
	if rec["guest_boot_ms"] != 1234.0 {
		t.Errorf("guest_boot_ms = %v", rec["guest_boot_ms"])
	}
	if total, _ := rec["total_ms"].(float64); total < 2 {
		t.Errorf("total_ms = %v", total)
	}

	// A failed start records the error and which phase it died in.
	tl2 := c.newTimeline("start", "vm1")
	tl2.mark("console")
	tl2.done("boom")
	b, _ = os.ReadFile(filepath.Join(root.Dir, "metrics.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"ok":false`) || !strings.Contains(lines[1], `"error":"boom"`) {
		t.Errorf("failure record: %v", lines)
	}
}

// parseUptime reads the guest's /proc/uptime first field as milliseconds.
func TestParseUptimeMS(t *testing.T) {
	if got := parseUptimeMS("12.34 40.00\n"); got != 12340 {
		t.Errorf("got %d", got)
	}
	if got := parseUptimeMS("garbage"); got != 0 {
		t.Errorf("garbage: %d", got)
	}
}
