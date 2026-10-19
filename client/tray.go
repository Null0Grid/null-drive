//go:build !darwin

package main

import (
	"embed"
	"runtime"

	"fyne.io/systray"
)

//go:embed trayicons
var trayIcons embed.FS

// trayIcon returns the icon for an agent state: .ico on Windows, .png on
// Linux (what each platform's tray API expects).
func trayIcon(state string) []byte {
	switch state {
	case "idle", "syncing", "paused", "error", "offline":
	case "attention":
		state = "error"
	default:
		state = "idle"
	}
	ext := ".png"
	if runtime.GOOS == "windows" {
		ext = ".ico"
	}
	b, _ := trayIcons.ReadFile("trayicons/" + state + ext)
	return b
}

type tray struct {
	status *systray.MenuItem
	pause  *systray.MenuItem
}

// startTray runs the tray in its own OS thread (Wails owns the main one;
// on Windows each thread can run its own message loop).
func (a *App) startTray() {
	go func() {
		runtime.LockOSThread()
		systray.Run(a.trayReady, nil)
	}()
}

func (a *App) trayReady() {
	systray.SetIcon(trayIcon("idle"))
	systray.SetTooltip("Null Drive")
	t := &tray{}
	t.status = systray.AddMenuItem("Starting…", "")
	t.status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem("Open Null Drive", "Show the window")
	folder := systray.AddMenuItem("Open folder", "Open the synced folder")
	syncNow := systray.AddMenuItem("Sync now", "")
	t.pause = systray.AddMenuItem("Pause syncing", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Stop syncing and exit")

	a.mu.Lock()
	a.tray = t
	a.mu.Unlock()

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				a.Show()
			case <-folder.ClickedCh:
				_ = a.OpenFolder()
			case <-syncNow.ClickedCh:
				a.SyncNow()
			case <-t.pause.ClickedCh:
				if a.isPaused() {
					a.Resume()
				} else {
					a.Pause()
				}
			case <-quit.ClickedCh:
				a.Quit()
				return
			}
		}
	}()
}

func (a *App) updateTray(state, message string, paused bool) {
	a.mu.Lock()
	t := a.tray
	a.mu.Unlock()
	if t == nil {
		return
	}
	systray.SetIcon(trayIcon(state))
	systray.SetTooltip("Null Drive: " + message)
	t.status.SetTitle(message)
	if paused {
		t.pause.SetTitle("Resume syncing")
	} else {
		t.pause.SetTitle("Pause syncing")
	}
}

func stopTray() { systray.Quit() }
