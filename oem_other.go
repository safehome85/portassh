//go:build !windows

package main

import (
	"os/exec"

	"golang.org/x/text/transform"
)

// oemDecoder: на не-Windows вывод shell уже UTF-8.
func oemDecoder() transform.Transformer {
	return nil
}

// setCmdLine: на не-Windows exec.Cmd собирает командную строку сам.
func setCmdLine(cmd *exec.Cmd, line string) {}
