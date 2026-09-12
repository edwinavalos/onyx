package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/edwinavalos/onyx/internal/keychain"
	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// credProxy is one host-side reverse proxy: requests arriving over vsock
// from the guest are forwarded to Upstream with the credential attached.
// The credential is read from the Keychain once at start and lives only in
// this process.
type credProxy struct {
	name     string
	upstream *url.URL
	port     uint32
	server   *http.Server
	listener net.Listener
}

// proxyPortBase is the first host vsock port used for proxies; each VM
// numbers its proxies from here.
const proxyPortBase uint32 = 5000

// startProxies builds and serves a proxy for every proxy-mode secret in
// the VM's packs and returns what the guest needs to bridge them.
func (c *Core) startProxies(ctx context.Context, inst *instance, packs []string) ([]vsockproto.ProxyItem, error) {
	var items []vsockproto.ProxyItem
	port := proxyPortBase
	for _, pn := range packs {
		p, err := c.Packs().Load(pn)
		if err != nil {
			return nil, err
		}
		for _, s := range p.Secrets {
			if s.Mode != pack.ModeProxy {
				continue
			}
			value, err := keychain.Get(ctx, s.Key)
			if err != nil {
				return nil, fmt.Errorf("pack %s: %w", pn, err)
			}
			cp, err := newCredProxy(inst, s, value, port, c.proxyLogger(inst.cfg.Name, s.Key))
			if err != nil {
				return nil, err
			}
			inst.proxyMu.Lock()
			inst.proxies = append(inst.proxies, cp)
			inst.proxyMu.Unlock()
			items = append(items, vsockproto.ProxyItem{Name: s.Key, HostPort: port, Upstream: s.Upstream})
			c.audit(inst.cfg.Name, pn, s.Key, "proxy")
			port++
		}
	}
	return items, nil
}

func newCredProxy(inst *instance, s pack.Secret, value string, port uint32, logf func(*http.Request)) (*credProxy, error) {
	upstream, err := url.Parse(s.Upstream)
	if err != nil {
		return nil, err
	}
	auth, err := pack.ParseAuth(s.Auth)
	if err != nil {
		return nil, err
	}
	header, headerValue := "Authorization", ""
	switch auth.Kind {
	case "bearer":
		headerValue = "Bearer " + value
	case "basic":
		headerValue = "Basic " + base64.StdEncoding.EncodeToString([]byte(auth.Arg+":"+value))
	case "header":
		header, headerValue = auth.Arg, value
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Host = upstream.Host
			// Never let the guest smuggle its own credential header through,
			// and never forward hop-by-hop identity from the loopback side.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Set(header, headerValue)
			logf(pr.In)
		},
		ErrorLog: nil,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ModifyResponse: func(resp *http.Response) error {
			// Upstream auth challenges would prompt git for a password the
			// guest doesn't have; make the failure explicit instead.
			if resp.StatusCode == http.StatusUnauthorized {
				resp.Header.Del("WWW-Authenticate")
			}
			return nil
		},
	}

	l, err := inst.machine.ListenHost(port)
	if err != nil {
		return nil, fmt.Errorf("proxy %s: listen vsock %d: %w", s.Key, port, err)
	}
	srv := &http.Server{Handler: rp, ReadHeaderTimeout: 30 * time.Second}
	cp := &credProxy{name: s.Key, upstream: upstream, port: port, server: srv, listener: l}
	go func() {
		if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
			slog.Warn("core: proxy stopped", "vm", inst.cfg.Name, "secret", s.Key, "err", err)
		}
	}()
	slog.Info("core: credential proxy up", "vm", inst.cfg.Name, "secret", s.Key, "upstream", s.Upstream, "vsock_port", port)
	return cp, nil
}

func (p *credProxy) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
	_ = p.listener.Close()
}

// proxyLogger records every proxied request (method + path, no bodies, no
// headers) to proxy.log so the user can see what the agent did with their
// credential.
func (c *Core) proxyLogger(vmName, secret string) func(*http.Request) {
	var mu sync.Mutex
	path := filepath.Join(c.root.Dir, "proxy.log")
	return func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- fixed path under the Onyx root
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = fmt.Fprintf(f, "%s vm=%s secret=%s %s %s\n", time.Now().UTC().Format(time.RFC3339), vmName, secret, r.Method, r.URL.RequestURI())
	}
}
