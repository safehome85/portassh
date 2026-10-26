//go:build !windows

package main

import (
	ssh "github.com/gliderlabs/ssh"
)

// На non-Windows поведенческого анализатора антивируса нет — пул не нужен.
func execViaPool(s ssh.Session, raw string) bool {
	_, _ = s, raw
	return false
}

func startShellPool() {}
