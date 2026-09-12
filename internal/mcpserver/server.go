// Package mcpserver exposes the Onyx core to coding harnesses over the
// Model Context Protocol (stdio). It is a thin layer over internal/client.
//
// Secret values never cross this boundary: tools can list secret keys and
// packs and deliver packs to VMs, but cannot read or set values. Anything
// that needs a value goes through the CLI or the app.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/core"
	"github.com/edwinavalos/onyx/internal/pack"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/tarfs"
	"github.com/edwinavalos/onyx/internal/vsockproto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is reported to MCP clients.
var Version = "dev"

// New builds the MCP server bound to a core client and state root.
func New(cl *client.Client, root store.Root) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "onyx", Version: Version}, &mcp.ServerOptions{
		Instructions: "Onyx manages local sandbox VMs for coding agents on this Mac. " +
			"VMs boot from an image, attach named volumes as disks, and receive secret packs. " +
			"Use start_session for a fresh VM that runs a command on its console; use exec to run commands in a running VM. " +
			"Secret values are never readable here; only their key names.",
	})
	t := &tools{cl: cl, root: root}

	mcp.AddTool(s, &mcp.Tool{Name: "list_vms", Description: "List all defined VMs with state, image, resources, volumes and packs."}, t.listVMs)
	mcp.AddTool(s, &mcp.Tool{Name: "get_vm", Description: "Get one VM's definition and state."}, t.getVM)
	mcp.AddTool(s, &mcp.Tool{Name: "create_vm", Description: "Define a VM (does not start it). Volumes are attached at absolute guest paths; packs are delivered on start."}, t.createVM)
	mcp.AddTool(s, &mcp.Tool{Name: "start_vm", Description: "Boot a defined VM and wait for its guest agent."}, t.vmAction("start"))
	mcp.AddTool(s, &mcp.Tool{Name: "stop_vm", Description: "Shut a VM down (asks the guest to power off, forces after a timeout)."}, t.vmAction("stop"))
	mcp.AddTool(s, &mcp.Tool{Name: "pause_vm", Description: "Freeze a running VM in place."}, t.vmAction("pause"))
	mcp.AddTool(s, &mcp.Tool{Name: "resume_vm", Description: "Continue a paused VM."}, t.vmAction("resume"))
	mcp.AddTool(s, &mcp.Tool{Name: "remove_vm", Description: "Delete a stopped VM's definition and root disk. Its volumes are kept."}, t.removeVM)
	mcp.AddTool(s, &mcp.Tool{Name: "exec", Description: "Run a command inside a running VM via the guest agent (as root; use `su dev -c ...` for the work user). Returns combined output."}, t.exec)
	mcp.AddTool(s, &mcp.Tool{Name: "console_log", Description: "Return the last N bytes of a VM's serial console log (what an attached terminal would have shown)."}, t.consoleLog)
	mcp.AddTool(s, &mcp.Tool{Name: "start_session", Description: "Create and boot a fresh VM with a work volume (/home/dev/work) and the shared claude-state volume (/home/dev/.claude), deliver packs, and run a command on the console. The VM powers off when the command exits. Attach a human terminal with `onyx vm console <name>`."}, t.startSession)
	mcp.AddTool(s, &mcp.Tool{Name: "copy_to_vm", Description: "Copy a local file or directory into a running VM (owned by the work user)."}, t.copyToVM)
	mcp.AddTool(s, &mcp.Tool{Name: "copy_from_vm", Description: "Copy a file or directory out of a running VM to a local directory."}, t.copyFromVM)

	mcp.AddTool(s, &mcp.Tool{Name: "list_volumes", Description: "List volumes (named disk images that VMs attach as block devices)."}, t.listVolumes)
	mcp.AddTool(s, &mcp.Tool{Name: "create_volume", Description: "Create a sparse volume. It is formatted ext4 on first mount."}, t.createVolume)
	mcp.AddTool(s, &mcp.Tool{Name: "remove_volume", Description: "Delete a volume that is not attached to a running VM. Its data is lost."}, t.removeVolume)
	mcp.AddTool(s, &mcp.Tool{Name: "list_images", Description: "List installed guest images."}, t.listImages)

	mcp.AddTool(s, &mcp.Tool{Name: "list_packs", Description: "List secret packs with their entries (keys and delivery modes; never values)."}, t.listPacks)
	mcp.AddTool(s, &mcp.Tool{Name: "list_secret_keys", Description: "List the key names of secrets in the Onyx Keychain service. Values are not accessible."}, t.listSecretKeys)
	mcp.AddTool(s, &mcp.Tool{Name: "deliver_packs", Description: "(Re)deliver secret packs to a running VM."}, t.deliverPacks)

	return s
}

