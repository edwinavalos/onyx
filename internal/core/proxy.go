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
			auth, _ := pack.ParseAuth(s.Auth)
			// A re-delivery to a running VM: the proxy is already serving
			// this port; just tell the guest about it again.
			if existing := inst.proxyOn(port); existing != nil && existing.name == s.Key {
				items = append(items, vsockproto.ProxyItem{Name: s.Key, HostPort: port, Upstream: s.Upstream, Auth: auth.Kind})
				port++
				continue
			}
			// Fail early if the secret cannot be read at all; afterwards the
			// proxy re-reads it so linked/rotated values stay current.
			if _, err := keychain.Get(ctx, s.Key); err != nil {
				return nil, fmt.Errorf("pack %s: %w", pn, err)
			}
			cp, err := newCredProxy(inst, s, port, c.proxyLogger(inst.cfg.Name, s.Key))
			if err != nil {
				return nil, err
			}
			inst.proxyMu.Lock()
			inst.proxies = append(inst.proxies, cp)
			inst.proxyMu.Unlock()
			items = append(items, vsockproto.ProxyItem{Name: s.Key, HostPort: port, Upstream: s.Upstream, Auth: auth.Kind})
			c.audit(inst.cfg.Name, pn, s.Key, "proxy")
			port++
		}
	}
	return items, nil
}

// proxyOn returns the instance's credential proxy serving vsock port, if any.
func (i *instance) proxyOn(port uint32) *credProxy {
	i.proxyMu.Lock()
	defer i.proxyMu.Unlock()
	for _, p := range i.proxies {
		if p.port == port {
			return p
		}
	}
	return nil
}

// credCacheTTL bounds how often the proxy re-reads a secret from the
// Keychain. Short enough that a token refreshed by its owning app (Claude
// Code rotates its OAuth token) is picked up promptly.
const credCacheTTL = 30 * time.Second

// credSource yields the current secret value with a small cache.
type credSource struct {
	key string
	mu  sync.Mutex
	val string
	at  time.Time
}

func (c *credSource) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val != "" && time.Since(c.at) < credCacheTTL {
		return c.val, nil
	}
	v, err := keychain.Get(ctx, c.key)
	if err != nil {
		return "", err
	}
	c.val, c.at = v, time.Now()
	return v, nil
}

func newCredProxy(inst *instance, s pack.Secret, port uint32, logf func(*http.Request)) (*credProxy, error) {
	upstream, err := url.Parse(s.Upstream)
	if err != nil {
		return nil, err
	}
	auth, err := pack.ParseAuth(s.Auth)
	if err != nil {
		return nil, err
	}
	src := &credSource{key: s.Key}
	headerFor := func(value string) (string, string) {
		switch auth.Kind {
		case "bearer":
			return "Authorization", "Bearer " + value
		case "basic":
			return "Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte(auth.Arg+":"+value))
		default:
			return auth.Arg, value
		}
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Host = upstream.Host
			// The guest's placeholder credentials must never reach upstream,
			// whichever header it put them in.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("X-Forwarded-For")
			value, err := src.get(pr.In.Context())
			if err != nil {
				// Leave the request unauthenticated; ModifyResponse/ErrorHandler
				// turn the upstream 401 into a clear message.
				slog.Warn("core: proxy credential unavailable", "secret", s.Key, "err", err)
				pr.Out.Header.Set("X-Onyx-Credential-Error", err.Error())
				return
			}
			h, v := headerFor(value)
			pr.Out.Header.Del(h)
			pr.Out.Header.Set(h, v)
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
