//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"os"

	ssh "github.com/gliderlabs/ssh"
)

func infoHandler(s ssh.Session) {
	h, _ := os.Hostname()
	out, _ := json.MarshalIndent(map[string]string{"host": h, "user": os.Getenv("USER"), "note": "подробная сводка только на Windows"}, "", "  ")
	fmt.Fprintln(s, string(out))
}
