// The desktop client is its own Go module so the server's `go test ./...`
// (and its Linux CI) never needs Wails' native toolchain (WebView2 on
// Windows, WebKitGTK on Linux). It uses the shared packages from the
// parent module through the replace directive below.
module github.com/nulldrive/nulldrive/client

go 1.24

require (
	fyne.io/systray v1.11.0
	github.com/fsnotify/fsnotify v1.9.0
	github.com/nulldrive/nulldrive v0.0.0
	github.com/wailsapp/wails/v2 v2.10.2
	github.com/zalando/go-keyring v0.2.6
	golang.org/x/sys v0.36.0
)

replace github.com/nulldrive/nulldrive => ../
