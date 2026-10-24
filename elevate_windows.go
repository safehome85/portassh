//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isElevated reports whether the current process runs with elevated rights.
func isElevated() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// elevateSelf relaunches itself through UAC (verb "runas") with an extra
// argument. An error is also returned when the user declines the UAC prompt.
func elevateSelf(extraArg string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(append(os.Args[1:], extraArg), " "))
	if err != nil {
		return err
	}
	rc, _, _ := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exe)),
		uintptr(unsafe.Pointer(params)),
		0,
		1, // SW_SHOWNORMAL
	)
	if rc <= 32 {
		return fmt.Errorf("failed to launch the admin instance (UAC declined or no rights): code %d", rc)
	}
	return nil
}

var shell32 = windows.NewLazySystemDLL("shell32.dll")
var shellExecuteW = shell32.NewProc("ShellExecuteW")
