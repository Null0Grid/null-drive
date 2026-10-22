//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Inside Flatpak the first copy takes the lock and serves; a second launch
// finds the lock taken and hands its arguments over the socket.
func TestFlatpakSingleInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app", "dev.nullgrid.NullDrive")

	first, err := tryFirstCopy(dir)
	if err != nil || first == nil {
		t.Fatalf("first copy didn't get the lock: %v", err)
	}
	defer first.Close()
	got := make(chan []string, 1)
	if err := serveArgs(dir, func(a []string) { got <- a }); err != nil {
		t.Fatal(err)
	}

	second, err := tryFirstCopy(dir)
	if err != nil || second != nil {
		t.Fatalf("second copy got the lock too (%v)", err)
	}
	want := []string{"--url", "nulldrive://open?id=7"}
	if err := sendArgs(dir, want); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-got:
		if !slices.Equal(a, want) {
			t.Fatalf("got %q, want %q", a, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first copy never got the arguments")
	}
	if fi, err := os.Stat(filepath.Join(dir, "instance.sock")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket must be private: %v %v", fi.Mode(), err)
	}

	// Once the first copy is gone, the next launch is the first copy again,
	// and a socket file it left behind doesn't get in the way.
	first.Close()
	again, err := tryFirstCopy(dir)
	if err != nil || again == nil {
		t.Fatalf("lock not released: %v", err)
	}
	defer again.Close()
	if err := serveArgs(dir, func([]string) {}); err != nil {
		t.Fatalf("stale socket blocked the new first copy: %v", err)
	}
}

func TestInstanceDirOnlyInFlatpak(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("FLATPAK_ID", "")
	if d := instanceDir(); d != "" {
		t.Fatalf("outside Flatpak: %q", d)
	}
	t.Setenv("FLATPAK_ID", "dev.nullgrid.NullDrive")
	if d := instanceDir(); d != "/run/user/1000/app/dev.nullgrid.NullDrive" {
		t.Fatalf("got %q", d)
	}
}
