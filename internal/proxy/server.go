package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Server is the proxy process: it owns the CA and the only handle on
// proxied credentials, and serves each VM's forward and reverse proxies on
// connections the core hands it over the Unix socket.
type Server struct {
	CA     *CA
	Source SecretSource
	// LogDir receives proxy.log (intercepted/reverse requests) and
	// egress.log (tunnel verdicts). Empty disables both.
	LogDir string
	// UpstreamTLS is trust for real upstreams; nil means system roots.
	UpstreamTLS *tls.Config

	mu  sync.Mutex
	vms map[string]*vmProxies
}

type vmProxies struct {
	forward  http.Handler
	reverses []http.Handler
}

// Serve accepts connections on l until ctx ends.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	go func() { <-ctx.Done(); _ = l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	r := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := r.ReadBytes('\n')
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	var h Hello
	if err := json.Unmarshal(line, &h); err != nil {
		reply(c, Reply{Error: "bad hello: " + err.Error()})
		_ = c.Close()
		return
	}
	switch h.Op {
	case "ping":
		reply(c, Reply{OK: true})
		_ = c.Close()
	case "configure":
		err := s.configure(h.VM, h.Config)
		if err != nil {
			reply(c, Reply{Error: err.Error()})
		} else {
			reply(c, Reply{OK: true, CAPEM: string(s.CA.PEM())})
		}
		_ = c.Close()
	case "remove":
		s.mu.Lock()
		delete(s.vms, h.VM)
		s.mu.Unlock()
		reply(c, Reply{OK: true})
		_ = c.Close()
	case "serve":
		handler, err := s.handlerFor(h)
		if err != nil {
			// The core dialled for a handler that is not configured; say so
			// in-band as HTTP so the guest's tool sees an error, not a hang.
			handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "onyx proxy: "+err.Error(), http.StatusBadGateway)
			})
		}
		serveConn(handler, prefixedConn{Conn: c, r: r})
	default:
		reply(c, Reply{Error: fmt.Sprintf("unknown op %q", h.Op)})
		_ = c.Close()
	}
}

func reply(c net.Conn, r Reply) {
	b, _ := json.Marshal(r)
	_, _ = c.Write(append(b, '\n'))
}

// configure (re)builds a VM's handlers. Reconfiguring a VM that is already
// served replaces the handlers; connections in flight finish on the old
// ones.
func (s *Server) configure(vm string, cfg *VMConfig) error {
	if vm == "" || cfg == nil {
		return errors.New("configure: vm and config required")
	}
	rules, err := ParseAllow(cfg.Allow)
	if err != nil {
		return err
	}
	// Fail the VM start early if a credential cannot be read at all;
	// afterwards each request re-reads it so rotated values stay current.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, r := range cfg.Routes {
		if _, err := r.upstreamURL(); err != nil {
			return err
		}
		if _, err := s.Source.Get(ctx, r.Key); err != nil {
			return fmt.Errorf("secret %q: %w", r.Key, err)
		}
	}
	src := s.Source
	p := &vmProxies{}
	p.forward = NewForward(ForwardConfig{
		CA: s.CA, Source: src, Routes: cfg.Routes, Restricted: cfg.Restricted, Allow: rules,
		LogRequest:  s.requestLogger(vm),
		LogEgress:   s.egressLogger(vm),
		UpstreamTLS: s.UpstreamTLS,
	})
	logReq := s.requestLogger(vm)
	for _, r := range cfg.Routes {
		route := r
		var logf func(*http.Request)
		if logReq != nil {
			logf = func(req *http.Request) { logReq(route.Name, req) }
		}
		p.reverses = append(p.reverses, NewReverse(route, src, logf, s.UpstreamTLS))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vms == nil {
		s.vms = map[string]*vmProxies{}
	}
	s.vms[vm] = p
	slog.Info("proxy: vm configured", "vm", vm, "routes", len(cfg.Routes), "restricted", cfg.Restricted)
	return nil
}

func (s *Server) handlerFor(h Hello) (http.Handler, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.vms[h.VM]
	if p == nil {
		return nil, fmt.Errorf("vm %q is not configured", h.VM)
	}
	switch h.Kind {
	case "forward":
		return p.forward, nil
	case "reverse":
		if h.Index < 0 || h.Index >= len(p.reverses) {
			return nil, fmt.Errorf("vm %q has no reverse proxy %d", h.VM, h.Index)
		}
		return p.reverses[h.Index], nil
	}
	return nil, fmt.Errorf("unknown handler kind %q", h.Kind)
}

// serveConn runs handler over a single already-accepted connection until
// the client closes it.
func serveConn(handler http.Handler, c net.Conn) {
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	l := &oneConnListener{conn: c, done: make(chan struct{})}
	_ = srv.Serve(l)
	<-l.closed()
}

// requestLogger records every credentialed request (method + path, no
// bodies, no headers) so the user can see what the agent did with their
// credential.
func (s *Server) requestLogger(vm string) func(route string, r *http.Request) {
	if s.LogDir == "" {
		return nil
	}
	return func(route string, r *http.Request) {
		s.appendLog("proxy.log", fmt.Sprintf("vm=%s secret=%s %s %s", vm, route, r.Method, r.URL.RequestURI()))
	}
}

// egressLogger records verdicts (allow/deny/intercept + host:port).
// Denials are how the user discovers what to add to the allowlist.
func (s *Server) egressLogger(vm string) func(verdict, target string) {
	if s.LogDir == "" {
		return nil
	}
	return func(verdict, target string) {
		s.appendLog("egress.log", fmt.Sprintf("vm=%s %s %s", vm, verdict, target))
	}
}

var logMu sync.Mutex

func (s *Server) appendLog(name, line string) {
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.LogDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- fixed names under the Onyx root
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}
