// Package pack defines secret packs: named bundles of Keychain secrets with a
// delivery mode each (decisions.md D6).
package pack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/edwinavalos/onyx/internal/keychain"
)

// Mode says how a secret reaches the guest.
type Mode string

const (
	// ModeEnv exports the secret into the agent session's environment.
	ModeEnv Mode = "env"
	// ModeFile writes the secret to a tmpfs-backed path in the guest.
	ModeFile Mode = "file"
	// ModeProxy keeps the secret on the host; a helper substitutes it.
	// Not implemented yet.
	ModeProxy Mode = "proxy"
)

// Secret is one entry in a pack.
type Secret struct {
	// Key is the Keychain key holding the value.
	Key string `json:"key"`
	// Mode is how it is delivered.
	Mode Mode `json:"mode"`
	// Name is the environment variable name (env mode). Defaults to Key.
	Name string `json:"name,omitempty"`
	// Path is the guest path to write (file mode).
	Path string `json:"path,omitempty"`
	// Perm is the file mode in octal, e.g. "0600" (file mode). Defaults to 0600.
	Perm string `json:"perm,omitempty"`
}

// Pack is a named set of secrets.
type Pack struct {
	Name    string   `json:"name"`
	Secrets []Secret `json:"secrets"`
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var envRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrNotFound is returned for unknown packs.
var ErrNotFound = errors.New("pack not found")

// Validate checks the pack is well formed.
func (p Pack) Validate() error {
	if !nameRE.MatchString(p.Name) {
		return fmt.Errorf("invalid pack name %q", p.Name)
	}
	for i, s := range p.Secrets {
		if err := keychain.ValidKey(s.Key); err != nil {
			return fmt.Errorf("secret %d: %w", i, err)
		}
		switch s.Mode {
		case ModeEnv:
			n := s.Name
			if n == "" {
				n = s.Key
			}
			if !envRE.MatchString(n) {
				return fmt.Errorf("secret %s: %q is not a valid environment variable name", s.Key, n)
			}
		case ModeFile:
			if !filepath.IsAbs(s.Path) {
				return fmt.Errorf("secret %s: file mode needs an absolute guest path", s.Key)
			}
		case ModeProxy:
			return fmt.Errorf("secret %s: proxy mode is not implemented yet", s.Key)
		default:
			return fmt.Errorf("secret %s: unknown mode %q", s.Key, s.Mode)
		}
	}
	return nil
}

// Store persists packs as JSON files in a directory.
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name+".json") }

// Save writes p.
func (s Store) Save(p Pack) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(p.Name), b, 0o600)
}

// Load reads a pack by name.
func (s Store) Load(name string) (Pack, error) {
	var p Pack
	if !nameRE.MatchString(name) {
		return p, fmt.Errorf("invalid pack name %q", name)
	}
	b, err := os.ReadFile(s.path(name)) // #nosec G304 -- name validated
	if errors.Is(err, os.ErrNotExist) {
		return p, fmt.Errorf("pack %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

// Delete removes a pack.
func (s Store) Delete(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid pack name %q", name)
	}
	err := os.Remove(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("pack %q: %w", name, ErrNotFound)
	}
	return err
}

// List returns all pack names.
func (s Store) List() ([]string, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if n, ok := cutJSON(e.Name()); ok {
			out = append(out, n)
		}
	}
	return out, nil
}

func cutJSON(s string) (string, bool) {
	const suf = ".json"
	if len(s) > len(suf) && s[len(s)-len(suf):] == suf {
		return s[:len(s)-len(suf)], true
	}
	return "", false
}
