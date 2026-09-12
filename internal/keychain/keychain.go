// Package keychain stores and retrieves Onyx secrets in the macOS Keychain.
//
// Dev-mode implementation (decisions.md D7): shells out to the `security`
// CLI under a dedicated service name. Items created this way are readable by
// `security` without ACL prompts, which sidesteps the re-sign-re-prompt loop
// while the binary is ad-hoc signed. A Security.framework implementation can
// replace this without changing callers.
package keychain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Service is the Keychain service name Onyx items live under.
const Service = "onyx"

// ErrNotFound is returned when no item exists for a key.
var ErrNotFound = errors.New("keychain item not found")

var keyRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ValidKey checks a secret key name.
func ValidKey(key string) error {
	if !keyRE.MatchString(key) {
		return fmt.Errorf("invalid secret key %q", key)
	}
	return nil
}

// Set stores value under key, replacing any existing item.
func Set(ctx context.Context, key, value string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "security", "add-generic-password", "-U", "-a", key, "-s", Service, "-w", value) // #nosec G204 -- key validated
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain set %s: %w: %s", key, err, out)
	}
	return nil
}

// Get returns the value stored under key, following a link if key is one.
func Get(ctx context.Context, key string) (string, error) {
	raw, err := getRaw(ctx, key)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(raw, refPrefix) {
		return raw, nil
	}
	var r Ref
	if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, refPrefix)), &r); err != nil {
		return "", fmt.Errorf("%s: corrupt link: %w", key, err)
	}
	return resolveRef(ctx, r)
}

func getRaw(ctx context.Context, key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-a", key, "-s", Service, "-w") // #nosec G204 -- key validated
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return "", fmt.Errorf("%s: %w", key, ErrNotFound)
		}
		return "", fmt.Errorf("keychain get %s: %w: %s", key, err, stderr.String())
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// Delete removes the item under key.
func Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "security", "delete-generic-password", "-a", key, "-s", Service) // #nosec G204 -- key validated
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "could not be found") {
			return fmt.Errorf("%s: %w", key, ErrNotFound)
		}
		return fmt.Errorf("keychain delete %s: %w: %s", key, err, out)
	}
	return nil
}

var acctRE = regexp.MustCompile(`"acct"<blob>="([^"]*)"`)

// List returns the keys of all Onyx items.
func List(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "security", "dump-keychain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("keychain list: %w", err)
	}
	return parseDump(string(out)), nil
}

// parseDump extracts our keys from `security dump-keychain` output, which
// prints one attribute block per item.
func parseDump(dump string) []string {
	var keys []string
	for _, block := range strings.Split(dump, "keychain: ") {
		if !strings.Contains(block, `"svce"<blob>="`+Service+`"`) {
			continue
		}
		if m := acctRE.FindStringSubmatch(block); m != nil {
			keys = append(keys, m[1])
		}
	}
	sort.Strings(keys)
	return keys
}

// refPrefix marks an Onyx item whose value is a reference to another
// Keychain item rather than a secret itself.
const refPrefix = "onyx-ref:v1:"

// Ref points at a generic-password item owned by another application and
// optionally a path into a JSON value stored there.
type Ref struct {
	Service string `json:"service"`
	Account string `json:"account,omitempty"`
	// JSONPath is a dot-separated path into the item's JSON value, e.g.
	// "claudeAiOauth.accessToken". Empty means the whole value.
	JSONPath string `json:"json_path,omitempty"`
}

// ClaudeCodeRef references the OAuth access token Claude Code keeps on the
// host. It is refreshed by Claude Code itself, so resolving at use time
// always yields the current token.
var ClaudeCodeRef = Ref{Service: "Claude Code-credentials", JSONPath: "claudeAiOauth.accessToken"}

// Link stores key as a reference to another Keychain item. Get resolves it
// on every call, so the linked value is never copied into Onyx's items.
func Link(ctx context.Context, key string, ref Ref) error {
	if ref.Service == "" {
		return errors.New("link: service required")
	}
	b, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	return Set(ctx, key, refPrefix+string(b))
}

// Describe reports whether key is a link and, if so, what it points at.
func Describe(ctx context.Context, key string) (ref *Ref, err error) {
	raw, err := getRaw(ctx, key)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(raw, refPrefix) {
		return nil, nil
	}
	var r Ref
	if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, refPrefix)), &r); err != nil {
		return nil, fmt.Errorf("%s: corrupt link: %w", key, err)
	}
	return &r, nil
}

// resolveRef reads the referenced item and applies the JSON path.
func resolveRef(ctx context.Context, r Ref) (string, error) {
	args := []string{"find-generic-password", "-s", r.Service, "-w"}
	if r.Account != "" {
		args = append(args, "-a", r.Account)
	}
	cmd := exec.CommandContext(ctx, "security", args...) // #nosec G204 -- fixed argv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return "", fmt.Errorf("linked item %q: %w", r.Service, ErrNotFound)
		}
		return "", fmt.Errorf("linked item %q: %w: %s", r.Service, err, stderr.String())
	}
	val := strings.TrimSuffix(string(out), "\n")
	if r.JSONPath == "" {
		return val, nil
	}
	return jsonPath(val, r.JSONPath)
}

func jsonPath(doc, path string) (string, error) {
	var cur any
	if err := json.Unmarshal([]byte(doc), &cur); err != nil {
		return "", fmt.Errorf("linked value is not JSON: %w", err)
	}
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("json path %q: %q is not an object", path, seg)
		}
		cur, ok = m[seg]
		if !ok {
			return "", fmt.Errorf("json path %q: missing %q", path, seg)
		}
	}
	switch v := cur.(type) {
	case string:
		return v, nil
	case float64, bool:
		return fmt.Sprint(v), nil
	default:
		return "", fmt.Errorf("json path %q: value is not a scalar", path)
	}
}
