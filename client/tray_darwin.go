//go:build darwin

package main

// On macOS both Wails and the tray library need the main thread, so the
// tray is not started there yet (Windows is the first target). The window
// and Dock icon still work; a native macOS menu-bar item is future work.

type tray struct{}

func (a *App) startTray()                      {}
func (a *App) updateTray(string, string, bool) {}
func stopTray()                                {}