type tools struct {
	cl   *client.Client
	root store.Root
}

// ---- inputs ---------------------------------------------------------------

type nameIn struct {
	Name string `json:"name" jsonschema:"VM name"`
}

type createVMIn struct {
	Name     string              `json:"name" jsonschema:"VM name (letters, digits, . _ -)"`
	Image    string              `json:"image,omitempty" jsonschema:"image name; default base"`
	CPUs     uint                `json:"cpus,omitempty" jsonschema:"virtual CPUs; default 2"`
	MemoryMB uint64              `json:"memory_mb,omitempty" jsonschema:"memory in MB; default 2048"`
	Volumes  []store.VolumeMount `json:"volumes,omitempty" jsonschema:"volumes to attach as {volume, target}; target is an absolute guest path"`
	Packs    []string            `json:"packs,omitempty" jsonschema:"secret packs to deliver on start"`
}

type execIn struct {
	Name string   `json:"name" jsonschema:"VM name"`
	Argv []string `json:"argv" jsonschema:"command and arguments, e.g. [\"sh\",\"-c\",\"ls /home/dev/work\"]"`
}

type consoleLogIn struct {
	Name  string `json:"name" jsonschema:"VM name"`
	Bytes int    `json:"bytes,omitempty" jsonschema:"how many trailing bytes to return; default 4000"`
}

type sessionIn struct {
	Name     string   `json:"name,omitempty" jsonschema:"VM name; default session-<timestamp>"`
	Cmd      string   `json:"cmd,omitempty" jsonschema:"command to run on the console; default claude"`
	Dir      string   `json:"dir,omitempty" jsonschema:"guest working directory; default /home/dev/work"`
	Packs    []string `json:"packs,omitempty" jsonschema:"secret packs to deliver"`
	Image    string   `json:"image,omitempty" jsonschema:"image name; default base"`
	CPUs     uint     `json:"cpus,omitempty" jsonschema:"virtual CPUs; default 4"`
	MemoryMB uint64   `json:"memory_mb,omitempty" jsonschema:"memory in MB; default 4096"`
	Work     string   `json:"work_volume,omitempty" jsonschema:"work volume name; default <name>-work (created if missing)"`
	State    string   `json:"state_volume,omitempty" jsonschema:"volume for /home/dev/.claude; default claude-state; empty string disables"`
	NoState  bool     `json:"no_state_volume,omitempty" jsonschema:"do not attach a state volume"`
}

type copyIn struct {
	Name  string `json:"name" jsonschema:"VM name"`
	Local string `json:"local" jsonschema:"local path"`
	Guest string `json:"guest" jsonschema:"absolute guest path (destination directory for copy_to_vm, source for copy_from_vm)"`
}

type volumeIn struct {
	Name   string `json:"name" jsonschema:"volume name"`
	SizeMB int64  `json:"size_mb,omitempty" jsonschema:"size in MB; default 10240 (sparse)"`
}

type packsIn struct {
	Name  string   `json:"name" jsonschema:"VM name"`
	Packs []string `json:"packs" jsonschema:"pack names"`
}

