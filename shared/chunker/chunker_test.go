package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"
)

// data returns n deterministic pseudo-random bytes.
func data(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := 0; i+8 <= n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

func refs(t testing.TB, b []byte) []Ref {
	t.Helper()
	rs, whole, size, err := Manifest(bytes.NewReader(b), Default, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if whole != hex.EncodeToString(sum[:]) || size != int64(len(b)) {
		t.Fatalf("whole hash/size mismatch")
	}
	return rs
}

func TestBoundsAndReassembly(t *testing.T) {
	b := data(1, 64<<20)
	c, _ := New(bytes.NewReader(b), Default)
	var out bytes.Buffer
	var sizes []int
	for {
		ch, err := c.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ch.Offset != int64(out.Len()) {
			t.Fatalf("offset %d, want %d", ch.Offset, out.Len())
		}
		out.Write(ch.Data)
		sizes = append(sizes, len(ch.Data))
	}
	if !bytes.Equal(out.Bytes(), b) {
		t.Fatal("chunks do not reassemble to the input")
	}
	total := 0
	for i, s := range sizes {
		total += s
		if s > Default.Max || (s < Default.Min && i != len(sizes)-1) {
			t.Fatalf("chunk %d has size %d outside [%d, %d]", i, s, Default.Min, Default.Max)
		}
	}
	avg := total / len(sizes)
	t.Logf("64 MiB -> %d chunks, average %.2f MiB", len(sizes), float64(avg)/(1<<20))
	if avg < 1<<20 || avg > 4<<20 {
		t.Fatalf("average chunk size %d far from the 2 MiB target", avg)
	}
}

// shared counts chunks of b whose hash also appears in a.
func shared(a, b []Ref) (n int, bytesNew int64) {
	set := map[string]bool{}
	for _, r := range a {
		set[r.Hash] = true
	}
	for _, r := range b {
		if set[r.Hash] {
			n++
		} else {
			bytesNew += r.Size
		}
	}
	return n, bytesNew
}

// TestStability is the point of CDC: small edits change few chunks,
// wherever they happen, including insertions that shift everything after.
func TestStability(t *testing.T) {
	orig := data(2, 64<<20)
	base := refs(t, orig)
	edits := map[string][]byte{
		"insert 1 byte at start":   append([]byte{0x42}, orig...),
		"insert 100 bytes mid":     append(append(append([]byte{}, orig[:30<<20]...), data(3, 100)...), orig[30<<20:]...),
		"delete 4 KiB mid":         append(append([]byte{}, orig[:20<<20]...), orig[20<<20+4096:]...),
		"overwrite 1 KiB at 10MiB": overwrite(orig, 10<<20, data(4, 1024)),
		"append 1 MiB":             append(append([]byte{}, orig...), data(5, 1<<20)...),
	}
	for name, edited := range edits {
		got := refs(t, edited)
		same, newBytes := shared(base, got)
		t.Logf("%-26s %3d/%3d chunks unchanged, %5.2f MiB to upload", name, same, len(got), float64(newBytes)/(1<<20))
		// A local edit may touch the chunk it lands in plus a neighbour
		// whose boundary moved; budget 3 chunks' worth at the maximum size.
		if newBytes > int64(3*Default.Max) {
			t.Errorf("%s: %d new bytes, expected only a few chunks", name, newBytes)
		}
	}
}

func overwrite(b []byte, at int, with []byte) []byte {
	out := append([]byte{}, b...)
	copy(out[at:], with)
	return out
}

// TestFixedSizeComparison documents why CDC: with fixed-size blocks one
// inserted byte changes every block after it.
func TestFixedSizeComparison(t *testing.T) {
	orig := data(2, 32<<20)
	edited := append([]byte{0x42}, orig...)
	fixed := func(b []byte) []Ref {
		var rs []Ref
		for off := 0; off < len(b); off += Default.Avg {
			end := min(off+Default.Avg, len(b))
			s := sha256.Sum256(b[off:end])
			rs = append(rs, Ref{Hash: hex.EncodeToString(s[:]), Size: int64(end - off)})
		}
		return rs
	}
	sameFixed, _ := shared(fixed(orig), fixed(edited))
	sameCDC, _ := shared(refs(t, orig), refs(t, edited))
	t.Logf("insert 1 byte at start of 32 MiB: fixed-size blocks unchanged %d, CDC chunks unchanged %d", sameFixed, sameCDC)
	if sameFixed != 0 || sameCDC < len(refs(t, edited))-2 {
		t.Fatalf("fixed=%d cdc=%d", sameFixed, sameCDC)
	}
}

func TestEdgeCases(t *testing.T) {
	if rs := refs(t, nil); len(rs) != 0 {
		t.Fatalf("empty input -> %d chunks", len(rs))
	}
	if rs := refs(t, []byte("tiny")); len(rs) != 1 || rs[0].Size != 4 {
		t.Fatalf("tiny input -> %+v", rs)
	}
	// Zero-filled data has no content boundaries: every chunk is Max and
	// identical, so it dedups to one stored chunk.
	rs := refs(t, make([]byte, 20<<20))
	if len(rs) != 3 || rs[0].Size != int64(Default.Max) || rs[0].Hash != rs[1].Hash {
		t.Fatalf("zeros -> %+v", rs)
	}
	if _, err := New(nil, Params{Min: 10, Avg: 3000, Max: 1}); !errors.Is(err, ErrParams) {
		t.Fatal("invalid params accepted")
	}
}

// slowReader returns at most 7 bytes per Read, to exercise refilling.
type slowReader struct{ r io.Reader }

func (s slowReader) Read(p []byte) (int, error) {
	if len(p) > 7777 {
		p = p[:7777]
	}
	return s.r.Read(p)
}

func TestReaderIndependence(t *testing.T) {
	b := data(6, 12<<20)
	a := refs(t, b)
	got, _, _, err := Manifest(slowReader{bytes.NewReader(b)}, Default, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a) != fmt.Sprint(got) {
		t.Fatal("chunk boundaries depend on read sizes")
	}
}

// TestGolden pins the boundaries for a fixed input. If this fails, the
// chunking changed and every existing stored file would stop deduplicating
// against new uploads: change the parameters only with a migration plan.
func TestGolden(t *testing.T) {
	rs := refs(t, data(7, 16<<20))
	h := sha256.New()
	for _, r := range rs {
		fmt.Fprintf(h, "%s:%d;", r.Hash, r.Size)
	}
	got := hex.EncodeToString(h.Sum(nil))[:16]
	const want = "b188b5f3c48b4dec"
	if got != want {
		t.Fatalf("chunk boundaries changed: digest %s, want %s", got, want)
	}
}

func FuzzChunker(f *testing.F) {
	f.Add([]byte("hello"), uint16(100))
	f.Fuzz(func(t *testing.T, b []byte, scale uint16) {
		p := Params{Min: 64, Avg: 256, Max: 1024}
		if scale%2 == 1 {
			b = bytes.Repeat(b, int(scale%50)+1)
		}
		var out []byte
		rs, _, _, err := Manifest(bytes.NewReader(b), p, func(c Chunk) error {
			out = append(out, c.Data...)
			if len(c.Data) > p.Max || len(c.Data) == 0 {
				t.Fatalf("chunk size %d", len(c.Data))
			}
			return nil
		})
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("reassembly failed (%v), %d chunks", err, len(rs))
		}
	})
}

func BenchmarkChunker(b *testing.B) {
	in := data(8, 64<<20)
	b.SetBytes(int64(len(in)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, _ := New(bytes.NewReader(in), Default)
		for {
			if _, err := c.Next(); err != nil {
				break
			}
		}
	}
}

func BenchmarkCutOnly(b *testing.B) {
	in := data(9, 64<<20)
	b.SetBytes(int64(len(in)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for off := 0; off < len(in); {
			off += Default.Cut(in[off:])
		}
	}
}
