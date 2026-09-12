package pack

import (
	"errors"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		p    Pack
		ok   bool
	}{
		{"env default name", Pack{Name: "p", Secrets: []Secret{{Key: "gh-token", Mode: ModeEnv}}}, false}, // "gh-token" is not a valid env name
		{"env explicit name", Pack{Name: "p", Secrets: []Secret{{Key: "gh-token", Mode: ModeEnv, Name: "GH_TOKEN"}}}, true},
		{"file abs", Pack{Name: "p", Secrets: []Secret{{Key: "k", Mode: ModeFile, Path: "/run/onyx/x"}}}, true},
		{"file rel", Pack{Name: "p", Secrets: []Secret{{Key: "k", Mode: ModeFile, Path: "x"}}}, false},
		{"proxy unimplemented", Pack{Name: "p", Secrets: []Secret{{Key: "k", Mode: ModeProxy}}}, false},
		{"bad mode", Pack{Name: "p", Secrets: []Secret{{Key: "k", Mode: "magic"}}}, false},
		{"bad name", Pack{Name: "../p", Secrets: nil}, false},
		{"empty ok", Pack{Name: "empty"}, true},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if names, err := s.List(); err != nil || len(names) != 0 {
		t.Fatalf("empty List = %v, %v", names, err)
	}
	p := Pack{Name: "claude", Secrets: []Secret{{Key: "claude-oauth-token", Mode: ModeEnv, Name: "CLAUDE_CODE_OAUTH_TOKEN"}}}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != p.Name || len(got.Secrets) != 1 || got.Secrets[0].Name != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("Load = %+v", got)
	}
	names, err := s.List()
	if err != nil || len(names) != 1 || names[0] != "claude" {
		t.Fatalf("List = %v, %v", names, err)
	}
	if err := s.Delete("claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("claude"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete("claude"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double Delete = %v, want ErrNotFound", err)
	}
}
