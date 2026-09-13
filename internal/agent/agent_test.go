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
	if claude.Command() != "claude" || codex.Command() != "codex" {
		t.Fatalf("commands: claude=%q codex=%q", claude.Command(), codex.Command())
	}
	if claude.StateDir() == codex.StateDir() || claude.StateVolume() == codex.StateVolume() {
		t.Fatalf("adapters share state: claude=%q/%q codex=%q/%q", claude.StateDir(), claude.StateVolume(), codex.StateDir(), codex.StateVolume())
	}
}

func TestLookupRejectsUnknownAgent(t *testing.T) {
	if _, err := Lookup("gemini"); err == nil {
		t.Fatal("Lookup accepted unknown agent")
	}
}
