//go:build !windows

package main

// События питания — только для Windows-флота; на других ОС реакция на сон
// остаётся на TCP-keepalive сокета.
func startPowerWatcher() {}
