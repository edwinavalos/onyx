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

// Get returns the value stored under key.
func Get(ctx context.Context, key string) (string, error) {
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
