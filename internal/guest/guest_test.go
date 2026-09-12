package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

func TestApplySecretsEnvAndFile(t *testing.T) {
	dir := t.TempDir()
	EnvFile = filepath.Join(dir, "env")
	t.Cleanup(func() { envMap = map[string]string{} })

	keyPath := filepath.Join(dir, "keys", "id_test")
	err := applySecrets([]vsockproto.SecretItem{
		{Mode: "env", Name: "TOKEN", Value: "it's-a-secret"},
		{Mode: "env", Name: "ANOTHER", Value: "x"},
		{Mode: "file", Path: keyPath, Perm: 0o600, Value: "KEYDATA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(EnvFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "export ANOTHER='x'\nexport TOKEN='it'\\''s-a-secret'\n"
	if string(b) != want {
		t.Fatalf("env file = %q, want %q", b, want)
	}
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key perm = %o", st.Mode().Perm())
	}
	env := envForExec()
	if !contains(env, "TOKEN=it's-a-secret") {
		t.Fatalf("envForExec missing TOKEN: %v", env[len(env)-3:])
	}

	// Redelivery replaces, does not duplicate.
	if err := applySecrets([]vsockproto.SecretItem{{Mode: "env", Name: "TOKEN", Value: "v2"}}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(EnvFile)
	if strings.Count(string(b), "TOKEN=") != 1 || !strings.Contains(string(b), "'v2'") {
		t.Fatalf("env after redelivery = %q", b)
	}
}

func TestApplySecretsRejectsBadItems(t *testing.T) {
	EnvFile = filepath.Join(t.TempDir(), "env")
	t.Cleanup(func() { envMap = map[string]string{} })
	if err := applySecrets([]vsockproto.SecretItem{{Mode: "env", Value: "x"}}); err == nil {
		t.Error("env without name accepted")
	}
	if err := applySecrets([]vsockproto.SecretItem{{Mode: "file", Path: "relative", Value: "x"}}); err == nil {
		t.Error("relative file path accepted")
	}
	if err := applySecrets([]vsockproto.SecretItem{{Mode: "carrier-pigeon", Value: "x"}}); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestWriteSession(t *testing.T) {
	SessionFile = filepath.Join(t.TempDir(), "session")
	if err := writeSession(vsockproto.Session{Dir: "/home/dev/work", Cmd: "claude --model 'x'", Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "ONYX_SESSION_DIR='/home/dev/work'\nONYX_SESSION_CMD='claude --model '\\''x'\\'''\nONYX_ROWS=40\nONYX_COLS=120\n"
	if string(b) != want {
		t.Fatalf("session = %q\nwant      %q", b, want)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
