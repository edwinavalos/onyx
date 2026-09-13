package main

import (
	"strings"
	"testing"
)

func TestFormatMetrics(t *testing.T) {
	lines := []string{
		`{"ts":"2026-09-13T00:09:00Z","event":"start","vm":"a","ok":true,"total_ms":3018,"guest_boot_ms":2780,"order":["console","agent","packs"],"phases_ms":{"console":1,"agent":2841,"packs":1}}`,
		`{"ts":"2026-09-13T00:15:00Z","event":"start","vm":"b","ok":false,"error":"boom","total_ms":90000,"order":["console"],"phases_ms":{"console":2}}`,
		`not json`,
	}
	out := formatMetrics(strings.Join(lines, "\n"), 10)
	for _, want := range []string{"WHEN", "VM", "TOTAL", "GUEST", "SLOWEST", "a", "3.018s", "2.780s", "agent 2.841s", "b", "FAILED boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not json") {
		t.Error("bad line should be skipped")
	}
	// The last N only, newest last.
	out = formatMetrics(strings.Join(lines, "\n"), 1)
	if strings.Contains(out, "\ta\t") || !strings.Contains(out, "FAILED") {
		t.Errorf("limit: %s", out)
	}
	// A summary line gives the median so drift is visible at a glance.
	out = formatMetrics(strings.Join(lines[:1], "\n"), 10)
	if !strings.Contains(out, "median 3.018s") {
		t.Errorf("no median: %s", out)
	}
}
