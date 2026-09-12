package main

import (
	"context"
	"os/exec"
)

func execCommand(name string, args ...string) ([]byte, error) {
	return exec.CommandContext(context.Background(), name, args...).CombinedOutput() // #nosec G204 -- dev probe
}
