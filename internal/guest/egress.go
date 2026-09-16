package guest

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// egressAddr is the loopback address the host's egress proxy is bridged to
// in a restricted VM. Fixed so tools configured on a persistent volume
// keep working across boots.
const egressAddr = "127.0.0.1:3128"

// CAFile is where the host's proxy CA is written (tmpfs) for tools with
// their own trust store; SystemBundle is the OpenSSL/Go bundle the CA is
// appended to so git, curl, gh and the rest trust the proxy's leaves.
var (
	CAFile       = "/run/onyx/ca.crt"
	SystemBundle = "/etc/ssl/certs/ca-certificates.crt"
)

var (
	egressMu sync.Mutex
	egressUp bool
)

// applyEgress bridges egressAddr to the host's forward proxy, points the
// standard proxy variables at it and installs the proxy's CA when the
// host sent one. Loopback stays direct so the credential proxies (also on
// loopback) are not sent through it.
func applyEgress(it *vsockproto.EgressItem) error {
	if it == nil {
		return fmt.Errorf("egress: missing body")
	}
	egressMu.Lock()
	defer egressMu.Unlock()
	if it.CAPEM != "" {
		envMu.Lock()
		err := installCA(it.CAPEM)
		envMu.Unlock()
		if err != nil {
			return err
		}
	}
	if !egressUp {
		l, err := net.Listen("tcp", egressAddr) //nolint:noctx // long-lived listener
		if err != nil {
			return fmt.Errorf("egress: listen %s: %w", egressAddr, err)
		}
		go bridge(l, vsockproto.ProxyItem{Name: "egress", HostPort: it.HostPort})
		egressUp = true
		slog.Info("guest: egress bridged", "local", egressAddr, "host_port", it.HostPort)
	}
	local := "http://" + egressAddr
	envMu.Lock()
	defer envMu.Unlock()
	for _, n := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY"} {
		envMap[n] = local
	}
	envMap["no_proxy"] = "127.0.0.1,localhost"
	envMap["NO_PROXY"] = envMap["no_proxy"]
	return writeEnvFile()
}

// installCA writes the proxy CA to CAFile, appends it to SystemBundle once
// (the root disk is this VM's own clone; the certificate is public) and
// sets the variables for tools that do not read the system bundle. Caller
// holds envMu.
func installCA(pem string) error {
	if err := os.MkdirAll(filepath.Dir(CAFile), envDirPerm); err != nil {
		return err
	}
	err := os.WriteFile(CAFile, []byte(pem), 0o644) // #nosec G306 -- public certificate, read by the work user
	if err != nil {
		return fmt.Errorf("write %s: %w", CAFile, err)
	}
	bundle, err := os.ReadFile(SystemBundle)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !bytes.Contains(bundle, []byte(pem)) {
		if len(bundle) > 0 && !bytes.HasSuffix(bundle, []byte("\n")) {
			bundle = append(bundle, '\n')
		}
		bundle = append(bundle, pem...)
		err := os.WriteFile(SystemBundle, bundle, 0o644) // #nosec G306 -- system trust bundle, world-readable by design
		if err != nil {
			return fmt.Errorf("append %s: %w", SystemBundle, err)
		}
	}
	envMap["NODE_EXTRA_CA_CERTS"] = CAFile
	envMap["SSL_CERT_FILE"] = SystemBundle
	envMap["REQUESTS_CA_BUNDLE"] = SystemBundle
	return nil
}
