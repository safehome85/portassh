//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/text/transform"
)

// oemDecoder: on non-Windows the shell output is already UTF-8.
func oemDecoder() transform.Transformer {
	return nil
}

// setCmdLine: on non-Windows exec.Cmd builds the command line itself.
func setCmdLine(cmd *exec.Cmd, line string) {}

func enableRDPIfNeeded(rdpPort int) { _ = rdpPort }

func shadowPolicySetup() {}

func setClipboardData(text []byte) error { _ = text; return fmt.Errorf("только Windows") }

func autostartInstall(args []string) error { _ = args; return fmt.Errorf("только Windows") }
func autostartUninstall() error            { return fmt.Errorf("только Windows") }

// Телеметрия для heartbeat (unix-заглушки).
func nativeUsername() string  { return os.Getenv("USER") }
func nativeOSVersion() string { return runtime.GOOS }
func nativeFreeDiskGB() int   { return -1 }

// selfCleanAndExit: non-Windows — удаляет бинарник и завершается.
func selfCleanAndExit() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
	os.Exit(0)
}

// startDetached: child processes survive parent exit on unix.
func startDetached(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return cmd.Start()
}
