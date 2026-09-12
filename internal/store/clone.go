package store

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// CloneFile copies src to dst using an APFS clone (copy-on-write, instant)
// when possible, falling back to a byte copy.
func CloneFile(ctx context.Context, src, dst string) error {
	out, err := exec.CommandContext(ctx, "cp", "-c", src, dst).CombinedOutput() // #nosec G204 -- fixed argv
	if err == nil {
		return nil
	}
	if !strings.Contains(string(out), "not supported") {
		return fmt.Errorf("cp -c: %w: %s", err, out)
	}
	in, err := os.Open(src) // #nosec G304 -- caller-owned path
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	defer o.Close()
	_, err = io.Copy(o, in)
	return err
}
