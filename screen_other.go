//go:build !windows

package main

import gossh "golang.org/x/crypto/ssh"

// Захват экрана — только Windows.
func screenPushLoop(client *gossh.Client, cfg config, port int, stop chan struct{}) {}
