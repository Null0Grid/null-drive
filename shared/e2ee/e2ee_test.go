package e2ee

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
)

// Low-cost KDF for tests (the default takes ~100 ms per unlock).
func testKDF() KDF { return KDF{Alg: "argon2id", Time: 1, MemoryKiB: 8 * 1024, Threads: 1} }

func mustEnable(t *testing.T) (Bundle, *Keys, string) {
	t.Helper()
	b, k, rk, err := Enable("correct horse battery staple", testKDF())
	if err != nil {
		t.Fatal(err)
	}
	return b, k, rk
}

func TestUnlockRoundTrip(t *testing.T) {
	b, k, rk := mustEnable(t)
	k2, err := b.UnlockPassphrase("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.Master(), k2.Master()) {
		t.Fatal("passphrase unlock returned a different master key")
	}
	k3, err := b.UnlockRecovery(rk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.Master(), k3.Master()) {
		t.Fatal("recovery unlock returned a different master key")
	}
	// Lenient recovery key input: lower case, no dashes, look-alikes.
	sloppy := strings.ToLower(strings.ReplaceAll(rk, "-", " "))
	sloppy = strings.ReplaceAll(sloppy, "0", "o")
	sloppy = strings.ReplaceAll(sloppy, "1", "l")
	if _, err := b.UnlockRecovery(sloppy); err != nil {
		t.Fatalf("sloppy recovery key: %v", err)
	}
}

func TestWrongPassphrase(t *testing.T) {
	b, _, rk := mustEnable(t)
	for _, p := range []string{"", "correct horse battery stapl", "Correct horse battery staple", "correct horse battery staple "} {
		if _, err := b.UnlockPassphrase(p); !errors.Is(err, ErrWrongKey) {
			t.Fatalf("passphrase %q: got %v, want ErrWrongKey", p, err)
		}
	}
	// A recovery key with one character changed.
	bad := []byte(rk)
	if bad[0] == 'A' {
		bad[0] = 'B'
	} else {
		bad[0] = 'A'
	}
	if _, err := b.UnlockRecovery(string(bad)); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("wrong recovery key: %v", err)
	}
	for _, s := range []string{"", "short", rk + "A", "UUUU-" + rk[5:]} {
		if _, err := b.UnlockRecovery(s); !errors.Is(err, ErrWrongKey) {
			t.Fatalf("malformed recovery key %q: %v", s, err)
		}
	}
}

func TestWrapsAreBoundToPurpose(t *testing.T) {
	b, _, rk := mustEnable(t)
	// Swapping the two wrapped blobs must not unlock anything.
	swapped := b
	swapped.PassphraseWrap, swapped.RecoveryWrap = b.RecoveryWrap, b.PassphraseWrap
	if _, err := swapped.UnlockRecovery(rk); err == nil {
		t.Fatal("recovery key opened the passphrase wrap")
	}
	// A tampered wrap fails authentication.
	tampered := b
	tampered.PassphraseWrap = append([]byte(nil), b.PassphraseWrap...)
	tampered.PassphraseWrap[30] ^= 1
	if _, err := tampered.UnlockPassphrase("correct horse battery staple"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("tampered wrap: %v", err)
	}
	// A bundle whose fingerprint doesn't match the key inside is refused
	// (a server swapping in a different account's key).
	other, _, _ := mustEnable(t)
	mixed := b
	mixed.Fingerprint = other.Fingerprint
	if _, err := mixed.UnlockPassphrase("correct horse battery staple"); !errors.Is(err, ErrBundle) {
		t.Fatalf("fingerprint mismatch: %v", err)
	}
	// Weak or unknown KDF parameters are rejected before any work.
	weak := b
	weak.KDF.MemoryKiB = 1024
	if _, err := weak.UnlockPassphrase("correct horse battery staple"); !errors.Is(err, ErrBundle) {
		t.Fatalf("weak kdf: %v", err)
	}
}

