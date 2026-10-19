//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole stops helper commands (reg, explorer) from flashing a
// console window, since the app is a GUI program.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
