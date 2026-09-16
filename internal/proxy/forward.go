package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"sync"
	"time"
)

// ForwardConfig describes one VM's forward proxy.
type ForwardConfig struct {
	CA     *CA
	Source SecretSource
	// Routes are intercepted: a CONNECT (or plain request) to a route's
	// upstream host is terminated here, the credential injected, and the
	// request forwarded to the real upstream.
	Routes []Route
	// Restricted refuses every destination that is neither intercepted nor
	// on Allow. Otherwise everything else is tunnelled untouched.
	Restricted bool
	Allow      []Rule
	// LogRequest sees every intercepted request (route, method + path).
	LogRequest func(route string, r *http.Request)
	// LogEgress sees every admit/refuse decision for tunnelled destinations.
	LogEgress func(verdict, target string)

	UpstreamTLS *tls.Config // trust for the real upstream; nil means system roots (tests)
}

// Forward is an HTTP forward proxy — the guest's HTTPS_PROXY. It is the
// only way out of a restricted VM and the way tools that hard-code an
// HTTPS host (gh → api.github.com) get their credential: the guest trusts
// the Onyx CA, the proxy terminates TLS for intercepted hosts and injects
// the real credential in place of the guest's placeholder. Every other
// destination is a plain CONNECT tunnel; the proxy never sees inside it.
type Forward struct {
	cfg        ForwardConfig
	intercepts map[string]*intercept // hostKey → handler
	tunnel     *http.Transport
}

type intercept struct {
	route Route
	host  string // bare host, for the leaf certificate
	rp    *httputil.ReverseProxy
}

// NewForward builds the handler; serving is the caller's job.
func NewForward(cfg ForwardConfig) *Forward {
	f := &Forward{cfg: cfg, intercepts: map[string]*intercept{}}
	f.tunnel = &http.Transport{
		Proxy:                 nil, // must not re-enter a host proxy for the guest's plain HTTP
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true,
	}
	for _, r := range cfg.Routes {
		key, err := r.hostKey()
		if err != nil {
			continue // validated by the pack; nothing to intercept
		}
		u, _ := r.upstreamURL()
		host, _ := splitTarget(u.Host, 443)
		route := r
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(u)
				pr.Out.Host = u.Host
				inject(route, cfg.Source, pr.Out)
				if cfg.LogRequest != nil {
					cfg.LogRequest(route.Name, pr.In)
				}
			},
			Transport:      upstreamTransport(cfg.UpstreamTLS),
			ModifyResponse: stripChallenge,
		}
		f.intercepts[key] = &intercept{route: route, host: host, rp: rp}
	}
	return f
}

const dialTimeout = 20 * time.Second

func (f *Forward) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		f.connect(w, r)
		return
	}
	// Plain HTTP through a proxy arrives with an absolute URI.
	if !r.URL.IsAbs() {
		http.Error(w, "onyx proxy: proxy requests only", http.StatusBadRequest)
		return
	}
	if ic := f.intercepts[hostKey(r.URL.Host, 80)]; ic != nil {
		ic.rp.ServeHTTP(w, r)
		return
	}
	host, port := splitTarget(r.URL.Host, 80)
	target := net.JoinHostPort(host, strconv.Itoa(port))
	if !f.admit(host, port, target) {
		http.Error(w, "onyx egress: "+target+" is not on this VM's allowlist", http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := f.tunnel.RoundTrip(out)
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

// admit decides for a non-intercepted destination and logs the verdict.
func (f *Forward) admit(host string, port int, target string) bool {
	ok := !f.cfg.Restricted || Allowed(f.cfg.Allow, host, port)
	if f.cfg.LogEgress != nil {
		verdict := "allow"
		if !ok {
			verdict = "deny"
		}
		f.cfg.LogEgress(verdict, target)
	}
	return ok
}

func (f *Forward) connect(w http.ResponseWriter, r *http.Request) {
	host, port := splitTarget(r.Host, 443)
	target := net.JoinHostPort(host, strconv.Itoa(port))
	ic := f.intercepts[hostKey(r.Host, 443)]
	if ic == nil && !f.admit(host, port, target) {
		http.Error(w, "onyx egress: "+target+" is not on this VM's allowlist", http.StatusForbidden)
		return
	}
	var upstream net.Conn
	if ic == nil {
		var err error
		upstream, err = (&net.Dialer{Timeout: dialTimeout}).DialContext(r.Context(), "tcp", target)
		if err != nil {
			http.Error(w, "onyx egress: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "onyx proxy: cannot hijack", http.StatusInternalServerError)
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
	if ic != nil {
		if f.cfg.LogEgress != nil {
			f.cfg.LogEgress("intercept", target)
		}
		f.serveIntercept(ic, prefixedConn{Conn: client, r: buf.Reader})
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

// serveIntercept terminates TLS on the tunnelled connection with a leaf
// for the route's host and serves the requests inside it through the
// route's injecting reverse proxy until the client closes.
func (f *Forward) serveIntercept(ic *intercept, client net.Conn) {
	tlsConn := tls.Server(client, f.cfg.CA.TLSConfig(ic.host))
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		return
	}
	srv := &http.Server{Handler: ic.rp, ReadHeaderTimeout: 30 * time.Second}
	l := &oneConnListener{conn: tlsConn, done: make(chan struct{})}
	_ = srv.Serve(l) // returns ErrServerClosed-like once the single conn is consumed
	<-l.closed()
}

// oneConnListener hands out a single connection and then blocks Accept
// until that connection is closed, so http.Server.Serve owns it fully.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = &notifyConn{Conn: l.conn, done: l.done} })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, errors.New("listener done")
}
func (l *oneConnListener) Close() error            { return nil }
func (l *oneConnListener) Addr() net.Addr          { return l.conn.LocalAddr() }
func (l *oneConnListener) closed() <-chan struct{} { return l.done }

// notifyConn closes done when the connection is closed.
type notifyConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

// prefixedConn replays bytes already buffered by the CONNECT reader.
type prefixedConn struct {
	net.Conn
	r io.Reader
}

func (p prefixedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
