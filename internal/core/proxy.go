package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/proxy"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Credentials are proxied by a separate process (decisions.md D20). The
// core's part is plumbing: it tells the proxy process what each VM needs,
// listens on the VM's host vsock ports and splices every guest connection
// into the proxy's Unix socket. Nothing here reads a secret.

// egressPort is the host vsock port serving a VM's forward proxy (the
// guest's HTTPS_PROXY): the only way out of a restricted VM, and TLS
// interception for hosts named by proxy-mode secrets. Reverse proxies
// number upwards from proxyPortBase, so this sits below.
const egressPort uint32 = 4900

// proxyPortBase is the first host vsock port used for reverse proxies;
// each VM numbers its proxy-mode secrets from here in pack order.
const proxyPortBase uint32 = 5000

// hostListener is one vsock port the core splices into the proxy process.
type hostListener struct {
	name     string // secret key, or "forward"
	port     uint32
	listener net.Listener
}

func (l *hostListener) close() { _ = l.listener.Close() }

// proxyRoutes lists the proxy-mode secrets of packs in delivery order.
func (c *Core) proxyRoutes(packs []string) ([]proxy.Route, []string, error) {
	var routes []proxy.Route
	var owners []string
	for _, pn := range packs {
		p, err := c.Packs().Load(pn)
		if err != nil {
			return nil, nil, err
		}
		for _, s := range p.Secrets {
			if s.Mode != pack.ModeProxy {
				continue
			}
			routes = append(routes, proxy.Route{Name: s.Key, Key: s.Key, Upstream: s.Upstream, Auth: s.Auth})
			owners = append(owners, pn)
		}
	}
	return routes, owners, nil
}

// setupProxies configures the proxy process for the VM and makes sure a
// host listener is splicing every port it needs. It returns what the guest
// must be told: the reverse proxies to bridge and, when there is a forward
// proxy, where it is and which CA to trust. Calling it again for a running
// VM (re-delivery, a resume) reuses listeners that are already serving.
func (c *Core) setupProxies(ctx context.Context, inst *instance, packs []string) ([]vsockproto.ProxyItem, *vsockproto.EgressItem, error) {
	routes, owners, err := c.proxyRoutes(packs)
	if err != nil {
		return nil, nil, err
	}
	restricted := inst.cfg.Network == store.NetworkRestricted
	if len(routes) == 0 && !restricted {
		return nil, nil, nil
	}
	caPEM, err := c.proxy.Configure(ctx, inst.cfg.Name, proxy.VMConfig{Routes: routes, Restricted: restricted, Allow: inst.cfg.Allow})
	if err != nil {
		return nil, nil, fmt.Errorf("proxy: %w", err)
	}
	items := make([]vsockproto.ProxyItem, 0, len(routes))
	for i, r := range routes {
		port := proxyPortBase + uint32(i) // #nosec G115 -- pack sizes are tiny
		auth, _ := pack.ParseAuth(r.Auth)
		items = append(items, vsockproto.ProxyItem{Name: r.Name, HostPort: port, Upstream: r.Upstream, Auth: auth.Kind})
		if err := c.ensureListener(inst, r.Name, port, "reverse", i); err != nil {
			return nil, nil, err
		}
		c.audit(inst.cfg.Name, owners[i], r.Key, "proxy")
	}
	if err := c.ensureListener(inst, "forward", egressPort, "forward", 0); err != nil {
		return nil, nil, err
	}
	slog.Info("core: proxies up", "vm", inst.cfg.Name, "routes", len(routes), "restricted", restricted)
	return items, &vsockproto.EgressItem{HostPort: egressPort, CAPEM: string(caPEM)}, nil
}

// ensureListener splices vsock port into the named proxy handler unless a
// listener for the same secret is already there. A different secret on a
// port that is serving (packs changed between deliveries) replaces it.
func (c *Core) ensureListener(inst *instance, name string, port uint32, kind string, index int) error {
	inst.proxyMu.Lock()
	defer inst.proxyMu.Unlock()
	for i, l := range inst.listeners {
		if l.port != port {
			continue
		}
		if l.name == name {
			return nil
		}
		l.close()
		inst.listeners = append(inst.listeners[:i], inst.listeners[i+1:]...)
		break
	}
	l, err := inst.machine.ListenHost(port)
	if err != nil {
		return fmt.Errorf("proxy %s: listen vsock %d: %w", name, port, err)
	}
	inst.listeners = append(inst.listeners, &hostListener{name: name, port: port, listener: l})
	go c.proxy.ServeListener(context.Background(), l, inst.cfg.Name, kind, index)
	return nil
}

// closeProxies drops the VM's host listeners and its proxy configuration.
func (c *Core) closeProxies(inst *instance) {
	inst.proxyMu.Lock()
	for _, l := range inst.listeners {
		l.close()
	}
	had := len(inst.listeners) > 0
	inst.listeners = nil
	inst.proxyMu.Unlock()
	if had {
		if err := c.proxy.Remove(context.Background(), inst.cfg.Name); err != nil {
			slog.Debug("core: proxy remove", "vm", inst.cfg.Name, "err", err)
		}
	}
}

// deliverProxies sets the proxies up and tells the guest: the forward
// proxy (HTTPS_PROXY and the CA) first, then the reverse proxies to
// bridge on loopback.
func (c *Core) deliverProxies(ctx context.Context, inst *instance, packs []string) error {
	items, egress, err := c.setupProxies(ctx, inst, packs)
	if err != nil {
		return err
	}
	if egress != nil {
		if _, err := inst.call(vsockproto.Request{Op: "egress", Egress: egress}); err != nil {
			return fmt.Errorf("deliver egress: %w", err)
		}
	}
	if len(items) > 0 {
		if _, err := inst.call(vsockproto.Request{Op: "proxies", Proxies: items}); err != nil {
			return fmt.Errorf("deliver proxies: %w", err)
		}
	}
	return nil
}
