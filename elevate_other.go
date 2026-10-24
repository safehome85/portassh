//go:build !windows

package main

import "errors"

// isElevated: на не-Windows повышение прав не поддерживается.
func isElevated() bool {
	return false
}

// elevateSelf: админ-режим реализован только для Windows.
func elevateSelf(extraArg string) error {
	return errors.New("admin-режим поддерживается только на Windows")
}
