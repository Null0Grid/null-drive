package synctest

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Null0Grid/null-drive/shared/client"
)

// TestBenchmarkLargeFile measures the Phase 6 headline: editing part of a
// 1 GiB file, whole-file (v1) vs. chunked (v2) transfers, in bytes on the
// wire and wall time, upload on device A and download on device B, against
// the real server over loopback HTTP with SQLite and disk storage.
//
// Opt-in, it needs ~7 GB of disk and a few minutes:
//
//	NULLDRIVE_BENCH=1 go test ./server/internal/synctest -run BenchmarkLargeFile -v -timeout 30m
//
// NULLDRIVE_BENCH_MB overrides the size (default 1024). Results are
// printed as a Markdown table for docs/BENCHMARKS.md.
func TestBenchmarkLargeFile(t *testing.T) {
	if os.Getenv("NULLDRIVE_BENCH") != "1" {
		t.Skip("set NULLDRIVE_BENCH=1 to run the large-file benchmark")
	}
	mb := 1024
	if v := os.Getenv("NULLDRIVE_BENCH_MB"); v != "" {
		mb, _ = strconv.Atoi(v)
	}
	size := mb << 20

	type step struct {
		name string
		edit func(f *os.File) error
	}
	steps := []step{
		{"initial upload", nil},
		{"overwrite 1 MiB in the middle", func(f *os.File) error {
			_, err := f.WriteAt(randomData(101, 1<<20), int64(size/2))
			return err
		}},
		{"insert 4 KiB near the start", func(f *os.File) error {
			// Insertion shifts every byte after it: the case fixed-size
			// blocks handle worst.
			rest, err := io.ReadAll(io.NewSectionReader(f, 4096, 1<<62))
			if err != nil {
				return err
			}
			if _, err := f.WriteAt(randomData(102, 4096), 4096); err != nil {
				return err
			}
			_, err = f.WriteAt(rest, 8192)
			return err
		}},
		{"append 10 MiB", func(f *os.File) error {
			st, err := f.Stat()
			if err != nil {
				return err
			}
			_, err = f.WriteAt(randomData(103, 10<<20), st.Size())
			return err
		}},
	}

	type result struct {
		upBytes, downBytes int64
		upTime, downTime   time.Duration
	}
	run := func(chunked bool) []result {
		w := newWorld(t)
		a, b := w.device("A"), w.device("B")
		var counters [2]*counter
		for i, d := range []*device{a, b} {
			c := d.engine.Remote.(*client.Client)
			counters[i] = &counter{rt: http.DefaultTransport}
			d.engine.Remote = client.New(w.ts.URL, c.Token(), &http.Client{Transport: counters[i]})
			d.engine.WholeFileOnly = !chunked
		}
		path := a.full("disk-image.bin")
		if err := writeRandomFile(path, size); err != nil {
			t.Fatal(err)
		}
		var out []result
		for i, s := range steps {
			if s.edit != nil {
				f, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.edit(f); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
				// A new mtime the scan can't miss.
				future := time.Now().Add(time.Duration(i) * time.Second)
				_ = os.Chtimes(path, future, future)
			}
			var r result
			counters[0].reset()
			start := time.Now()
			a.sync()
			r.upTime, r.upBytes = time.Since(start), counters[0].total()
			counters[1].reset()
			start = time.Now()
			b.sync()
			r.downTime, r.downBytes = time.Since(start), counters[1].total()
			if !sameFile(t, path, b.full("disk-image.bin")) {
				t.Fatalf("%s: B's copy differs", s.name)
			}
			t.Logf("%-5v %-32s up %8.1f MiB %6.2fs   down %8.1f MiB %6.2fs", chunked, s.name,
				mib(r.upBytes), r.upTime.Seconds(), mib(r.downBytes), r.downTime.Seconds())
			out = append(out, r)
		}
		return out
	}

	whole := run(false)
	chunked := run(true)

	var sb strings.Builder
	fmt.Fprintf(&sb, "\n| Change to a %d MiB file | Upload v1 (whole file) | Upload v2 (chunks) | Download v1 | Download v2 | Bytes saved |\n", mb)
	sb.WriteString("|---|---|---|---|---|---|\n")
	for i, s := range steps {
		w, c := whole[i], chunked[i]
		saved := 1 - float64(c.upBytes+c.downBytes)/float64(w.upBytes+w.downBytes)
		fmt.Fprintf(&sb, "| %s | %.1f MiB · %.1fs | %.1f MiB · %.1fs | %.1f MiB · %.1fs | %.1f MiB · %.1fs | %.1f%% |\n",
			s.name, mib(w.upBytes), w.upTime.Seconds(), mib(c.upBytes), c.upTime.Seconds(),
			mib(w.downBytes), w.downTime.Seconds(), mib(c.downBytes), c.downTime.Seconds(), 100*saved)
	}
	sb.WriteString("\nAt 100 Mbit/s (typical home upload), wire time only:\n\n| Change | v1 upload | v2 upload |\n|---|---|---|\n")
	for i, s := range steps {
		fmt.Fprintf(&sb, "| %s | %s | %s |\n", s.name, wire(whole[i].upBytes, 100e6), wire(chunked[i].upBytes, 100e6))
	}
	t.Log(sb.String())
	if p := os.Getenv("NULLDRIVE_BENCH_OUT"); p != "" {
		_ = os.WriteFile(p, []byte(sb.String()), 0o644)
	}
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }

func wire(bytes int64, bitsPerSec float64) string {
	s := float64(bytes) * 8 / bitsPerSec
	if s >= 60 {
		return fmt.Sprintf("%.1f min", s/60)
	}
	return fmt.Sprintf("%.1f s", s)
}

func writeRandomFile(path string, size int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const block = 16 << 20
	for off, seed := 0, uint64(1000); off < size; off, seed = off+block, seed+1 {
		n := min(block, size-off)
		if _, err := f.Write(randomData(seed, n)); err != nil {
			return err
		}
	}
	return nil
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 4<<20), make([]byte, 4<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, _ := io.ReadFull(fb, bb)
		if na != nb || string(ba[:na]) != string(bb[:nb]) {
			return false
		}
		if ea != nil {
			return true
		}
	}
}

// counter counts HTTP payload bytes in both directions (request bodies and
// response bodies), which is what the protocol choice changes; headers are
// a few hundred bytes per request either way.
type counter struct {
	rt http.RoundTripper
	n  atomic.Int64
}

func (c *counter) reset()       { c.n.Store(0) }
func (c *counter) total() int64 { return c.n.Load() }

func (c *counter) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body = &countingBody{ReadCloser: r.Body, n: &c.n}
	}
	res, err := c.rt.RoundTrip(r)
	if res != nil && res.Body != nil {
		res.Body = &countingBody{ReadCloser: res.Body, n: &c.n}
	}
	return res, err
}

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	return n, err
}
