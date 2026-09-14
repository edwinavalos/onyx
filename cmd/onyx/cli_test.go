package main

import (
	"flag"
	"reflect"
	"testing"

	"github.com/edwinavalos/onyx/internal/pack"
)

func TestParseInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	n := fs.Int("n", 0, "")
	var vols volumeFlags
	fs.Var(&vols, "volume", "")
	pos, err := parseInterspersed(fs, []string{"first", "-n", "3", "second", "-volume", "work:/w", "-volume", "s:/s"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"first", "second"}) || *n != 3 {
		t.Fatalf("pos=%v n=%d", pos, *n)
	}
	want := volumeFlags{{Volume: "work", Target: "/w"}, {Volume: "s", Target: "/s"}}
	if !reflect.DeepEqual(vols, want) {
		t.Fatalf("vols = %+v", vols)
	}
}

func TestVolumeFlagRejectsMissingTarget(t *testing.T) {
	var v volumeFlags
	if err := v.Set("work"); err == nil {
		t.Fatal("accepted volume without target")
	}
}

func TestSecretFlagForms(t *testing.T) {
	var s secretFlags
	for _, spec := range []string{"gh-token", "gh-token=GH_TOKEN", "ssh-key@/run/onyx/id:0400", "cfg@/run/onyx/cfg", "gh>https://github.com", "api>https://api.example.com>bearer"} {
		if err := s.Set(spec); err != nil {
			t.Fatal(err)
		}
	}
	want := secretFlags{
		{Key: "gh-token", Mode: pack.ModeEnv},
		{Key: "gh-token", Mode: pack.ModeEnv, Name: "GH_TOKEN"},
		{Key: "ssh-key", Mode: pack.ModeFile, Path: "/run/onyx/id", Perm: "0400"},
		{Key: "cfg", Mode: pack.ModeFile, Path: "/run/onyx/cfg"},
		{Key: "gh", Mode: pack.ModeProxy, Upstream: "https://github.com"},
		{Key: "api", Mode: pack.ModeProxy, Upstream: "https://api.example.com", Auth: "bearer"},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
}

func TestSecretFlagsSupportMultiSecretPacks(t *testing.T) {
	var s secretFlags
	for _, spec := range []string{"agent-token=AGENT_TOKEN", "tool-token@/run/onyx/tools/token:0600"} {
		if err := s.Set(spec); err != nil {
			t.Fatal(err)
		}
	}
	if len(s) != 2 || s[0].Key != "agent-token" || s[1].Key != "tool-token" {
		t.Fatalf("multi-secret flags = %+v", s)
	}
}

// `onyx run` mounts the work volume at /home/dev/work/<volume> and starts
// the session there; an explicit -dir still wins (issue #2).
func TestRunSessionPaths(t *testing.T) {
	work, dir := sessionPaths("s1", "", "")
	if work != "s1-work" || dir != "/home/dev/work/s1-work" {
		t.Errorf("defaults: work=%q dir=%q", work, dir)
	}
	if work, dir := sessionPaths("s1", "proj", ""); work != "proj" || dir != "/home/dev/work/proj" {
		t.Errorf("named work volume: work=%q dir=%q", work, dir)
	}
	if _, dir := sessionPaths("s1", "proj", "/tmp/x"); dir != "/tmp/x" {
		t.Errorf("explicit dir: %q", dir)
	}
}
