//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Single instance inside Flatpak.
//
// Wails' lock owns a D-Bus name outside the app's namespace
// (org.wails_app_…), which Flathub doesn't allow. Every instance of a
// Flatpak app shares $XDG_RUNTIME_DIR/app/<app id>, so there the first copy
// holds a lock file and listens on a socket; a later launch finds the lock
// taken, hands its arguments over the socket (show the window, open a
// link, quit) and exits. Two copies would mean two sync engines on one
// folder. Outside Flatpak, Wails' lock is used as before.

// instanceLock is held for the life of the first copy.
var instanceLock *os.File

func instanceDir() string {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if !inFlatpak() || rt == "" {
		return ""
	}
	return filepath.Join(rt, "app", os.Getenv("FLATPAK_ID"))
}

// sendToRunningCopy hands args to the running copy. It returns false, and
// this process becomes the first copy, if none is running.
func sendToRunningCopy(args []string) (bool, error) {
	dir := instanceDir()
	if dir == "" {
		return false, nil
	}
	lock, err := tryFirstCopy(dir)
	if err != nil {
		return false, nil // can't tell: start normally rather than not at all
	}
	if lock != nil {
		instanceLock = lock
		return false, nil
	}
	return true, sendArgs(dir, args)
}

func signalRunningCopy(args []string) (bool, error) { return sendToRunningCopy(args) }

// serveOtherCopies passes later launches' arguments to handle, if this is
// the first copy inside Flatpak.
func serveOtherCopies(handle func([]string)) {
	if instanceLock == nil {
		if sent, _ := sendToRunningCopy(nil); sent || instanceLock == nil {
			return
		}
	}
	if err := serveArgs(instanceDir(), handle); err != nil {
		fmt.Fprintln(os.Stderr, "single instance:", err)
	}
}

// tryFirstCopy takes the lock in dir; nil (no error) if another copy has it.
func tryFirstCopy(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "instance.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// sendArgs gives args to the copy listening in dir. The first copy may still
// be starting, so it retries for a few seconds.
func sendArgs(dir string, args []string) error {
	if args == nil {
		args = []string{}
	}
	var conn net.Conn
	var err error
	for i := 0; i < 50; i++ {
		if conn, err = net.DialTimeout("unix", filepath.Join(dir, "instance.sock"), time.Second); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("the running copy doesn't answer: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(args); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || reply != "ok\n" {
		return fmt.Errorf("the running copy didn't confirm: %q %v", reply, err)
	}
	return nil
}

// serveArgs listens in dir (the caller holds the lock, so a socket file
// left there is stale) and calls handle with each launch's arguments.
func serveArgs(dir string, handle func([]string)) error {
	sock := filepath.Join(dir, "instance.sock")
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return err
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				var args []string
				if err := json.NewDecoder(c).Decode(&args); err != nil {
					return
				}
				_, _ = c.Write([]byte("ok\n"))
				handle(args)
			}(conn)
		}
	}()
	return nil
}
