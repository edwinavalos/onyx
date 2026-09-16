package proxy

// The core and the proxy process speak over a Unix socket. Every
// connection opens with one JSON line (a Hello); control operations get
// one JSON line back and the connection closes; a "serve" connection is
// then owned by the named handler and carries the guest's HTTP.

// Hello opens a connection from the core.
type Hello struct {
	Op string `json:"op"` // "configure", "remove", "serve", "ping"
	VM string `json:"vm,omitempty"`

	// configure
	Config *VMConfig `json:"config,omitempty"`

	// serve: which of the VM's handlers owns this connection.
	Kind  string `json:"kind,omitempty"` // "forward" or "reverse"
	Index int    `json:"index,omitempty"`
}

// VMConfig is everything the proxy needs for one VM.
type VMConfig struct {
	Routes     []Route  `json:"routes"`
	Restricted bool     `json:"restricted,omitempty"`
	Allow      []string `json:"allow,omitempty"`
}

// Reply answers a control Hello.
type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	CAPEM string `json:"ca_pem,omitempty"` // configure: what the guest should trust
}