type empty struct{}

// ---- handlers -------------------------------------------------------------

func (t *tools) listVMs(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, []core.VMStatus, error) {
	vms, err := t.cl.ListVMs(ctx)
	if err != nil {
		return nil, nil, err
	}
	if vms == nil {
		vms = []core.VMStatus{}
	}
	return nil, vms, nil
}

func (t *tools) getVM(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, core.VMStatus, error) {
	st, err := t.cl.GetVM(ctx, in.Name)
	return nil, st, err
}

func (t *tools) createVM(ctx context.Context, _ *mcp.CallToolRequest, in createVMIn) (*mcp.CallToolResult, core.VMStatus, error) {
	st, err := t.cl.CreateVM(ctx, store.VMConfig{Name: in.Name, Image: in.Image, CPUs: in.CPUs, MemoryMB: in.MemoryMB, Volumes: in.Volumes, Packs: in.Packs})
	return nil, st, err
}

func (t *tools) vmAction(action string) mcp.ToolHandlerFor[nameIn, core.VMStatus] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, core.VMStatus, error) {
		var st core.VMStatus
		var err error
		switch action {
		case "start":
			st, err = t.cl.StartVM(ctx, in.Name)
		case "stop":
			st, err = t.cl.StopVM(ctx, in.Name)
		default:
			st, err = t.cl.VMAction(ctx, in.Name, action)
		}
		return nil, st, err
	}
}

func (t *tools) removeVM(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, empty, error) {
	return nil, empty{}, t.cl.RemoveVM(ctx, in.Name)
}

func (t *tools) exec(ctx context.Context, _ *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, empty, error) {
	if len(in.Argv) == 0 {
		return nil, empty{}, fmt.Errorf("argv is required")
	}
	out, err := t.cl.Exec(ctx, in.Name, in.Argv)
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}
	if err != nil {
		res.IsError = true
		res.Content = append(res.Content, &mcp.TextContent{Text: "error: " + err.Error()})
	}
	return res, empty{}, nil
}

