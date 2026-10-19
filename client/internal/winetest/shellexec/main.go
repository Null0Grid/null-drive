//go:build windows

// Opens a URL the way a browser hands an external protocol to Windows:
// ShellExecuteW("open", url). Exit status 0 if Windows found a handler.
package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func main() {
	err := windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(os.Args[1]), nil, nil, windows.SW_SHOWNORMAL)
	if err != nil {
		fmt.Println("ShellExecute:", err)
		os.Exit(1)
	}
}
