package proxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Rule is one parsed egress allowlist entry (restricted VMs).
type Rule struct {
	host   string // exact host, or the suffix after "*." (must be a strict subdomain)
	suffix bool
	port   int // 0: 80 or 443
}

// ParseAllow validates and parses allowlist entries: host, *.suffix or
// host:port.
func ParseAllow(allow []string) ([]Rule, error) {
	rules := make([]Rule, 0, len(allow))
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		var r Rule
		host, port, err := net.SplitHostPort(a)
		if err == nil {
			r.port, err = strconv.Atoi(port)
			if err != nil || r.port < 1 || r.port > 65535 {
				return nil, fmt.Errorf("allow %q: bad port", a)
			}
		} else {
			host = a
		}
		if strings.HasPrefix(host, "*.") {
			r.suffix = true
			host = host[2:]
		}
		if host == "" || strings.ContainsAny(host, "*/ ") {
			return nil, fmt.Errorf("allow %q: want host, *.suffix or host:port", a)
		}
		r.host = host
		rules = append(rules, r)
	}
	return rules, nil
}

// Allowed reports whether host:port is admitted by the rules.
func Allowed(rules []Rule, host string, port int) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, r := range rules {
		if r.port == 0 && port != 80 && port != 443 {
			continue
		}
		if r.port != 0 && port != r.port {
			continue
		}
		if r.suffix {
			if strings.HasSuffix(host, "."+r.host) {
				return true
			}
			continue
		}
		if host == r.host {
			return true
		}
	}
	return false
}

// splitTarget separates host and port, defaulting the port.
func splitTarget(hostport string, def int) (string, int) {
	host, ps, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport, def
	}
	port, err := strconv.Atoi(ps)
	if err != nil {
		return host, def
	}
	return host, port
}
