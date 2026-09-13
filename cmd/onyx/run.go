package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/edwinavalos/onyx/internal/agent"
	"github.com/edwinavalos/onyx/internal/client"
	"github.com/edwinavalos/onyx/internal/store"
	"github.com/edwinavalos/onyx/internal/vsockproto"
	"golang.org/x/term"
)

// runRun implements `onyx run`: a fresh VM for one interactive session of
// the coding harness, torn down when the session ends.
func runRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		cfg       store.VMConfig
		vols      volumeFlags
		packs     stringList
		allow     stringList
		cmd       = fs.String("cmd", "", "command to run on the console (default: selected agent)")
		agentName = fs.String("agent", agent.Default().Name(), "coding agent: claude, codex or pi")
		dir       = fs.String("dir", "", "guest working directory for the command (default: the work volume's mount, "+store.WorkRoot+"/<work>)")
		work      = fs.String("work", "", "work volume name (default: <name>-work; created if missing)")
		state     optionalString
		keep      = fs.Bool("keep", false, "keep the VM definition after the session ends")
		volSize   = fs.Int64("volume-size", 20480, "size in MB for volumes created here")
	)
	fs.Var(&state, "state", "volume holding the selected agent's state (created if missing; \"\" to disable)")
	fs.StringVar(&cfg.Name, "name", "", "VM name (default: session-<time>)")
	fs.StringVar(&cfg.Image, "image", "base", "image name")
	fs.UintVar(&cfg.CPUs, "cpus", store.DefaultCPUs, "virtual CPUs")
	fs.Uint64Var(&cfg.MemoryMB, "mem", store.DefaultMemoryMB, "memory in MB")
	fs.Var(&vols, "volume", "extra volume as name:/guest/path (repeatable)")
	fs.Var(&packs, "pack", "secret pack to deliver (repeatable)")
	fs.StringVar(&cfg.Network, "network", "nat", "network mode: nat (full internet), restricted (HTTP(S) to -allow hosts only, via a host proxy; no NIC), none")
	fs.Var(&allow, "allow", "host a restricted VM may reach: host, *.suffix or host:port (repeatable; 80 and 443 when no port)")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if cfg.Name == "" {
		cfg.Name = "session-" + time.Now().Format("20060102-150405")
	}
	a, err := agent.Lookup(*agentName)
	if err != nil {
		return err
	}
	*work, *dir = sessionPaths(cfg.Name, *work, *dir)
	if *cmd == "" {
		*cmd = a.Command()
	}
	cfg.Packs = packs
	cfg.Allow = allow

	cl, err := connect()
	if err != nil {
		return err
	}

	// Volumes: work + state first so they are /dev/vdb and /dev/vdc. The
	// core creates the missing ones with the VM and owns them until the
	// session has run, so a failed start does not leave them behind (D18).
	cfg.Volumes = append(cfg.Volumes, store.VolumeMount{Volume: *work, Target: store.WorkMountTarget(*work)})
	stateVolume := state.Value
	if !state.Provided {
		stateVolume = a.StateVolume()
	}
	if stateVolume != "" {
		cfg.Volumes = append(cfg.Volumes, store.VolumeMount{Volume: stateVolume, Target: a.StateDir()})
	}
	cfg.Volumes = append(cfg.Volumes, vols...)

	if _, err := cl.CreateVMWithVolumes(ctx, cfg, *volSize); err != nil {
		return err
	}
	cleanup := func() {
		if !*keep {
			if err := cl.RemoveVM(context.Background(), cfg.Name); err != nil {
				fmt.Fprintln(os.Stderr, "onyx: remove vm:", err)
			}
		}
	}

	sess := vsockproto.Session{Dir: *dir, Cmd: *cmd, OnExit: "poweroff"}
	if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		sess.Rows, sess.Cols = uint16(rows), uint16(cols) // #nosec G115 -- terminal sizes are small
	}
	fmt.Fprintf(os.Stderr, "onyx: starting %s ...\n", cfg.Name)
	if _, err := cl.StartVM(ctx, cfg.Name, &sess); err != nil {
		cleanup()
		return err
	}
	stop := func() {
		// The guest powers itself off when the session ends; only force it
		// if it is still up (detach path, or a guest that did not comply).
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		waitStopped(wctx, cl, cfg.Name)
		cancel()
		if st, err := cl.GetVM(context.Background(), cfg.Name); err == nil && st.State != "stopped" {
			if _, err := cl.StopVM(context.Background(), cfg.Name); err != nil {
				fmt.Fprintln(os.Stderr, "onyx: stop vm:", err)
			}
		}
		cleanup()
	}

	err = attachConsole(ctx, cl, cfg.Name)
	if errors.Is(err, errDetached) {
		fmt.Fprintf(os.Stderr, "onyx: %s is still running; reattach with `onyx vm console %s`, stop with `onyx vm stop %s`\n", cfg.Name, cfg.Name, cfg.Name)
		return nil
	}
	fmt.Fprintf(os.Stderr, "onyx: session over, stopping %s\n", cfg.Name)
	stop()
	return err
}

// optionalString distinguishes an omitted flag from -state="", which lets
// each adapter provide its own state-volume default while preserving an
// explicit opt-out.
type optionalString struct {
	Value    string
	Provided bool
}

func (v *optionalString) String() string { return v.Value }
func (v *optionalString) Set(s string) error {
	v.Value, v.Provided = s, true
	return nil
}

// sessionPaths resolves the work volume name (default <name>-work) and the
// session's working directory: the volume's own mount point unless -dir
// was given. Per-volume mounts keep agent memory per project.
func sessionPaths(name, work, dir string) (string, string) {
	if work == "" {
		work = name + "-work"
	}
	if dir == "" {
		dir = store.WorkMountTarget(work)
	}
	return work, dir
}

// waitStopped polls until the VM is no longer running or ctx ends.
func waitStopped(ctx context.Context, cl *client.Client, name string) {
	for ctx.Err() == nil {
		st, err := cl.GetVM(ctx, name)
		if err != nil || st.State == "stopped" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}
