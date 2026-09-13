package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

// runMetrics implements `onyx metrics`: the recent start timelines the
// core appended to metrics.jsonl, so "is starting getting slower?" has a
// number.
func runMetrics(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	n := fs.Int("n", 20, "how many recent records to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := store.Default()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root.Dir, "metrics.jsonl")) // #nosec G304 -- fixed path under the Onyx root
	if os.IsNotExist(err) {
		fmt.Println("no metrics yet: start a VM")
		return nil
	}
	if err != nil {
		return err
	}
	out, err := formatMetrics(string(b), *n)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

type metricRecord struct {
	TS        time.Time        `json:"ts"`
	Event     string           `json:"event"`
	VM        string           `json:"vm"`
	OK        bool             `json:"ok"`
	Error     string           `json:"error"`
	TotalMS   int64            `json:"total_ms"`
	GuestBoot int64            `json:"guest_boot_ms"`
	Order     []string         `json:"order"`
	Phases    map[string]int64 `json:"phases_ms"`
}

// formatMetrics renders the last n records as a table plus a median line.
func formatMetrics(jsonl string, n int) (string, error) {
	var recs []metricRecord
	for _, line := range strings.Split(jsonl, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r metricRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // a torn line must not hide the rest
		}
		recs = append(recs, r)
	}
	if len(recs) > n {
		recs = recs[len(recs)-n:]
	}
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WHEN\tEVENT\tVM\tTOTAL\tGUEST BOOT\tSLOWEST PHASE")
	var oks []int64
	for _, r := range recs {
		slowest, slowestMS := "", int64(-1)
		for p, ms := range r.Phases {
			if ms > slowestMS {
				slowest, slowestMS = p, ms
			}
		}
		guest := "-"
		if r.GuestBoot > 0 {
			guest = secs(r.GuestBoot)
		}
		last := fmt.Sprintf("%s %s", slowest, secs(slowestMS))
		if !r.OK {
			last = "FAILED " + r.Error
		} else {
			oks = append(oks, r.TotalMS)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.TS.Local().Format("01-02 15:04:05"), r.Event, r.VM, secs(r.TotalMS), guest, last)
	}
	_ = tw.Flush()
	if len(oks) > 0 {
		sort.Slice(oks, func(i, j int) bool { return oks[i] < oks[j] })
		fmt.Fprintf(&sb, "%d ok, median %s, max %s\n", len(oks), secs(oks[len(oks)/2]), secs(oks[len(oks)-1]))
	}
	return sb.String(), nil
}

func secs(ms int64) string { return fmt.Sprintf("%.3fs", float64(ms)/1000) }
