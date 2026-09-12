package guest

import (
	"archive/tar"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// ServeFiles handles file-transfer connections (see vsockproto.FilePort).
func ServeFiles(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go handleFiles(c)
	}
}

func handleFiles(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var h vsockproto.FileHeader
	if err := json.Unmarshal(line, &h); err != nil {
		_ = json.NewEncoder(c).Encode(vsockproto.Response{Error: "bad header: " + err.Error()})
		return
	}
	slog.Info("guest: file transfer", "op", h.Op, "path", h.Path)
	switch h.Op {
	case "put":
		err = extractTar(br, h.Path, h.UID, h.GID)
		if err != nil {
			_ = json.NewEncoder(c).Encode(vsockproto.Response{Error: err.Error()})
			return
		}
		_ = json.NewEncoder(c).Encode(vsockproto.Response{OK: "put"})
	case "get":
		if err := writeTar(c, h.Path); err != nil {
			slog.Warn("guest: get", "path", h.Path, "err", err)
		}
	default:
		_ = json.NewEncoder(c).Encode(vsockproto.Response{Error: "unknown file op " + h.Op})
	}
}

// extractTar unpacks r under dest. Entries may not escape dest.
func extractTar(r io.Reader, dest string, uid, gid int) error {
	if !filepath.IsAbs(dest) {
		return fmt.Errorf("put: destination must be absolute")
	}
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	chown := func(p string) {
		if uid > 0 {
			_ = os.Lchown(p, uid, gid)
		}
	}
	chown(dest)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode) & 0o777 // #nosec G115 -- masked
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
			chown(target)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 -- sanitised by safeJoin
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
			chown(target)
		case tar.TypeSymlink:
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			chown(target)
		default:
			slog.Debug("guest: skipping tar entry", "name", hdr.Name, "type", hdr.Typeflag)
		}
	}
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean("/" + name)
	if strings.HasPrefix(clean, "/..") {
		return "", fmt.Errorf("tar entry escapes destination: %q", name)
	}
	return filepath.Join(root, clean), nil
}

// writeTar streams src (file or directory) as a tar archive rooted at its
// base name.
func writeTar(w io.Writer, src string) error {
	if !filepath.IsAbs(src) {
		return fmt.Errorf("get: source must be absolute")
	}
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
			name = filepath.Join(base, rel)
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
		f, err := os.Open(p) // #nosec G304 -- host-requested path inside the guest
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
