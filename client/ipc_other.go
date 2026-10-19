//go:build !windows

package main

// Elsewhere a second launch goes through Wails' single-instance lock,
// which forwards its arguments to the running copy (onSecondInstance).

func sendToRunningCopy([]string) (bool, error) { return false, nil }

func signalRunningCopy([]string) (bool, error) { return false, nil }
