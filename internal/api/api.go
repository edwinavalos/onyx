// Package api exposes internal/core over HTTP+JSON on a Unix socket. The
// wire format is deliberately plain so the CLI and a SwiftUI client can both
// speak it without generated code.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/store"
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
	case errors.Is(err, store.ErrNotFound):
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
