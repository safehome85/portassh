//go:build !windows

package main

import ssh "github.com/gliderlabs/ssh"

// Нативная Win32-диагностика — только на Windows.
func sysNative(s ssh.Session, sub string) bool {
	_, _ = s, sub
	return false
}

func nativeMemoryLoad() int { return -1 }
