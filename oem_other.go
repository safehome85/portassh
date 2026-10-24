//go:build !windows

package main

import (
	"os/exec"

	"golang.org/x/text/transform"
)

// oemDecoder: on non-Windows the shell output is already UTF-8.
func oemDecoder() transform.Transformer {
	return nil
}

// setCmdLine: on non-Windows exec.Cmd builds the command line itself.
func setCmdLine(cmd *exec.Cmd, line string) {}

// startDetached: child processes survive parent exit on unix.
func startDetached(cmd *exec.Cmd) {
	_ = cmd.Start()
}
