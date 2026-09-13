// Package client is the Go client for the Onyx core API (see internal/api).
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/edwinavalos/onyx/internal/api"
	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/keychain"
	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// Client talks to one core over its Unix socket.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client for the socket at path.
func New(path string) *Client {
	return &Client{socket: path, http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://onyx"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("is `onyx serve` running? %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error  string `json:"error"`
			Output string `json:"output"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			if e.Output != "" {
				return fmt.Errorf("%s\n%s", e.Error, e.Output)
			}
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, "GET", "/v1/ping", nil, nil)
}

func (c *Client) ListImages(ctx context.Context) ([]string, error) {
	var r api.NamesResp
	return r.Names, c.do(ctx, "GET", "/v1/images", nil, &r)
}

func (c *Client) ImportImage(ctx context.Context, name, dir string) error {
	return c.do(ctx, "POST", "/v1/images", api.ImportImageReq{Name: name, Dir: dir}, nil)
}

func (c *Client) ListVolumes(ctx context.Context) ([]string, error) {
	var r api.NamesResp
	return r.Names, c.do(ctx, "GET", "/v1/volumes", nil, &r)
}

// ListVolumeInfo lists volumes with the VMs whose definitions attach them.
func (c *Client) ListVolumeInfo(ctx context.Context) ([]core.VolumeInfo, error) {
	var r api.VolumesResp
	return r.Volumes, c.do(ctx, "GET", "/v1/volumes", nil, &r)
}

func (c *Client) CreateVolume(ctx context.Context, name string, sizeMB int64) error {
	return c.do(ctx, "POST", "/v1/volumes", api.CreateVolumeReq{Name: name, SizeMB: sizeMB}, nil)
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/volumes/"+url.PathEscape(name), nil, nil)
}

func (c *Client) ListVMs(ctx context.Context) ([]core.VMStatus, error) {
	var r []core.VMStatus
	return r, c.do(ctx, "GET", "/v1/vms", nil, &r)
}

func (c *Client) CreateVM(ctx context.Context, cfg store.VMConfig) (core.VMStatus, error) {
	return c.CreateVMWithVolumes(ctx, cfg, 0)
}

// CreateVMWithVolumes defines the VM and, with createVolumesMB > 0, creates
// the volumes it names that do not exist yet. Those belong to the VM until
// it first runs: RemoveVM deletes them with it (D18).
func (c *Client) CreateVMWithVolumes(ctx context.Context, cfg store.VMConfig, createVolumesMB int64) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "POST", "/v1/vms", api.CreateVMReq{VMConfig: cfg, CreateVolumesMB: createVolumesMB}, &r)
}

func (c *Client) GetVM(ctx context.Context, name string) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "GET", "/v1/vms/"+url.PathEscape(name), nil, &r)
}

func (c *Client) RemoveVM(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/vms/"+url.PathEscape(name), nil, nil)
}

// StartVM boots the VM; a non-nil session is what its console runs once
// up (otherwise a shell).
func (c *Client) StartVM(ctx context.Context, name string, sess *vsockproto.Session) (core.VMStatus, error) {
	var r core.VMStatus
	var body any
	if sess != nil {
		body = api.StartReq{Session: sess}
	}
	return r, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/start", body, &r)
}

func (c *Client) StopVM(ctx context.Context, name string) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/stop", nil, &r)
}

func (c *Client) Exec(ctx context.Context, name string, argv []string) (string, error) {
	var r api.ExecResp
	return r.Output, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/exec", api.ExecReq{Argv: argv}, &r)
}

func (c *Client) ListSecrets(ctx context.Context) ([]string, error) {
	var r api.NamesResp
	return r.Names, c.do(ctx, "GET", "/v1/secrets", nil, &r)
}

func (c *Client) SetSecret(ctx context.Context, key, value string) error {
	return c.do(ctx, "PUT", "/v1/secrets", api.SetSecretReq{Key: key, Value: value}, nil)
}

func (c *Client) RemoveSecret(ctx context.Context, key string) error {
	return c.do(ctx, "DELETE", "/v1/secrets/"+url.PathEscape(key), nil, nil)
}

func (c *Client) ListPacks(ctx context.Context) ([]string, error) {
	var r api.NamesResp
	return r.Names, c.do(ctx, "GET", "/v1/packs", nil, &r)
}

func (c *Client) SavePack(ctx context.Context, p pack.Pack) error {
	return c.do(ctx, "PUT", "/v1/packs", p, nil)
}

func (c *Client) GetPack(ctx context.Context, name string) (pack.Pack, error) {
	var p pack.Pack
	return p, c.do(ctx, "GET", "/v1/packs/"+url.PathEscape(name), nil, &p)
}

func (c *Client) RemovePack(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/packs/"+url.PathEscape(name), nil, nil)
}

func (c *Client) DeliverPacks(ctx context.Context, vmName string, packs []string) error {
	return c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(vmName)+"/packs", api.NamesResp{Names: packs}, nil)
}

func (c *Client) SetSession(ctx context.Context, vmName string, s vsockproto.Session) error {
	return c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(vmName)+"/session", s, nil)
}

func (c *Client) Resize(ctx context.Context, vmName string, rows, cols uint16) error {
	return c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(vmName)+"/resize", api.ResizeReq{Rows: rows, Cols: cols}, nil)
}

// Console opens a raw bidirectional stream to the VM's serial console.
// The caller owns the returned connection.
func (c *Client) Console(ctx context.Context, vmName string) (net.Conn, error) {
	return c.upgrade(ctx, "/v1/vms/"+url.PathEscape(vmName)+"/console", "onyx-console")
}

// Tunnel returns a raw stream to a vsock port inside a running VM.
func (c *Client) Tunnel(ctx context.Context, vmName string, port uint32) (net.Conn, error) {
	return c.upgrade(ctx, fmt.Sprintf("/v1/vms/%s/tunnel?port=%d", url.PathEscape(vmName), port), "onyx-tunnel")
}

// upgrade performs the HTTP/1.1 Upgrade handshake on a fresh socket and
// hands back the connection as a byte stream.
func (c *Client) upgrade(ctx context.Context, path, proto string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("is `onyx serve` running? %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://onyx"+path, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", proto)
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = conn.Close()
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("%s: %s", proto, resp.Status)
	}
	// Anything already buffered past the headers is stream data.
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// PutFiles uploads a tar stream to dest inside the VM.
func (c *Client) PutFiles(ctx context.Context, vmName, dest string, tarStream io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://onyx/v1/vms/"+url.PathEscape(vmName)+"/files?path="+url.QueryEscape(dest), tarStream)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("is `onyx serve` running? %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("put files: %s", resp.Status)
	}
	return nil
}

// GetFiles returns a tar stream of src inside the VM. The caller closes it.
func (c *Client) GetFiles(ctx context.Context, vmName, src string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://onyx/v1/vms/"+url.PathEscape(vmName)+"/files?path="+url.QueryEscape(src), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("is `onyx serve` running? %w", err)
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("get files: %s", resp.Status)
	}
	return resp.Body, nil
}

// VMAction posts a simple lifecycle action (pause, resume, suspend).
func (c *Client) VMAction(ctx context.Context, name, action string) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/"+action, nil, &r)
}

func (c *Client) LinkSecret(ctx context.Context, key string, ref keychain.Ref) error {
	return c.do(ctx, "PUT", "/v1/secrets/link", api.LinkSecretReq{Key: key, Ref: ref}, nil)
}

func (c *Client) DescribeSecret(ctx context.Context, key string) (api.SecretInfo, error) {
	var r api.SecretInfo
	return r, c.do(ctx, "GET", "/v1/secrets/"+url.PathEscape(key), nil, &r)
}

func (c *Client) RemoveImage(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/images/"+url.PathEscape(name), nil, nil)
}
