// Package vsockproto defines the line-oriented protocol spoken between the
// host daemon and the guest agent over vsock. It is deliberately tiny for
// the spike: newline-delimited JSON, one request per line, one response per
// line.
package vsockproto

// Port is the vsock port the guest agent listens on.
const Port uint32 = 4242

// Request is sent host → guest.
type Request struct {
	// Op is the operation name: "ping", "mount", "exec", "secrets".
	Op string `json:"op"`

	// Mount: block device to format-if-needed and mount.
	Device string `json:"device,omitempty"`
	Target string `json:"target,omitempty"`

	// Exec: command to run.
	Argv []string `json:"argv,omitempty"`

	// Secrets: values to place in the guest (tmpfs only).
	Secrets []SecretItem `json:"secrets,omitempty"`
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
