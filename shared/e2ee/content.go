package e2ee

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

// ---- chunks ----

// ChunkVersion is the first byte of every encrypted chunk.
const ChunkVersion byte = 1

// ChunkOverhead is the size an encrypted chunk adds to its plaintext:
// version byte + 24-byte synthetic nonce + 16-byte Poly1305 tag. The
// server uses it to check that a commit's plaintext size matches its
// chunks without seeing any plaintext.
const ChunkOverhead = 1 + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead

const adChunk = "nulldrive/v1/chunk"

// SealChunk encrypts one content-defined chunk. The result is
// deterministic for this account, so the server's chunk ID (the SHA-256
// of the sealed bytes) is the same every time the same data is uploaded:
// dedup and delta sync keep working within the account. Another account
// has different keys, so the same data seals to unrelated bytes and the
// server cannot tell that two users store the same file.
func (k *Keys) SealChunk(plain []byte) []byte {
	out := make([]byte, 1, 1+len(plain)+ChunkOverhead)
	out[0] = ChunkVersion
	return append(out, siv(&k.chunkEnc, &k.chunkSIV, adChunk, plain)...)
}

// OpenChunk decrypts and authenticates a sealed chunk.
func (k *Keys) OpenChunk(blob []byte) ([]byte, error) {
	if len(blob) < ChunkOverhead || blob[0] != ChunkVersion {
		return nil, ErrCorrupt
	}
	return openSIV(&k.chunkEnc, &k.chunkSIV, adChunk, blob[1:])
}

// FileHash maps a file's plaintext SHA-256 to the value stored in the
// server's sha256 column: HMAC-SHA256 under a per-account key. Clients
// compare files by it exactly as before; the server can't use it to
// confirm that a user stores a known file (it would need the key).
func (k *Keys) FileHash(sum []byte) string {
	m := hmac.New(sha256.New, k.fileHash[:])
	m.Write(sum)
	return hex.EncodeToString(m.Sum(nil))
}

// ---- names ----

// NamePrefix starts every encrypted path segment.
const NamePrefix = "~"

const adName = "nulldrive/v1/name"

// namePad is the padding granularity: names are padded to a multiple of
// 16 bytes, so the server learns a name's length only to within 16 bytes.
const namePad = 16

// MaxNameBytes is the longest plaintext name (one path segment), the
// common filesystem limit.
const MaxNameBytes = 255

var b64 = base64.RawURLEncoding

// ErrName: the segment is not a valid encrypted name, or the plaintext
// name is not allowed.
var ErrName = errors.New("e2ee: invalid encrypted name")

// EncryptName encrypts one path segment into "~" + base64url(nonce ||
// ciphertext || tag). Deterministic: the same name in any folder gives the
// same segment, which is what lets a client find "Documents/report.pdf"
// without listing everything. The trade-off (documented in SECURITY.md):
// the server can tell that two items anywhere in the tree have the same
// name, though not what it is.
func (k *Keys) EncryptName(name string) (string, error) {
	if name == "" || len(name) > MaxNameBytes || !utf8.ValidString(name) || strings.ContainsRune(name, '/') {
		return "", ErrName
	}
	// ISO/IEC 7816-4 padding: 0x80 then zeros, always at least one byte.
	padded := make([]byte, 0, (len(name)/namePad+1)*namePad)
	padded = append(padded, name...)
	padded = append(padded, 0x80)
	for len(padded)%namePad != 0 {
		padded = append(padded, 0)
	}
	return NamePrefix + b64.EncodeToString(siv(&k.nameEnc, &k.nameSIV, adName, padded)), nil
}

// DecryptName reverses EncryptName.
func (k *Keys) DecryptName(seg string) (string, error) {
	if !IsEncryptedName(seg) {
		return "", ErrName
	}
	blob, err := b64.DecodeString(seg[len(NamePrefix):])
	if err != nil {
		return "", ErrName
	}
	padded, err := openSIV(&k.nameEnc, &k.nameSIV, adName, blob)
	if err != nil {
		return "", err
	}
	i := len(padded) - 1
	for i >= 0 && padded[i] == 0 {
		i--
	}
	if i < 1 || padded[i] != 0x80 {
		return "", ErrCorrupt
	}
	name := string(padded[:i])
	if !utf8.ValidString(name) || strings.ContainsRune(name, '/') {
		return "", ErrCorrupt
	}
	return name, nil
}

// Encrypted segment sizes: 24-byte nonce + 16-byte tag + padded name
// (16..256 bytes), base64url without padding, plus the prefix.
const (
	minEncBytes = chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead + namePad
	maxEncBytes = chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead + (MaxNameBytes/namePad+1)*namePad
	// MaxEncryptedSegment is the longest encrypted segment in bytes.
	MaxEncryptedSegment = 1 + (maxEncBytes*8+5)/6
)

// IsEncryptedName reports whether seg has the shape of an encrypted name
// (it does not check the key). The server uses this to refuse plaintext
// names on encrypted accounts.
func IsEncryptedName(seg string) bool {
	if !strings.HasPrefix(seg, NamePrefix) {
		return false
	}
	body := seg[len(NamePrefix):]
	for i := 0; i < len(body); i++ {
		c := body[i]
		if !isB64URL(c) {
			return false
		}
	}
	n := b64.DecodedLen(len(body))
	if b64.EncodedLen(n) != len(body) {
		return false
	}
	return n >= minEncBytes && n <= maxEncBytes && (n-minEncBytes)%namePad == 0
}

// IsEncryptedPath reports whether every segment of p is an encrypted name.
func IsEncryptedPath(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if !IsEncryptedName(s) {
			return false
		}
	}
	return true
}

// EncryptPath encrypts each segment of a clean relative path.
func (k *Keys) EncryptPath(p string) (string, error) {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		e, err := k.EncryptName(s)
		if err != nil {
			return "", err
		}
		segs[i] = e
	}
	return strings.Join(segs, "/"), nil
}

// DecryptPath decrypts each segment of an encrypted path.
func (k *Keys) DecryptPath(p string) (string, error) {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		d, err := k.DecryptName(s)
		if err != nil {
			return "", err
		}
		segs[i] = d
	}
	return strings.Join(segs, "/"), nil
}

// Overhead is ChunkOverhead (client.Codec).
func (k *Keys) Overhead() int { return ChunkOverhead }

func isB64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
