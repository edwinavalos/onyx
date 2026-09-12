package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/edwinavalos/onyx/internal/tarfs"
)

// runCp implements `onyx cp <src> <dst>` where exactly one side is
// `vm:/absolute/path` (Docker's convention).
//
//	onyx cp ./project dev:/home/dev/work      → /home/dev/work/project
//	onyx cp dev:/home/dev/work/project ./out  → ./out/project
func runCp(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: onyx cp <local|vm:/path> <local|vm:/path>")
	}
	srcVM, srcPath := splitVMPath(args[0])
	dstVM, dstPath := splitVMPath(args[1])
	cl, err := connect()
	if err != nil {
		return err
	}
	switch {
	case srcVM == "" && dstVM != "":
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(tarfs.Pack(pw, srcPath)) }()
		if err := cl.PutFiles(ctx, dstVM, dstPath, pr); err != nil {
			_ = pr.CloseWithError(err)
			return err
		}
		return nil
	case srcVM != "" && dstVM == "":
		rc, err := cl.GetFiles(ctx, srcVM, srcPath)
		if err != nil {
			return err
		}
		defer rc.Close()
		return tarfs.Unpack(rc, dstPath)
	default:
		return fmt.Errorf("cp: exactly one side must be vm:/path")
	}
}

func splitVMPath(s string) (vm, path string) {
	if i := strings.Index(s, ":/"); i > 0 && !strings.Contains(s[:i], "/") {
		return s[:i], s[i+1:]
	}
	return "", s
}
