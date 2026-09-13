// Package agent describes coding harnesses that Onyx ships in its guest
// image. The core deliberately does not know about them: adapters only
// supply the session and persistent-state policy shared by the CLI, MCP and
// app.
package agent

import "fmt"

// Adapter is the provider-specific policy needed to start a coding agent.
// Commands and state locations are guest paths; credentials remain ordinary
// packs so Onyx never gives an adapter access to secret values.
type Adapter interface {
	Name() string
	Command() string
	StateDir() string
	StateVolume() string
	DefaultPack() string
}

type definition struct {
	name, command, stateDir, stateVolume, defaultPack string
}

func (d definition) Name() string        { return d.name }
func (d definition) Command() string     { return d.command }
func (d definition) StateDir() string    { return d.stateDir }
func (d definition) StateVolume() string { return d.stateVolume }
func (d definition) DefaultPack() string { return d.defaultPack }

var adapters = map[string]Adapter{
	"claude": definition{name: "claude", command: "claude", stateDir: "/home/dev/.claude", stateVolume: "claude-state", defaultPack: "claude"},
	"codex":  definition{name: "codex", command: "codex", stateDir: "/home/dev/.codex", stateVolume: "codex-state", defaultPack: "codex"},
	"pi":     definition{name: "pi", command: "pi", stateDir: "/home/dev/.pi", stateVolume: "pi-state", defaultPack: "pi"},
}

// Default is the long-standing Claude Code adapter, kept for compatibility.
func Default() Adapter { return adapters["claude"] }

// Lookup returns a built-in adapter by name.
func Lookup(name string) (Adapter, error) {
	if a, ok := adapters[name]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("unknown coding agent %q (want claude, codex or pi)", name)
}

// All returns the built-in adapters in stable display order.
func All() []Adapter { return []Adapter{adapters["claude"], adapters["codex"], adapters["pi"]} }
