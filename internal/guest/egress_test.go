package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The forward proxy's CA lands on tmpfs, is appended to the system bundle
// exactly once, and the tools that keep their own trust store are pointed
// at it through the session environment.
func TestInstallCA(t *testing.T) {
	dir := t.TempDir()
	EnvFile = filepath.Join(dir, "env")
	CAFile = filepath.Join(dir, "ca.crt")
	SystemBundle = filepath.Join(dir, "ca-certificates.crt")
	if err := os.WriteFile(SystemBundle, []byte("-----BEGIN CERTIFICATE-----\nsystem\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pem := "-----BEGIN CERTIFICATE-----\nonyx\n-----END CERTIFICATE-----\n"
	envMu.Lock()
	envMap = map[string]string{}
	envMu.Unlock()
	t.Cleanup(func() { envMu.Lock(); envMap = map[string]string{}; envMu.Unlock() })

	envMu.Lock()
	err := installCA(pem)
	envMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	envMu.Lock()
	err = installCA(pem) // re-delivery after an agent restart
	envMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(CAFile)
	if string(got) != pem {
		t.Fatalf("ca.crt = %q", got)
	}
	bundle, _ := os.ReadFile(SystemBundle)
	if strings.Count(string(bundle), "onyx") != 1 || !strings.HasPrefix(string(bundle), "-----BEGIN CERTIFICATE-----\nsystem") {
		t.Fatalf("bundle = %q", bundle)
	}
	envMu.Lock()
	defer envMu.Unlock()
	if envMap["NODE_EXTRA_CA_CERTS"] != CAFile || envMap["REQUESTS_CA_BUNDLE"] != SystemBundle || envMap["SSL_CERT_FILE"] != SystemBundle {
		t.Fatalf("env = %v", envMap)
	}
}
