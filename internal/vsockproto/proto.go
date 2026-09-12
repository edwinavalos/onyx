// Package vsockproto defines the line-oriented protocol spoken between the
// host daemon and the guest agent over vsock. It is deliberately tiny for
// the spike: newline-delimited JSON, one request per line, one response per
// line.
package vsockproto

// Port is the vsock port the guest agent listens on.
const Port uint32 = 4242

// Request is sent host → guest.
type Request struct {
	// Op is the operation name: "ping", "mount", "exec".
	Op string `json:"op"`

	// Mount: block device to format-if-needed and mount.
	Device string `json:"device,omitempty"`
	Target string `json:"target,omitempty"`

	// Exec: command to run.
	Argv []string `json:"argv,omitempty"`
}

// Response is sent guest → host.
type Response struct {
	OK     string `json:"ok,omitempty"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}
