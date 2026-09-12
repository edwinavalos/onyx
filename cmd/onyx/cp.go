package main

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		go func() { pw.CloseWithError(tarLocal(pw, srcPath)) }()
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
		return untarLocal(rc, dstPath)
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

// tarLocal writes src (file or directory) to w rooted at its base name.
func tarLocal(w io.Writer, src string) error {
	src = filepath.Clean(src)
	tw := tar.NewWriter(w)
	base := filepath.Base(src)
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		name := base
		if rel != "." {
			name = filepath.ToSlash(filepath.Join(base, rel))
		}
		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p) // #nosec G304 -- user-supplied local path
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// untarLocal extracts r under dest, refusing entries that escape it.
func untarLocal(r io.Reader, dest string) error {
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean("/" + hdr.Name)
		if strings.HasPrefix(clean, "/..") {
			return fmt.Errorf("tar entry escapes destination: %q", hdr.Name)
		}
		target := filepath.Join(dest, clean)
		mode := os.FileMode(hdr.Mode) & 0o777 // #nosec G115 -- masked
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 -- sanitised above
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}
