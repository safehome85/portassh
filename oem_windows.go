//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// setCmdLine sets the raw command line of the child process without Go's
// MSVCRT argument escaping — the only reliable way to pass quoting through
// to cmd. HideWindow: the child console never flashes (the parent is windowsgui).
func setCmdLine(cmd *exec.Cmd, line string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true}
}

// oemDecoder converts cmd/powershell output from the OEM codepage (CP866 on
// Russian Windows) to UTF-8. Unconditional CP866: verified against the live
// tunnel byte-for-byte. Trade-off: tools emitting UTF-8 natively get decoded
// twice — acceptable for the target fleet.
func oemDecoder() transform.Transformer {
	return charmap.CodePage866.NewDecoder()
}

// startDetached запускает процесс, переживающий завершение родителя
// (и не держащий консоль/канал SSH-сессии).
func startDetached(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x00000008 | 0x00000200 // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
	return cmd.Start()
}
