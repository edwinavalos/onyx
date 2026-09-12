package main

import (
	"fmt"
	"os"
)

// Set via -ldflags at build time; see Makefile.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Printf("onyx %s (%s)\n", version, commit)
	case "spike":
		err = runSpike(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "onyx:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: onyx <version|spike> [flags]")
}
