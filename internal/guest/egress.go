package guest

import (
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// egressAddr is the loopback address the host's egress proxy is bridged to
// in a restricted VM. Fixed so tools configured on a persistent volume
// keep working across boots.
const egressAddr = "127.0.0.1:3128"

var (
	egressMu sync.Mutex
	egressUp bool
)

// applyEgress bridges egressAddr to the host's egress proxy and points the
// standard proxy variables at it. Loopback stays direct so the credential
// proxies (also on loopback) are not sent through it.
func applyEgress(it *vsockproto.EgressItem) error {
	if it == nil {
		return fmt.Errorf("egress: missing body")
	}
	egressMu.Lock()
	defer egressMu.Unlock()
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