func (t *tools) consoleLog(_ context.Context, _ *mcp.CallToolRequest, in consoleLogIn) (*mcp.CallToolResult, empty, error) {
	if err := store.ValidName(in.Name); err != nil {
		return nil, empty{}, err
	}
	n := in.Bytes
	if n <= 0 {
		n = 4000
	}
	f, err := os.Open(filepath.Join(t.root.VMDir(in.Name), "console.log")) // #nosec G304 -- name validated
	if err != nil {
		return nil, empty{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, empty{}, err
	}
	if st.Size() > int64(n) {
		if _, err := f.Seek(-int64(n), io.SeekEnd); err != nil {
			return nil, empty{}, err
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, empty{}, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, empty{}, nil
}

type sessionOut struct {
	Name    string `json:"name"`
	Console string `json:"attach_console"`
	Note    string `json:"note"`
}

func (t *tools) startSession(ctx context.Context, _ *mcp.CallToolRequest, in sessionIn) (*mcp.CallToolResult, sessionOut, error) {
	name := in.Name
	if name == "" {
		name = "session-" + timestamp()
	}
	if in.Cmd == "" {
		in.Cmd = "claude"
	}
	if in.Dir == "" {
		in.Dir = "/home/dev/work"
	}
	if in.Work == "" {
		in.Work = name + "-work"
	}
	if in.State == "" && !in.NoState {
		in.State = "claude-state"
	}
	if in.CPUs == 0 {
		in.CPUs = 4
	}
	if in.MemoryMB == 0 {
		in.MemoryMB = 4096
	}
	var mounts []store.VolumeMount
	if err := t.cl.CreateVolume(ctx, in.Work, 20480); err != nil && !strings.Contains(err.Error(), "already exists") {
		return nil, sessionOut{}, err
	}
	mounts = append(mounts, store.VolumeMount{Volume: in.Work, Target: "/home/dev/work"})
	if !in.NoState && in.State != "" {
		if err := t.cl.CreateVolume(ctx, in.State, 20480); err != nil && !strings.Contains(err.Error(), "already exists") {
			return nil, sessionOut{}, err
		}
		mounts = append(mounts, store.VolumeMount{Volume: in.State, Target: "/home/dev/.claude"})
	}
	if _, err := t.cl.CreateVM(ctx, store.VMConfig{Name: name, Image: in.Image, CPUs: in.CPUs, MemoryMB: in.MemoryMB, Volumes: mounts, Packs: in.Packs}); err != nil {
		return nil, sessionOut{}, err
	}
	if _, err := t.cl.StartVM(ctx, name); err != nil {
		_ = t.cl.RemoveVM(ctx, name)
		return nil, sessionOut{}, err
	}
	if err := t.cl.SetSession(ctx, name, vsockproto.Session{Dir: in.Dir, Cmd: in.Cmd, Rows: 40, Cols: 120, OnExit: "poweroff"}); err != nil {
		_, _ = t.cl.StopVM(ctx, name)
		return nil, sessionOut{}, err
	}
	return nil, sessionOut{
		Name:    name,
		Console: "onyx vm console " + name,
		Note:    "The VM powers off when the command exits; remove it afterwards with remove_vm. Volumes persist.",
	}, nil
}

func (t *tools) copyToVM(ctx context.Context, _ *mcp.CallToolRequest, in copyIn) (*mcp.CallToolResult, empty, error) {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(tarfs.Pack(pw, in.Local)) }()
	if err := t.cl.PutFiles(ctx, in.Name, in.Guest, pr); err != nil {
		_ = pr.CloseWithError(err)
		return nil, empty{}, err
	}
	return nil, empty{}, nil
}

func (t *tools) copyFromVM(ctx context.Context, _ *mcp.CallToolRequest, in copyIn) (*mcp.CallToolResult, empty, error) {
	rc, err := t.cl.GetFiles(ctx, in.Name, in.Guest)
	if err != nil {
		return nil, empty{}, err
	}
	defer rc.Close()
	return nil, empty{}, tarfs.Unpack(rc, in.Local)
}

func (t *tools) listVolumes(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, []string, error) {
	v, err := t.cl.ListVolumes(ctx)
	if v == nil {
		v = []string{}
	}
	return nil, v, err
}

func (t *tools) createVolume(ctx context.Context, _ *mcp.CallToolRequest, in volumeIn) (*mcp.CallToolResult, empty, error) {
	if in.SizeMB <= 0 {
		in.SizeMB = 10240
	}
	return nil, empty{}, t.cl.CreateVolume(ctx, in.Name, in.SizeMB)
}

func (t *tools) removeVolume(ctx context.Context, _ *mcp.CallToolRequest, in volumeIn) (*mcp.CallToolResult, empty, error) {
	return nil, empty{}, t.cl.RemoveVolume(ctx, in.Name)
}

func (t *tools) listImages(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, []string, error) {
	v, err := t.cl.ListImages(ctx)
	if v == nil {
		v = []string{}
	}
	return nil, v, err
}

func (t *tools) listPacks(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, []pack.Pack, error) {
	names, err := t.cl.ListPacks(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := []pack.Pack{}
	for _, n := range names {
		p, err := t.cl.GetPack(ctx, n)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	return nil, out, nil
}

func (t *tools) listSecretKeys(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, []string, error) {
	v, err := t.cl.ListSecrets(ctx)
	if v == nil {
		v = []string{}
	}
	return nil, v, err
}

func (t *tools) deliverPacks(ctx context.Context, _ *mcp.CallToolRequest, in packsIn) (*mcp.CallToolResult, empty, error) {
	return nil, empty{}, t.cl.DeliverPacks(ctx, in.Name, in.Packs)
}

func timestamp() string { return time.Now().Format("20060102-150405") }
