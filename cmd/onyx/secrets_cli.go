package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/edwinavalos/onyx/internal/pack"
	"golang.org/x/term"
)

func runSecret(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("secret: need set|ls|rm")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("secret set", flag.ContinueOnError)
		fromStdin := fs.Bool("stdin", false, "read the value from stdin instead of prompting")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return fmt.Errorf("usage: onyx secret set <key> [-stdin]")
		}
		value, err := readSecretValue(*fromStdin)
		if err != nil {
			return err
		}
		return cl.SetSecret(ctx, pos[0], value)
	case "ls":
		keys, err := cl.ListSecrets(ctx)
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Println(k)
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx secret rm <key>")
		}
		return cl.RemoveSecret(ctx, args[1])
	}
	return fmt.Errorf("secret: unknown subcommand %q", args[0])
}

// readSecretValue reads a secret without echoing it, or from stdin when
// piped. The value never appears in argv, so it stays out of shell history
// and process listings.
func readSecretValue(fromStdin bool) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "value: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type secretFlags []pack.Secret

func (v *secretFlags) String() string { return fmt.Sprint(*v) }

// Set parses one of:
//
//	key                          env, variable named after the key
//	key=ENV_NAME                 env, explicit variable name
//	key@/guest/path[:perm]       file
//	key>https://upstream[>auth]  proxy; auth is bearer | basic:<user> | header:<Name>
func (v *secretFlags) Set(spec string) error {
	if key, rest, ok := strings.Cut(spec, ">"); ok {
		upstream, auth, _ := strings.Cut(rest, ">")
		*v = append(*v, pack.Secret{Key: key, Mode: pack.ModeProxy, Upstream: upstream, Auth: auth})
		return nil
	}
	if key, rest, ok := strings.Cut(spec, "@"); ok {
		path, perm, _ := strings.Cut(rest, ":")
		*v = append(*v, pack.Secret{Key: key, Mode: pack.ModeFile, Path: path, Perm: perm})
		return nil
	}
	key, name, _ := strings.Cut(spec, "=")
	*v = append(*v, pack.Secret{Key: key, Mode: pack.ModeEnv, Name: name})
	return nil
}

func runPack(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pack: need create|ls|show|rm|deliver")
	}
	cl, err := connect()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("pack create", flag.ContinueOnError)
		var secrets secretFlags
		fs.Var(&secrets, "secret", "key | key=ENV_NAME | key@/guest/path[:perm] (repeatable)")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return fmt.Errorf("usage: onyx pack create <name> -secret ... [-secret ...]")
		}
		return cl.SavePack(ctx, pack.Pack{Name: pos[0], Secrets: secrets})
	case "ls":
		names, err := cl.ListPacks(ctx)
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx pack show <name>")
		}
		p, err := cl.GetPack(ctx, args[1])
		if err != nil {
			return err
		}
		return printJSON(p)
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: onyx pack rm <name>")
		}
		return cl.RemovePack(ctx, args[1])
	case "deliver":
		if len(args) < 3 {
			return fmt.Errorf("usage: onyx pack deliver <vm> <pack...>")
		}
		return cl.DeliverPacks(ctx, args[1], args[2:])
	}
	return fmt.Errorf("pack: unknown subcommand %q", args[0])
}
