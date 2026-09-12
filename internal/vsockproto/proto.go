// Package vsockproto defines the line-oriented protocol spoken between the
// host daemon and the guest agent over vsock. It is deliberately tiny for
// the spike: newline-delimited JSON, one request per line, one response per
// line.
package vsockproto

// Port is the vsock port the guest agent listens on.
const Port uint32 = 4242

// Request is sent host → guest.
type Request struct {
	// Op is the operation name: "ping", "mount", "exec", "secrets",
	// "session", "winsize", "proxies".
	Op string `json:"op"`

	// Mount: block device to format-if-needed and mount.
	Device string `json:"device,omitempty"`
	Target string `json:"target,omitempty"`

	// Exec: command to run.
	Argv []string `json:"argv,omitempty"`

	// Secrets: values to place in the guest (tmpfs only).
	Secrets []SecretItem `json:"secrets,omitempty"`

	// Session: what the console login should run (see guest.SessionFile).
	Session *Session `json:"session,omitempty"`

	// Proxies: host-side credential proxies to expose on loopback.
	Proxies []ProxyItem `json:"proxies,omitempty"`

	// Winsize: terminal size for the console.
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

// ProxyItem tells the guest to bridge a loopback TCP port to a host vsock
// port on which the host serves a credential-injecting proxy for Upstream.
type ProxyItem struct {
	Name     string `json:"name"`      // pack secret key, for logging
	HostPort uint32 `json:"host_port"` // vsock port on the host (CID 2)
	Upstream string `json:"upstream"`  // e.g. https://github.com
	Auth     string `json:"auth"`      // "bearer", "basic", or "header" — how the host authenticates upstream
}

// Session describes the interactive session the console should start.
type Session struct {
	Dir  string `json:"dir"`
	Cmd  string `json:"cmd"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	// OnExit is what the console does after Cmd exits: "shell" (default)
	// drops to an interactive shell, "poweroff" shuts the VM down.
	OnExit string `json:"on_exit,omitempty"`
}

// SecretItem is one delivered secret. Values never touch the guest's disk:
// env entries go to /run/onyx/env, files to their path (which should itself
// be on tmpfs unless the user chooses otherwise).
type SecretItem struct {
	Mode  string `json:"mode"` // "env" or "file"
	Name  string `json:"name,omitempty"`
	Path  string `json:"path,omitempty"`
	Perm  uint32 `json:"perm,omitempty"`
	Value string `json:"value"`
}

// Response is sent guest → host.
type Response struct {
	OK     string `json:"ok,omitempty"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// FilePort is the vsock port for file transfer. Each connection carries one
// transfer: a FileHeader line, then a tar stream in the direction the
// header implies (host → guest for "put", guest → host for "get"). The
// guest ends a put with a Response line.
const FilePort uint32 = 4243

// FileHeader opens a file transfer connection.
type FileHeader struct {
	Op   string `json:"op"`            // "put" or "get"
	Path string `json:"path"`          // put: destination directory; get: source file or directory
	UID  int    `json:"uid,omitempty"` // put: owner for created files (0 keeps the tar's)
	GID  int    `json:"gid,omitempty"`
}
