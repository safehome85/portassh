//go:build !windows

package main

import "errors"

// isElevated: privilege elevation is not applicable outside Windows.
func isElevated() bool {
	return false
}

// elevateSelf: the admin mode is implemented for Windows only.
func elevateSelf(extraArg string) error {
	return errors.New("admin mode is supported on Windows only")
}
