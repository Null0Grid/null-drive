//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func systemSaysDangerous(string) bool { return false }

// shellOpen opens a file with its default app, or a folder in the file
// manager (`open` / `xdg-open`, which take the path as one argument).
func shellOpen(p string) error { return openPath(p) }

// shellReveal shows the file in its folder without opening it.
func shellReveal(p string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", "-R", p)
	} else {
		cmd = exec.Command("xdg-open", filepath.Dir(p)) // no portable "select"
	}
	return cmd.Start()
}

func isPackaged() bool { return false }

func realPath(p string) string { return p }
