package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "logs", "client.log")
	w, err := Open(p, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes
	for i := 0; i < 10; i++ {
		if _, err := fmt.Fprint(w, line); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client.log", "client.log.1", "client.log.2"} {
		st, err := os.Stat(filepath.Join(dir, "logs", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes, over the limit", name, st.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "client.log.3")); !os.IsNotExist(err) {
		t.Fatal("kept more files than asked")
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("write after close succeeded")
	}
}

func TestReopenAppendsAndConcurrentWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	w, _ := Open(p, 1<<20, 1)
	_, _ = w.Write([]byte("first\n"))
	_ = w.Close()
	w, _ = Open(p, 1<<20, 1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = w.Write([]byte("line\n")) }()
	}
	wg.Wait()
	_ = w.Close()
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "first\n") || strings.Count(string(b), "line\n") != 20 {
		t.Fatalf("content %q", b)
	}
}
