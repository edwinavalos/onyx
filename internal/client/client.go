// Package client is the Go client for the Onyx core API (see internal/api).
package client

import (
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
	"github.com/edwinavalos/onyx/internal/store"
)

// Client talks to one core over its Unix socket.
type Client struct {
	http *http.Client
}

// New returns a client for the socket at path.
func New(path string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
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
	var r core.VMStatus
	return r, c.do(ctx, "POST", "/v1/vms", cfg, &r)
}

func (c *Client) GetVM(ctx context.Context, name string) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "GET", "/v1/vms/"+url.PathEscape(name), nil, &r)
}

func (c *Client) RemoveVM(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/vms/"+url.PathEscape(name), nil, nil)
}

func (c *Client) StartVM(ctx context.Context, name string) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/start", nil, &r)
}

func (c *Client) StopVM(ctx context.Context, name string) (core.VMStatus, error) {
	var r core.VMStatus
	return r, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/stop", nil, &r)
}

func (c *Client) Exec(ctx context.Context, name string, argv []string) (string, error) {
	var r api.ExecResp
	return r.Output, c.do(ctx, "POST", "/v1/vms/"+url.PathEscape(name)+"/exec", api.ExecReq{Argv: argv}, &r)
}
