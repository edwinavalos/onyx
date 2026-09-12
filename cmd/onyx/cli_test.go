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
