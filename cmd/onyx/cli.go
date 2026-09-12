package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/edwinavalos/onyx/internal/store"
)

func runImage(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("image: need import|ls|rm")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	switch args[0] {
	case "import":
		if len(args) != 3 {
			return fmt.Errorf("usage: onyx image import <name> <dir>")
		}
		// The core may run elsewhere (inside the app); send an absolute path.
		dir, err := filepath.Abs(args[2])
		if err != nil {
			return err
		}
		return cl.ImportImage(ctx, args[1], dir)
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx image rm <name>")
		}
		return cl.RemoveImage(ctx, args[1])
	case "ls":
		names, err := cl.ListImages(ctx)
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	}
	return fmt.Errorf("image: unknown subcommand %q", args[0])
}

func runVolume(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("volume: need create|ls|rm")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("volume create", flag.ContinueOnError)
		size := fs.Int64("size", 10240, "size in MB (sparse; grows on use)")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return fmt.Errorf("usage: onyx volume create <name> [-size MB]")
		}
		return cl.CreateVolume(ctx, pos[0], *size)
	case "ls":
		names, err := cl.ListVolumes(ctx)
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx volume rm <name>")
		}
		return cl.RemoveVolume(ctx, args[1])
	}
	return fmt.Errorf("volume: unknown subcommand %q", args[0])
}

type volumeFlags []store.VolumeMount

func (v *volumeFlags) String() string { return fmt.Sprint(*v) }
func (v *volumeFlags) Set(s string) error {
	name, target, ok := strings.Cut(s, ":")
	if !ok {
		return fmt.Errorf("volume %q: want name:/guest/path", s)
	}
	*v = append(*v, store.VolumeMount{Volume: name, Target: target})
	return nil
}

func runVM(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("vm: need create|ls|start|stop|rm|status|exec")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("vm create", flag.ContinueOnError)
		var cfg store.VMConfig
		var vols volumeFlags
		fs.StringVar(&cfg.Image, "image", "base", "image name")
		fs.UintVar(&cfg.CPUs, "cpus", 2, "virtual CPUs")
		fs.Uint64Var(&cfg.MemoryMB, "mem", 2048, "memory in MB")
		fs.Var(&vols, "volume", "volume to attach as name:/guest/path (repeatable)")
		var packs, allow stringList
		fs.Var(&packs, "pack", "secret pack to deliver on start (repeatable)")
		fs.StringVar(&cfg.Network, "network", "nat", "network mode: nat (full internet), restricted (HTTP(S) to -allow hosts only, via a host proxy; no NIC), none")
		fs.Var(&allow, "allow", "host a restricted VM may reach: host, *.suffix or host:port (repeatable; 80 and 443 when no port)")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return fmt.Errorf("usage: onyx vm create <name> [flags]")
		}
		cfg.Name = pos[0]
		cfg.Volumes = vols
		cfg.Packs = packs
		cfg.Allow = allow
		st, err := cl.CreateVM(ctx, cfg)
		if err != nil {
			return err
		}
		return printJSON(st)
	case "ls":
		list, err := cl.ListVMs(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tIMAGE\tCPUS\tMEM\tNET\tUPTIME\tVOLUMES")
		for _, v := range list {
			up := ""
			if !v.Started.IsZero() {
				up = time.Since(v.Started).Truncate(time.Second).String()
			}
			vols := make([]string, 0, len(v.Volumes))
			for _, m := range v.Volumes {
				vols = append(vols, m.Volume+":"+m.Target)
			}
			netw := v.Network
			if netw == "" {
				netw = store.NetworkNAT
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%dM\t%s\t%s\t%s\n", v.Name, v.State, v.Image, v.CPUs, v.MemoryMB, netw, up, strings.Join(vols, ","))
		}
		return tw.Flush()
	case "pause", "resume", "suspend":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx vm %s <name>", args[0])
		}
		st, err := cl.VMAction(ctx, args[1], args[0])
		if err != nil {
			return err
		}
		return printJSON(st)
	case "start", "stop", "status", "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx vm %s <name>", args[0])
		}
		switch args[0] {
		case "start":
			st, err := cl.StartVM(ctx, args[1])
			if err != nil {
				return err
			}
			return printJSON(st)
		case "stop":
			st, err := cl.StopVM(ctx, args[1])
			if err != nil {
				return err
			}
			return printJSON(st)
		case "status":
			st, err := cl.GetVM(ctx, args[1])
			if err != nil {
				return err
			}
			return printJSON(st)
		default:
			return cl.RemoveVM(ctx, args[1])
		}
	case "console":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx vm console <name>")
		}
		err := attachConsole(ctx, cl, args[1])
		if errors.Is(err, errDetached) {
			return nil
		}
		return err
	case "dial":
		return runDial(ctx, cl, args[1:])
	case "exec":
		rest := args[1:]
		if len(rest) < 2 {
			return fmt.Errorf("usage: onyx vm exec <name> [--] <cmd...>")
		}
		name := rest[0]
		argv := rest[1:]
		if argv[0] == "--" {
			argv = argv[1:]
		}
		out, err := cl.Exec(ctx, name, argv)
		fmt.Print(out)
		return err
	}
	return fmt.Errorf("vm: unknown subcommand %q", args[0])
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// parseInterspersed parses args with fs, allowing flags to appear after
// positional arguments (stdlib flag stops at the first non-flag). It returns
// the positional arguments in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
		if len(args) == 0 {
			return pos, nil
		}
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
