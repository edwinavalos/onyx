package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// Client is the core's handle on the proxy process.
type Client struct {
	Socket string
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	return d.DialContext(ctx, "unix", c.Socket)
}

// control sends one Hello and reads the Reply.
func (c *Client) control(ctx context.Context, h Hello) (Reply, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()
	if err := writeHello(conn, h); err != nil {
		return Reply{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Reply{}, fmt.Errorf("proxy %s: %w", h.Op, err)
	}
	var r Reply
	if err := json.Unmarshal(line, &r); err != nil {
		return Reply{}, err
	}
	if !r.OK {
		return r, errors.New(r.Error)
	}
	return r, nil
}

func writeHello(conn net.Conn, h Hello) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(b, '\n'))
	return err
}

// Ping reports whether the proxy process answers.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.control(ctx, Hello{Op: "ping"})
	return err
}

// Configure installs a VM's proxies and returns the CA certificate the
// guest should trust.
func (c *Client) Configure(ctx context.Context, vm string, cfg VMConfig) (caPEM []byte, err error) {
	r, err := c.control(ctx, Hello{Op: "configure", VM: vm, Config: &cfg})
	if err != nil {
		return nil, err
	}
	return []byte(r.CAPEM), nil
}

// Remove drops a VM's proxies.
func (c *Client) Remove(ctx context.Context, vm string) error {
	_, err := c.control(ctx, Hello{Op: "remove", VM: vm})
	return err
}

// Serve opens a connection owned by one of the VM's handlers; the caller
// splices the guest's bytes into it. kind is "forward" or "reverse".
func (c *Client) Serve(ctx context.Context, vm, kind string, index int) (net.Conn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeHello(conn, Hello{Op: "serve", VM: vm, Kind: kind, Index: index}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