func TestChangePassphraseKeepsRecovery(t *testing.T) {
	b, k, rk := mustEnable(t)
	nb, err := b.WithPassphrase(k, "a completely new passphrase", testKDF())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nb.UnlockPassphrase("correct horse battery staple"); !errors.Is(err, ErrWrongKey) {
		t.Fatal("old passphrase still works")
	}
	k2, err := nb.UnlockPassphrase("a completely new passphrase")
	if err != nil || !bytes.Equal(k2.Master(), k.Master()) {
		t.Fatalf("new passphrase: %v", err)
	}
	if _, err := nb.UnlockRecovery(rk); err != nil {
		t.Fatalf("recovery key after passphrase change: %v", err)
	}
	rb, rk2, err := nb.WithNewRecoveryKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rb.UnlockRecovery(rk); err == nil {
		t.Fatal("old recovery key still works after rotation")
	}
	if _, err := rb.UnlockRecovery(rk2); err != nil {
		t.Fatal(err)
	}
	if err := CheckPassphrase("short"); err == nil {
		t.Fatal("short passphrase accepted")
	}
	if _, _, _, err := Enable("short", testKDF()); err == nil {
		t.Fatal("Enable accepted a short passphrase")
	}
}

func TestChunkRoundTripAndDeterminism(t *testing.T) {
	_, k, _ := mustEnable(t)
	for _, n := range []int{0, 1, 15, 16, 1000, 1 << 20} {
		plain := bytes.Repeat([]byte{byte(n), 0xAB}, n/2+1)[:n]
		s1, s2 := k.SealChunk(plain), k.SealChunk(plain)
		if !bytes.Equal(s1, s2) {
			t.Fatalf("n=%d: sealing is not deterministic", n)
		}
		if len(s1) != n+ChunkOverhead {
			t.Fatalf("n=%d: sealed %d bytes, want %d", n, len(s1), n+ChunkOverhead)
		}
		got, err := k.OpenChunk(s1)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: open: %v", n, err)
		}
		if n > 0 && bytes.Contains(s1, plain[:min(n, 64)]) && n >= 16 {
			t.Fatalf("n=%d: plaintext visible in ciphertext", n)
		}
	}
	// Different plaintexts get different nonces (no keystream reuse).
	a, b := k.SealChunk([]byte("aaaa")), k.SealChunk([]byte("aaab"))
	if bytes.Equal(a[1:25], b[1:25]) {
		t.Fatal("two plaintexts share a nonce")
	}
	// Another account seals the same data to unrelated bytes: no
	// cross-user dedup and no "does anyone store this file?" oracle.
	_, other, _ := mustEnable(t)
	if bytes.Equal(other.SealChunk([]byte("aaaa")), a) {
		t.Fatal("two accounts produced the same ciphertext")
	}
	if _, err := other.OpenChunk(a); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("another account's key opened the chunk: %v", err)
	}
}

// Every single-bit modification of a sealed chunk must be detected: the
// result is ErrCorrupt, never altered plaintext.
func TestChunkTamperDetection(t *testing.T) {
	_, k, _ := mustEnable(t)
	sealed := k.SealChunk([]byte("the quick brown fox jumps over the lazy dog"))
	for i := range sealed {
		for bit := 0; bit < 8; bit++ {
			mod := append([]byte(nil), sealed...)
			mod[i] ^= 1 << bit
			if _, err := k.OpenChunk(mod); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("flip byte %d bit %d: got %v", i, bit, err)
			}
		}
	}
	for _, cut := range []int{0, 1, ChunkOverhead - 1, len(sealed) - 1} {
		if _, err := k.OpenChunk(sealed[:cut]); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("truncated to %d: %v", cut, err)
		}
	}
	if _, err := k.OpenChunk(append(sealed, 0)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("extended ciphertext accepted")
	}
}

