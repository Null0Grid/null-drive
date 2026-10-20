//go:build !windows && !linux

package main

// registerURLScheme: macOS gets the scheme from the app bundle's
// Info.plist (CFBundleURLTypes) and delivers links via OnUrlOpen. Windows
// and Linux are in shell_windows.go and scheme_linux.go.
func registerURLScheme() error { return nil }
