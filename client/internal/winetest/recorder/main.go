//go:build windows

// A stand-in NullDrive.exe that records its command line in
// C:\launched.json, to prove what a clicked nulldrive:// link launches.
package main

import (
	"encoding/json"
	"os"
)

func main() {
	b, _ := json.Marshal(os.Args[1:])
	_ = os.WriteFile(`C:\launched.json`, b, 0o600)
}
