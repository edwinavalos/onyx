package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// timeline records how long each phase of an operation took and appends
// the result to <root>/metrics.jsonl, one JSON object per line, so that
// "starting feels slow" can be answered from history rather than by
// reproducing it. Phases are named in the order they complete.
type timeline struct {
	c      *Core
	event  string
	vm     string
	begin  time.Time
	last   time.Time
	order  []string
	phases map[string]int64
	extra  map[string]any
}

// metricsMu serialises appends from concurrent starts.
var metricsMu sync.Mutex

func (c *Core) newTimeline(event, vm string) *timeline {
	now := time.Now()
	return &timeline{c: c, event: event, vm: vm, begin: now, last: now, phases: map[string]int64{}, extra: map[string]any{}}
}

// mark closes the phase that just finished.
func (t *timeline) mark(phase string) {
	now := time.Now()
	t.phases[phase] = now.Sub(t.last).Milliseconds()
	t.order = append(t.order, phase)
	t.last = now
}

// set attaches an extra measurement (e.g. guest-reported boot time).
func (t *timeline) set(key string, v any) { t.extra[key] = v }

// done writes the record; errMsg empty means success.
func (t *timeline) done(errMsg string) {
	rec := map[string]any{
		"ts":        t.begin.UTC().Format(time.RFC3339Nano),
		"event":     t.event,
		"vm":        t.vm,
		"ok":        errMsg == "",
		"total_ms":  time.Since(t.begin).Milliseconds(),
		"phases_ms": t.phases,
		"order":     t.order,
	}
	if errMsg != "" {
		rec["error"] = errMsg
	}
	for k, v := range t.extra {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	attrs := []any{"vm", t.vm, "total_ms", rec["total_ms"]}
	for _, p := range t.order {
		attrs = append(attrs, p+"_ms", t.phases[p])
	}
	for k, v := range t.extra {
		attrs = append(attrs, k, v)
	}
	if errMsg != "" {
		attrs = append(attrs, "err", errMsg)
	}
	slog.Info("core: "+t.event+" timeline", attrs...)

	metricsMu.Lock()
	defer metricsMu.Unlock()
	f, err := os.OpenFile(filepath.Join(t.c.root.Dir, "metrics.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- fixed path under the Onyx root
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// parseUptimeMS converts /proc/uptime ("12.34 40.00") to milliseconds of
// guest uptime; 0 when unparsable.
func parseUptimeMS(s string) int64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return int64(v * 1000)
}