func TestNames(t *testing.T) {
	_, k, _ := mustEnable(t)
	names := []string{"a", "report.pdf", "Ünïcödé 📁", strings.Repeat("x", 15), strings.Repeat("x", 16), strings.Repeat("y", MaxNameBytes), "~not-encrypted", ".hidden"}
	for _, n := range names {
		e, err := k.EncryptName(n)
		if err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		if !IsEncryptedName(e) {
			t.Fatalf("%q: %q not recognised as encrypted", n, e)
		}
		if len(e) > MaxEncryptedSegment {
			t.Fatalf("%q: %d bytes > MaxEncryptedSegment %d", n, len(e), MaxEncryptedSegment)
		}
		e2, _ := k.EncryptName(n)
		if e != e2 {
			t.Fatal("name encryption is not deterministic")
		}
		d, err := k.DecryptName(e)
		if err != nil || d != n {
			t.Fatalf("%q: decrypt %q, %v", n, d, err)
		}
		if strings.Contains(e, n) && len(n) > 3 {
			t.Fatalf("%q visible in %q", n, e)
		}
	}
	// Length is hidden to 16-byte buckets.
	e1, _ := k.EncryptName("a")
	e15, _ := k.EncryptName(strings.Repeat("b", 15))
	e16, _ := k.EncryptName(strings.Repeat("c", 16))
	if len(e1) != len(e15) || len(e16) <= len(e15) {
		t.Fatalf("padding buckets: %d %d %d", len(e1), len(e15), len(e16))
	}
	for _, bad := range []string{"", "a/b", strings.Repeat("z", MaxNameBytes+1), "\xff"} {
		if _, err := k.EncryptName(bad); !errors.Is(err, ErrName) {
			t.Fatalf("EncryptName(%q) = %v", bad, err)
		}
	}
	// Shape checks the server relies on.
	for _, s := range []string{"report.pdf", "~", "~short", "~" + strings.Repeat("A", 54), "~$Budget.xlsx", "~" + strings.Repeat("A", 55) + "!"} {
		if IsEncryptedName(s) {
			t.Fatalf("%q looks encrypted", s)
		}
	}
	// Tampered names fail.
	e, _ := k.EncryptName("secret.txt")
	mod := []byte(e)
	mod[10] ^= 1
	if mod[10] == '-' || mod[10] == '_' || (mod[10] >= '0' && mod[10] <= '9') || (mod[10] >= 'A' && mod[10] <= 'Z') || (mod[10] >= 'a' && mod[10] <= 'z') {
		if _, err := k.DecryptName(string(mod)); err == nil {
			t.Fatal("tampered name decrypted")
		}
	}
	p, err := k.EncryptPath("Documents/Taxes/2026.pdf")
	if err != nil || !IsEncryptedPath(p) || strings.Count(p, "/") != 2 {
		t.Fatalf("EncryptPath: %q %v", p, err)
	}
	// Same name in different folders encrypts the same way, so moving a
	// folder on the server never needs its children re-encrypted.
	q, _ := k.EncryptPath("Other/2026.pdf")
	if p[strings.LastIndex(p, "/"):] != q[strings.LastIndex(q, "/"):] {
		t.Fatal("same leaf name encrypted differently in two folders")
	}
	back, err := k.DecryptPath(p)
	if err != nil || back != "Documents/Taxes/2026.pdf" {
		t.Fatalf("DecryptPath: %q %v", back, err)
	}
	if IsEncryptedPath("~" + strings.Repeat("A", 75) + "/plain") {
		t.Fatal("mixed path counted as encrypted")
	}
}

func TestRecoveryKeyFormat(t *testing.T) {
	raw := bytes.Repeat([]byte{0xff}, RecoveryKeyBytes)
	s := FormatRecoveryKey(raw)
	if len(s) != 39 || strings.Count(s, "-") != 7 {
		t.Fatalf("format %q", s)
	}
	back, err := ParseRecoveryKey(s)
	if err != nil || !bytes.Equal(back, raw) {
		t.Fatalf("parse: %v", err)
	}
}

func TestFileHash(t *testing.T) {
	_, k, _ := mustEnable(t)
	_, other, _ := mustEnable(t)
	sum := sha256.Sum256([]byte("hello"))
	h := k.FileHash(sum[:])
	if len(h) != 64 || h == hex.EncodeToString(sum[:]) || h == other.FileHash(sum[:]) || h != k.FileHash(sum[:]) {
		t.Fatalf("file hash %s", h)
	}
}

// ---- shared vectors with the web UI (ui/src/lib/crypto) ----

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

// Vectors is the cross-implementation fixture: the TypeScript tests load
// the same file and must produce byte-identical results.
type Vectors struct {
	Note        string `json:"note"`
	Master      string `json:"master"`
	Fingerprint string `json:"fingerprint"`
	AuthKey     string `json:"auth_key"`
	Names       []struct {
		Plain, Enc string
	} `json:"names"`
	Paths []struct {
		Plain, Enc string
	} `json:"paths"`
	Chunks []struct {
		Plain, Sealed string // hex
	} `json:"chunks"`
	FileHashes []struct {
		SHA256, Mapped string
	} `json:"file_hashes"`
	Bundle      Bundle `json:"bundle"`
	Passphrase  string `json:"passphrase"`
	RecoveryKey string `json:"recovery_key"`
}

