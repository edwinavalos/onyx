package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// egressPort is the host vsock port serving a restricted VM's egress proxy.
// Credential proxies number upwards from proxyPortBase, so this sits below.
const egressPort uint32 = 4900

// egressProxy is the only way out of a store.NetworkRestricted VM: an HTTP
// forward proxy (CONNECT for TLS, absolute-URI requests for plain HTTP)
// that admits destinations on the VM's allowlist and refuses everything
// else. The guest has no NIC, so nothing it does can route around this;
// the guest's own DNS never happens (CONNECT carries the hostname), which
// is what makes hostname allowlisting sound without terminating TLS.
type egressProxy struct {
	rules    []egressRule
	server   *http.Server
	listener net.Listener
	logf     func(verdict, target string)
}

// egressRule is one parsed allowlist entry.
type egressRule struct {
	host   string // exact host, or the suffix after "*." (must be a strict subdomain)
	suffix bool
	port   int // 0: 80 or 443
}

// parseEgressRules validates and parses allowlist entries.
func parseEgressRules(allow []string) ([]egressRule, error) {
	rules := make([]egressRule, 0, len(allow))
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		var r egressRule
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

// allows reports whether host:port is admitted by the rules.
func egressAllowed(rules []egressRule, host string, port int) bool {
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

// newEgressProxy builds the handler over rules; serving is the caller's job.
func newEgressProxy(rules []egressRule, logf func(verdict, target string)) *egressProxy {
	p := &egressProxy{rules: rules, logf: logf}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	return p
}

// egressDialTimeout bounds the outbound connect from the host.
const egressDialTimeout = 20 * time.Second

func (p *egressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	// Plain HTTP through a proxy arrives with an absolute URI.
	if !r.URL.IsAbs() {
		http.Error(w, "onyx egress: proxy requests only", http.StatusBadRequest)
		return
	}
	host, port := splitTarget(r.URL.Host, 80)
	target := net.JoinHostPort(host, strconv.Itoa(port))
	if !egressAllowed(p.rules, host, port) {
		p.logf("deny", target)
		http.Error(w, "onyx egress: "+target+" is not on this VM's allowlist", http.StatusForbidden)
		return
	}
	p.logf("allow", target)
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := egressTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, "onyx egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// egressTransport carries plain-HTTP proxied requests; it must not itself
// honour the host's proxy environment, which would re-enter this proxy.
var egressTransport = &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: egressDialTimeout}).DialContext,
	ResponseHeaderTimeout: 60 * time.Second,
	DisableCompression:    true,
}

func (p *egressProxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port := splitTarget(r.Host, 443)
	target := net.JoinHostPort(host, strconv.Itoa(port))
	if !egressAllowed(p.rules, host, port) {
		p.logf("deny", target)
		http.Error(w, "onyx egress: "+target+" is not on this VM's allowlist", http.StatusForbidden)
		return
	}
	p.logf("allow", target)
	upstream, err := (&net.Dialer{Timeout: egressDialTimeout}).DialContext(r.Context(), "tcp", target)
	if err != nil {
		http.Error(w, "onyx egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "onyx egress: cannot hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// Bytes the client sent after the CONNECT header, before we answered.
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Peek(n)
		if _, err := upstream.Write(pending); err != nil {
			return
		}
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); closeWrite(upstream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); closeWrite(client); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
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

// startEgress serves the VM's egress proxy on a host vsock port. Only
// restricted VMs get one; NAT VMs do not need it and none VMs must not
// have it.
func (c *Core) startEgress(inst *instance) (*egressProxy, error) {
	if inst.cfg.Network != store.NetworkRestricted {
		return nil, nil
	}
	rules, err := parseEgressRules(inst.cfg.Allow)
	if err != nil {
		return nil, err
	}
	p := newEgressProxy(rules, c.egressLogger(inst.cfg.Name))
	l, err := inst.machine.ListenHost(egressPort)
	if err != nil {
		return nil, fmt.Errorf("egress: listen vsock %d: %w", egressPort, err)
	}
	p.listener = l
	inst.proxyMu.Lock()
	inst.egress = p
	inst.proxyMu.Unlock()
	go func() {
		if err := p.server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("core: egress proxy stopped", "vm", inst.cfg.Name, "err", err)
		}
	}()
	slog.Info("core: egress proxy up", "vm", inst.cfg.Name, "allow", inst.cfg.Allow, "vsock_port", egressPort)
	return p, nil
}

// deliverEgress starts the egress proxy (restricted VMs) and tells the
// guest to route HTTP(S) through it.
func (c *Core) deliverEgress(inst *instance) error {
	p, err := c.startEgress(inst)
	if err != nil || p == nil {
		return err
	}
	if _, err := inst.call(vsockproto.Request{Op: "egress", Egress: &vsockproto.EgressItem{HostPort: egressPort}}); err != nil {
		return fmt.Errorf("deliver egress: %w", err)
	}
	return nil
}

func (p *egressProxy) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
	_ = p.listener.Close()
}

// egressLogger records every egress decision (verdict + host:port, nothing
// else) to egress.log. Denials are how the user discovers what to add to
// the allowlist.
func (c *Core) egressLogger(vmName string) func(verdict, target string) {
	var mu sync.Mutex
	path := filepath.Join(c.root.Dir, "egress.log")
	return func(verdict, target string) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- fixed path under the Onyx root
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = fmt.Fprintf(f, "%s vm=%s %s %s\n", time.Now().UTC().Format(time.RFC3339), vmName, verdict, target)
	}
}
