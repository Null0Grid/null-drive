package chunker

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
)

var updateCross = flag.Bool("update", false, "rewrite testdata/cdc-vectors.json")

// splitmixData is input any language can reproduce exactly: SplitMix64
// from a seed, 8 little-endian bytes per step.
func splitmixData(seed uint64, n int) []byte {
	b := make([]byte, n)
	x := seed
	for i := 0; i < n; i += 8 {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		for j := 0; j < 8 && i+j < n; j++ {
			b[i+j] = byte(z >> (8 * j))
		}
	}
	return b
}

type crossCase struct {
	Seed   uint64  `json:"seed"`
	Length int     `json:"length"`
	Params Params  `json:"params"`
	Sizes  []int64 `json:"sizes"`
}

// TestCrossVectors pins chunk boundaries for the web UI's TypeScript port
// (ui/src/lib/crypto/fastcdc.ts), which must cut at exactly the same
// places: identical chunks across desktop and browser uploads is what
// makes dedup and delta sync work between them.
func TestCrossVectors(t *testing.T) {
	const file = "testdata/cdc-vectors.json"
	cases := []crossCase{
		{Seed: 42, Length: 20<<20 + 12345, Params: Default},
		{Seed: 7, Length: 1 << 20, Params: Params{Min: 2048, Avg: 8192, Max: 65536}},
		{Seed: 1, Length: 100, Params: Default},
	}
	for i := range cases {
		c := &cases[i]
		rs, _, _, err := Manifest(bytes.NewReader(splitmixData(c.Seed, c.Length)), c.Params, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			c.Sizes = append(c.Sizes, r.Size)
		}
	}
	if *updateCross {
		b, _ := json.MarshalIndent(cases, "", " ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, append(b, '\n'), 0o644); err != nil { //nolint:gosec // fixture
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var want []crossCase
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if len(want[i].Sizes) != len(cases[i].Sizes) {
			t.Fatalf("case %d: %d chunks, fixture has %d", i, len(cases[i].Sizes), len(want[i].Sizes))
		}
		for j := range want[i].Sizes {
			if want[i].Sizes[j] != cases[i].Sizes[j] {
				t.Fatalf("case %d chunk %d: %d, fixture %d", i, j, cases[i].Sizes[j], want[i].Sizes[j])
			}
		}
	}
}