func TestVectors(t *testing.T) {
	const file = "testdata/vectors.json"
	master, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	k, err := FromMaster(master)
	if err != nil {
		t.Fatal(err)
	}
	var v Vectors
	if *update {
		v = Vectors{Note: "Generated by go test ./shared/e2ee -run TestVectors -update. Checked by Go and by ui/src/lib/crypto tests.",
			Master: hex.EncodeToString(master), Fingerprint: k.Fingerprint(), AuthKey: k.AuthKey()}
		for _, n := range []string{"a", "report.pdf", "Ünïcödé 📁", "0123456789abcdef", strings.Repeat("n", 255)} {
			e, _ := k.EncryptName(n)
			v.Names = append(v.Names, struct{ Plain, Enc string }{n, e})
		}
		for _, p := range []string{"Documents/Taxes/2026.pdf", "a"} {
			e, _ := k.EncryptPath(p)
			v.Paths = append(v.Paths, struct{ Plain, Enc string }{p, e})
		}
		for _, c := range [][]byte{{}, []byte("hello, null drive"), bytes.Repeat([]byte{7}, 1000)} {
			v.Chunks = append(v.Chunks, struct{ Plain, Sealed string }{hex.EncodeToString(c), hex.EncodeToString(k.SealChunk(c))})
		}
		for _, s := range []string{"", "hello"} {
			sum := sha256.Sum256([]byte(s))
			v.FileHashes = append(v.FileHashes, struct{ SHA256, Mapped string }{hex.EncodeToString(sum[:]), k.FileHash(sum[:])})
		}
		v.Passphrase = "vector passphrase 123"
		v.Bundle, err = newBundle(k, v.Passphrase, testKDF(), bytes.Repeat([]byte{0x5a}, RecoveryKeyBytes))
		if err != nil {
			t.Fatal(err)
		}
		v.RecoveryKey = FormatRecoveryKey(bytes.Repeat([]byte{0x5a}, RecoveryKeyBytes))
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(file, append(b, '\n'), 0o644); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Fingerprint != k.Fingerprint() || v.AuthKey != k.AuthKey() {
		t.Fatal("fingerprint/auth key changed: the key derivation is part of the format")
	}
	for _, n := range v.Names {
		if e, _ := k.EncryptName(n.Plain); e != n.Enc {
			t.Fatalf("name %q: %s, want %s", n.Plain, e, n.Enc)
		}
	}
	for _, p := range v.Paths {
		if e, _ := k.EncryptPath(p.Plain); e != p.Enc {
			t.Fatalf("path %q changed", p.Plain)
		}
	}
	for _, c := range v.Chunks {
		pl, _ := hex.DecodeString(c.Plain)
		if hex.EncodeToString(k.SealChunk(pl)) != c.Sealed {
			t.Fatalf("chunk %q changed", c.Plain)
		}
	}
	for _, f := range v.FileHashes {
		s, _ := hex.DecodeString(f.SHA256)
		if k.FileHash(s) != f.Mapped {
			t.Fatal("file hash mapping changed")
		}
	}
	for _, unlock := range []func() (*Keys, error){
		func() (*Keys, error) { return v.Bundle.UnlockPassphrase(v.Passphrase) },
		func() (*Keys, error) { return v.Bundle.UnlockRecovery(v.RecoveryKey) },
	} {
		got, err := unlock()
		if err != nil || !bytes.Equal(got.Master(), master) {
			t.Fatalf("vector bundle: %v", err)
		}
	}
}

func BenchmarkSealChunk2MiB(b *testing.B) {
	k, _ := FromMaster(make([]byte, KeySize))
	data := bytes.Repeat([]byte("0123456789abcdef"), 2<<20/16)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		k.SealChunk(data)
	}
}

func BenchmarkOpenChunk2MiB(b *testing.B) {
	k, _ := FromMaster(make([]byte, KeySize))
	sealed := k.SealChunk(bytes.Repeat([]byte("0123456789abcdef"), 2<<20/16))
	b.SetBytes(int64(len(sealed)))
	for i := 0; i < b.N; i++ {
		if _, err := k.OpenChunk(sealed); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnlockDefaultKDF(b *testing.B) {
	bundle, _, _, err := Enable("benchmark passphrase", DefaultKDF())
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		if _, err := bundle.UnlockPassphrase("benchmark passphrase"); err != nil {
			b.Fatal(err)
		}
	}
}
