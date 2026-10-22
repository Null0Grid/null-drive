// Command NullDrive is the desktop sync client (Wails v2).
//
//	NullDrive            open the window
//	NullDrive --hidden   start in the tray (used by "start on login")
//	NullDrive --quit     ask a running copy to stop syncing and exit (used by
//	                     the installer); does nothing if none is running
//	NullDrive --forget   delete this computer's settings and saved credentials
//	                     (used by the uninstaller); the synced files stay
//	NullDrive --url nulldrive://open?id=<id>
//	                     open a file from a link (Null Search results); the
//	                     scheme is registered by the installer, the Store
//	                     package and, for the portable copy, the app itself
package main

import (
	"context"
	"embed"
	"log"
	"os"
	"slices"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

// Set with -ldflags "-X main.version=v0.4.0".
var version = "dev"

// singleInstanceID names the single-instance lock. ipc_windows.go derives
// the names of Wails' lock objects from it, so it must not change.
const singleInstanceID = "io.nulldrive.client"

func main() {
	args := os.Args[1:]
	// A link (from a web page via the browser) ignores every other flag, so
	// a crafted one can't trigger --forget or --quit. If a copy is already
	// running, hand the link to it and exit; otherwise start (in the tray)
	// and open it once the app is ready.
	rawLink, hasLink := linkFromArgs(args)
	if hasLink {
		if sent, err := sendToRunningCopy([]string{"--url", rawLink}); sent {
			if err != nil {
				log.Fatalf("open link: %v", err)
			}
			return
		}
		args = []string{"--url", rawLink}
	}
	// The installer helpers run without starting Wails: Wails checks for
	// the WebView2 runtime before anything else and, if it's missing,
	// shows a download prompt, which would hang a silent uninstall.
	if slices.Contains(args, "--forget") {
		if err := forgetEverything(); err != nil {
			log.Fatalf("forget: %v", err)
		}
		return
	}
	quitOnly := slices.Contains(args, "--quit")
	if quitOnly {
		if handled, err := signalRunningCopy(args); handled {
			if err != nil {
				log.Fatalf("quit: %v", err)
			}
			return
		}
		// Elsewhere --quit goes through Wails' own single-instance lock
		// (onSecondInstance); if no copy is running, this process becomes
		// the first instance and exits at once without tray or sync.
	}
	// Inside Flatpak the single-instance lock is ours (ipc_linux.go): a
	// plain second launch hands over its arguments here and exits.
	if inFlatpak() && !hasLink && !quitOnly {
		if sent, err := sendToRunningCopy(args); sent {
			if err != nil {
				log.Fatalf("show the running copy: %v", err)
			}
			return
		}
	}
	app := NewApp(version)
	if hasLink {
		app.queueLink(rawLink)
	}
	var single *options.SingleInstanceLock
	if inFlatpak() {
		serveOtherCopies(func(a []string) { app.onSecondInstance(options.SecondInstanceData{Args: a}) })
	} else {
		single = &options.SingleInstanceLock{
			UniqueId:               singleInstanceID,
			OnSecondInstanceLaunch: app.onSecondInstance,
		}
	}
	hidden := slices.Contains(args, "--hidden") || hasLink
	startup := app.startup
	if quitOnly {
		startup = func(ctx context.Context) { wruntime.Quit(ctx) }
	}

	err := wails.Run(&options.App{
		Title:     "Null Drive",
		Width:     980,
		Height:    700,
		MinWidth:  420,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 10, G: 10, B: 15, A: 255},
		// Closing the window keeps syncing in the tray; Quit is in the tray menu.
		HideWindowOnClose:  true,
		StartHidden:        hidden || quitOnly,
		SingleInstanceLock: single,
		OnStartup:          startup,
		OnDomReady:         app.domReady,
		OnShutdown: func(ctx context.Context) {
			if !quitOnly {
				app.shutdown(ctx)
			}
		},
		Bind: []interface{}{app},
		Windows: &windows.Options{
			Theme: windows.Dark,
		},
		// macOS delivers links as an Apple event, not as an argument (the
		// bundle's Info.plist declares the scheme).
		Mac: &mac.Options{OnUrlOpen: app.queueLink},
	})
	if err != nil {
		log.Fatal(err)
	}
}
