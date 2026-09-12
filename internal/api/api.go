// Package api exposes internal/core over HTTP+JSON on a Unix socket. The
// wire format is deliberately plain so the CLI and a SwiftUI client can both
// speak it without generated code.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/keychain"
	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Request/response bodies shared with the client.
type (
	CreateVolumeReq struct {
		Name   string `json:"name"`
		SizeMB int64  `json:"size_mb"`
	}
	ImportImageReq struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}
	ExecReq struct {
		Argv []string `json:"argv"`
	}
	ExecResp struct {
		Output string `json:"output"`
	}
	ResizeReq struct {
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	SetSecretReq struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	LinkSecretReq struct {
		Key string       `json:"key"`
		Ref keychain.Ref `json:"ref"`
	}
	SecretInfo struct {
		Key  string        `json:"key"`
		Link *keychain.Ref `json:"link,omitempty"`
	}
	ErrorResp struct {
		Error string `json:"error"`
	}
	NamesResp struct {
		Names []string `json:"names"`
	}
)

// maxSockPath is the macOS sun_path limit.
const maxSockPath = 104

// Server serves the API for one Core.
type Server struct {
	core *core.Core
	http *http.Server
}

// NewServer builds the router.
func NewServer(c *core.Core) *Server {
	mux := http.NewServeMux()
	s := &Server{core: c}

	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"ok": "pong"}) })

	mux.HandleFunc("GET /v1/images", func(w http.ResponseWriter, _ *http.Request) {
		names, err := c.Root().ListImages()
		respond(w, NamesResp{Names: names}, err)
	})
	mux.HandleFunc("POST /v1/images", func(w http.ResponseWriter, r *http.Request) {
		var req ImportImageReq
		if !decode(w, r, &req) {
			return
		}
		respond(w, map[string]string{"name": req.Name}, c.ImportImage(r.Context(), req.Name, req.Dir))
	})

	mux.HandleFunc("GET /v1/volumes", func(w http.ResponseWriter, _ *http.Request) {
		names, err := c.Root().ListVolumes()
		respond(w, NamesResp{Names: names}, err)
	})
	mux.HandleFunc("POST /v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		var req CreateVolumeReq
		if !decode(w, r, &req) {
			return
		}
		p, err := c.CreateVolume(req.Name, req.SizeMB)
		respond(w, map[string]string{"name": req.Name, "path": p}, err)
	})
	mux.HandleFunc("DELETE /v1/volumes/{name}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"removed": r.PathValue("name")}, c.RemoveVolume(r.PathValue("name")))
	})

	mux.HandleFunc("GET /v1/vms", func(w http.ResponseWriter, _ *http.Request) {
		list, err := c.ListVMs()
		respond(w, list, err)
	})
	mux.HandleFunc("POST /v1/vms", func(w http.ResponseWriter, r *http.Request) {
		var cfg store.VMConfig
		if !decode(w, r, &cfg) {
			return
		}
		if err := c.CreateVM(r.Context(), cfg); err != nil {
			respond(w, nil, err)
			return
		}
		st, err := c.GetVM(cfg.Name)
		respond(w, st, err)
	})
	mux.HandleFunc("GET /v1/vms/{name}", func(w http.ResponseWriter, r *http.Request) {
		st, err := c.GetVM(r.PathValue("name"))
		respond(w, st, err)
	})
	mux.HandleFunc("DELETE /v1/vms/{name}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"removed": r.PathValue("name")}, c.RemoveVM(r.PathValue("name")))
	})
	mux.HandleFunc("POST /v1/vms/{name}/start", func(w http.ResponseWriter, r *http.Request) {
		if err := c.StartVM(r.Context(), r.PathValue("name")); err != nil {
			respond(w, nil, err)
			return
		}
		st, err := c.GetVM(r.PathValue("name"))
		respond(w, st, err)
	})
	mux.HandleFunc("POST /v1/vms/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := c.StopVM(r.Context(), r.PathValue("name")); err != nil {
			respond(w, nil, err)
			return
		}
		st, err := c.GetVM(r.PathValue("name"))
		respond(w, st, err)
	})
	for _, op := range []string{"pause", "resume"} {
		op := op
		mux.HandleFunc("POST /v1/vms/{name}/"+op, func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			var err error
			switch op {
			case "pause":
				err = c.PauseVM(name)
			case "resume":
				err = c.ResumeVM(name)
			}
			if err != nil {
				respond(w, nil, err)
				return
			}
			st, err := c.GetVM(name)
			respond(w, st, err)
		})
	}
	mux.HandleFunc("POST /v1/vms/{name}/exec", func(w http.ResponseWriter, r *http.Request) {
		var req ExecReq
		if !decode(w, r, &req) {
			return
		}
		out, err := c.Exec(r.PathValue("name"), req.Argv)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
			return
		}
		writeJSON(w, 200, ExecResp{Output: out})
	})

	mux.HandleFunc("GET /v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		keys, err := keychain.List(r.Context())
		respond(w, NamesResp{Names: keys}, err)
	})
	mux.HandleFunc("PUT /v1/secrets", func(w http.ResponseWriter, r *http.Request) {
		var req SetSecretReq
		if !decode(w, r, &req) {
			return
		}
		respond(w, map[string]string{"key": req.Key}, keychain.Set(r.Context(), req.Key, req.Value))
	})
	mux.HandleFunc("PUT /v1/secrets/link", func(w http.ResponseWriter, r *http.Request) {
		var req LinkSecretReq
		if !decode(w, r, &req) {
			return
		}
		respond(w, map[string]string{"key": req.Key}, keychain.Link(r.Context(), req.Key, req.Ref))
	})
	mux.HandleFunc("GET /v1/secrets/{key}", func(w http.ResponseWriter, r *http.Request) {
		ref, err := keychain.Describe(r.Context(), r.PathValue("key"))
		respond(w, SecretInfo{Key: r.PathValue("key"), Link: ref}, err)
	})
	mux.HandleFunc("DELETE /v1/secrets/{key}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"removed": r.PathValue("key")}, keychain.Delete(r.Context(), r.PathValue("key")))
	})

	mux.HandleFunc("GET /v1/packs", func(w http.ResponseWriter, _ *http.Request) {
		names, err := c.Packs().List()
		respond(w, NamesResp{Names: names}, err)
	})
	mux.HandleFunc("PUT /v1/packs", func(w http.ResponseWriter, r *http.Request) {
		var p pack.Pack
		if !decode(w, r, &p) {
			return
		}
		respond(w, p, c.Packs().Save(p))
	})
	mux.HandleFunc("GET /v1/packs/{name}", func(w http.ResponseWriter, r *http.Request) {
		p, err := c.Packs().Load(r.PathValue("name"))
		respond(w, p, err)
	})
	mux.HandleFunc("DELETE /v1/packs/{name}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"removed": r.PathValue("name")}, c.Packs().Delete(r.PathValue("name")))
	})
	mux.HandleFunc("POST /v1/vms/{name}/packs", func(w http.ResponseWriter, r *http.Request) {
		var req NamesResp
		if !decode(w, r, &req) {
			return
		}
		respond(w, map[string]any{"delivered": req.Names}, c.DeliverPacks(r.Context(), r.PathValue("name"), req.Names))
	})

	mux.HandleFunc("POST /v1/vms/{name}/session", func(w http.ResponseWriter, r *http.Request) {
		var sess vsockproto.Session
		if !decode(w, r, &sess) {
			return
		}
		respond(w, map[string]string{"session": "set"}, c.SetSession(r.PathValue("name"), sess))
	})
	mux.HandleFunc("POST /v1/vms/{name}/resize", func(w http.ResponseWriter, r *http.Request) {
		var req ResizeReq
		if !decode(w, r, &req) {
			return
		}
		respond(w, map[string]string{"resize": "ok"}, c.Resize(r.PathValue("name"), req.Rows, req.Cols))
	})
	// Files: tar in the request body (put) or the response body (get).
	mux.HandleFunc("PUT /v1/vms/{name}/files", func(w http.ResponseWriter, r *http.Request) {
		dest := r.URL.Query().Get("path")
		respond(w, map[string]string{"put": dest}, c.PutFiles(r.Context(), r.PathValue("name"), dest, r.Body))
	})
	mux.HandleFunc("GET /v1/vms/{name}/files", func(w http.ResponseWriter, r *http.Request) {
		src := r.URL.Query().Get("path")
		if _, err := c.GetVM(r.PathValue("name")); err != nil {
			respond(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		if err := c.GetFiles(r.Context(), r.PathValue("name"), src, w); err != nil {
			slog.Warn("api: get files", "err", err)
		}
	})

	// Console: the connection is hijacked and becomes a raw byte stream in
	// both directions until either side closes it.
	mux.HandleFunc("GET /v1/vms/{name}/console", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, ErrorResp{Error: "console: connection cannot be hijacked"})
			return
		}
		name := r.PathValue("name")
		// Validate before hijacking so errors are still JSON.
		if _, err := c.GetVM(name); err != nil {
			respond(w, nil, err)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: onyx-console\r\n\r\n"); err != nil {
			return
		}
		if err := rw.Flush(); err != nil {
			return
		}
		in, detach, err := c.AttachConsole(name, conn)
		if err != nil {
			_, _ = rw.WriteString(err.Error())
			_ = rw.Flush()
			return
		}
		defer detach()
		_, _ = io.Copy(in, rw) // client input → guest, until the client hangs up
	})

	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return s
}

// ListenAndServe binds the Unix socket at path and serves until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context, path string) error {
	if len(path) >= maxSockPath {
		return fmt.Errorf("socket path %q is longer than %d bytes; set ONYX_SOCKET to a shorter path", path, maxSockPath)
	}
	_ = os.Remove(path)
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdownCtx)
		_ = os.Remove(path)
	}()
	slog.Info("api: listening", "socket", path)
	err = s.http.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResp{Error: "bad request: " + err.Error()})
		return false
	}
	return true
}

func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		writeJSON(w, 200, v)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, pack.ErrNotFound), errors.Is(err, keychain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, ErrorResp{Error: err.Error()})
	default:
		writeJSON(w, http.StatusBadRequest, ErrorResp{Error: err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v == nil {
		v = map[string]any{}
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("api: write", "err", err)
	}
}
