package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/edwinavalos/onyx/internal/vsockproto"
)

// guestWorkUID mirrors the image's work user; files put into a VM are
// owned by it so the harness can edit them.
const guestWorkUID = 1000

func (c *Core) dialFiles(ctx context.Context, vmName string, h vsockproto.FileHeader) (net.Conn, error) {
	inst, err := c.instance(vmName)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := inst.machine.DialGuest(dctx, vsockproto.FilePort)
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(h); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// PutFiles streams a tar archive from r into dest inside the VM.
func (c *Core) PutFiles(ctx context.Context, vmName, dest string, r io.Reader) error {
	conn, err := c.dialFiles(ctx, vmName, vsockproto.FileHeader{Op: "put", Path: dest, UID: guestWorkUID, GID: guestWorkUID})
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := io.Copy(conn, r); err != nil {
		return fmt.Errorf("put: %w", err)
	}
	// Half-close so the guest sees EOF on the tar stream, then read its verdict.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("put: no response from guest: %w", err)
	}
	var resp vsockproto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("guest: %s", resp.Error)
	}
	return nil
}

// GetFiles streams src from inside the VM as a tar archive into w.
func (c *Core) GetFiles(ctx context.Context, vmName, src string, w io.Writer) error {
	conn, err := c.dialFiles(ctx, vmName, vsockproto.FileHeader{Op: "get", Path: src})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = io.Copy(w, conn)
	return err
}
