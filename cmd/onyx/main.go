package main

import "fmt"

// Set via -ldflags at build time; see Makefile.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	fmt.Printf("onyx %s (%s): local VM sandboxes for coding agents\n", version, commit)
}
