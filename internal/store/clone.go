package store

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
)

// CloneFile copies src to dst using an APFS clone (copy-on-write, instant)
// when possible, falling back to a byte copy. Only macOS cp knows -c; on
// other systems (and across filesystems, where the clone is "not
// supported") the byte copy is the whole story and reports any real error.
func CloneFile(ctx context.Context, src, dst string) error {
	if runtime.GOOS == "darwin" {
		if err := exec.CommandContext(ctx, "cp", "-c", src, dst).Run(); err == nil { // #nosec G204 -- fixed argv
			return nil
		}
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
