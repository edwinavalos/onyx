package agent

import "testing"

func TestBuiltInAdaptersHaveDistinctState(t *testing.T) {
	claude, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	codex, err := Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	pi, err := Lookup("pi")
	if err != nil {
		t.Fatal(err)
	}
	if claude.Command() != "claude" || codex.Command() != "codex" || pi.Command() != "pi" {
		t.Fatalf("commands: claude=%q codex=%q pi=%q", claude.Command(), codex.Command(), pi.Command())
	}
	if claude.StateDir() == codex.StateDir() || claude.StateDir() == pi.StateDir() || codex.StateDir() == pi.StateDir() || claude.StateVolume() == codex.StateVolume() || claude.StateVolume() == pi.StateVolume() || codex.StateVolume() == pi.StateVolume() {
		t.Fatalf("adapters share state: claude=%q/%q codex=%q/%q pi=%q/%q", claude.StateDir(), claude.StateVolume(), codex.StateDir(), codex.StateVolume(), pi.StateDir(), pi.StateVolume())
	}
}

func TestLookupRejectsUnknownAgent(t *testing.T) {
	if _, err := Lookup("gemini"); err == nil {
		t.Fatal("Lookup accepted unknown agent")
	}
}
