// Package e2ee is Null Drive's optional end-to-end encryption.
//
// Everything here runs on the client (desktop app, CLI; the web UI has a
// TypeScript twin pinned to the same test vectors). The server only ever
// stores what this package outputs: wrapped keys, encrypted chunks and
// encrypted path segments.
//
// # Key hierarchy
//
//	passphrase ──Argon2id(salt)──▶ passphrase KEK ─┐
//	                                               ├─wrap─▶ master key (32 random bytes)
//	recovery key ──HKDF(salt)────▶ recovery KEK ───┘            │
//	                                                            HKDF, one key per purpose
//	                     ┌──────────┬───────────┬───────────────┼────────────┬─────────┐
//	                 name-enc   name-siv    chunk-enc   chunk-siv   file-hash    auth
//
// The master key never leaves the client unwrapped. The server stores two
// wrapped copies (one per KEK), so changing the passphrase re-wraps the
// master key without re-encrypting any data.
//
// # Primitives (all from golang.org/x/crypto or the standard library)
//
//   - Argon2id (x/crypto/argon2): passphrase stretching, 3 passes, 64 MiB.
//   - XChaCha20-Poly1305 (x/crypto/chacha20poly1305): all encryption.
//   - HMAC-SHA256, HKDF-SHA256 (stdlib): synthetic nonces, subkeys.
//
// Names and chunks use deterministic authenticated encryption built the
// SIV way (RFC 5297's construction, with these primitives): the nonce is
// an HMAC of the plaintext under a separate key, so the same plaintext
// always produces the same ciphertext *for this user*. That determinism is
// what keeps path lookups, renames and within-user dedup working. See
// docs/DESIGN.md (Phase 7) for why this is not convergent encryption.
package e2ee

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the size of every symmetric key.
const KeySize = 32

// FormatVersion is the version of the key bundle and ciphertext formats.
const FormatVersion = 1

// Errors.
var (
	// ErrWrongKey: the passphrase or recovery key does not unwrap the
	// master key (the AEAD tag did not verify).
	ErrWrongKey = errors.New("e2ee: wrong passphrase or recovery key")
	// ErrCorrupt: a ciphertext failed authentication or is malformed.
	// Tampering with any byte produces this, never wrong plaintext.
	ErrCorrupt = errors.New("e2ee: ciphertext is corrupt or was modified")
	// ErrBundle: the key bundle is malformed or uses unknown parameters.
	ErrBundle = errors.New("e2ee: invalid key bundle")
)

// KDF holds the Argon2id parameters stored with the bundle, so they can be
// raised later without breaking existing accounts.
type KDF struct {
	Alg       string `json:"alg"` // "argon2id"
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	Salt      []byte `json:"salt"` // 16 bytes
}

// DefaultKDF: 3 passes over 64 MiB, one lane. One lane because the web
// UI's libsodium only supports p=1 and the hashes must match exactly; 64
// MiB keeps unlocking under a second on a phone browser while making each
// offline guess cost 64 MiB of memory bandwidth.
func DefaultKDF() KDF {
	return KDF{Alg: "argon2id", Time: 3, MemoryKiB: 64 * 1024, Threads: 1}
}

func (k KDF) valid() bool {
	return k.Alg == "argon2id" && k.Time >= 1 && k.Time <= 20 &&
		k.MemoryKiB >= 8*1024 && k.MemoryKiB <= 1024*1024 && k.Threads == 1 && len(k.Salt) == 16
}

// Bundle is what the server stores for an encrypted account. Nothing in it
// is secret without the passphrase or the recovery key.
type Bundle struct {
	Version        int    `json:"version"`
	KDF            KDF    `json:"kdf"`
	PassphraseWrap []byte `json:"passphrase_wrapped"` // nonce || XChaCha20-Poly1305(master key)
	RecoveryWrap   []byte `json:"recovery_wrapped"`
	RecoverySalt   []byte `json:"recovery_salt"`
	Fingerprint    string `json:"fingerprint"` // identifies the master key (not secret)
}

// Associated data binds each wrap to its purpose, so a passphrase-wrapped
// blob can't be passed off as the recovery one or vice versa.
const (
	adWrapPassphrase = "nulldrive/v1/wrap/passphrase" //nolint:gosec // associated-data label, not a credential
	adWrapRecovery   = "nulldrive/v1/wrap/recovery"
)

// Keys are the unwrapped per-purpose keys of one account.
type Keys struct {
	master                     [KeySize]byte
	nameEnc, nameSIV           [KeySize]byte
	chunkEnc, chunkSIV         [KeySize]byte
	fileHash, auth, fingerprnt [KeySize]byte
}

func subkey(master []byte, purpose string) (k [KeySize]byte) {
	b, err := hkdf.Key(sha256.New, master, nil, "nulldrive/v1/"+purpose, KeySize)
	if err != nil {
		panic(err) // only fails for absurd lengths
	}
	copy(k[:], b)
	return k
}

// FromMaster derives all subkeys from a master key.
func FromMaster(master []byte) (*Keys, error) {
	if len(master) != KeySize {
		return nil, fmt.Errorf("e2ee: master key must be %d bytes", KeySize)
	}
	k := &Keys{}
	copy(k.master[:], master)
	k.nameEnc = subkey(master, "name-enc")
	k.nameSIV = subkey(master, "name-siv")
	k.chunkEnc = subkey(master, "chunk-enc")
	k.chunkSIV = subkey(master, "chunk-siv")
	k.fileHash = subkey(master, "file-hash")
	k.auth = subkey(master, "auth")
	k.fingerprnt = subkey(master, "fingerprint")
	return k, nil
}

// Master returns a copy of the master key (to store in the OS keychain).
func (k *Keys) Master() []byte { return append([]byte(nil), k.master[:]...) }

// Fingerprint identifies the master key: 16 hex characters derived from it.
// Clients compare it to the bundle's to detect a key change.
func (k *Keys) Fingerprint() string { return hex.EncodeToString(k.fingerprnt[:8]) }

// AuthKey proves to the server that the caller holds the master key, for
// operations that replace the bundle. The server stores only its SHA-256;
// HKDF is one-way, so revealing it reveals nothing about the other keys.
func (k *Keys) AuthKey() string { return hex.EncodeToString(k.auth[:]) }

// AuthHash is what the server stores for an AuthKey.
func AuthHash(authKey string) string {
	s := sha256.Sum256([]byte(authKey))
	return hex.EncodeToString(s[:])
}

func passphraseKEK(pass string, k KDF) []byte {
	return argon2.IDKey([]byte(pass), k.Salt, k.Time, k.MemoryKiB, k.Threads, KeySize)
}

func recoveryKEK(rk []byte, salt []byte) []byte {
	b, err := hkdf.Key(sha256.New, rk, salt, "nulldrive/v1/recovery-kek", KeySize)
	if err != nil {
		panic(err)
	}
	return b
}

func wrap(kek, master []byte, ad string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, master, []byte(ad)), nil
}

func unwrap(kek, blob []byte, ad string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, err
	}
	if len(blob) != aead.NonceSize()+KeySize+aead.Overhead() {
		return nil, ErrBundle
	}
	m, err := aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], []byte(ad))
	if err != nil {
		return nil, ErrWrongKey
	}
	return m, nil
}

// Enable creates a new master key, wraps it under the passphrase and a
// freshly generated recovery key, and returns the bundle for the server,
// the keys, and the recovery key to show the user (once).
func Enable(passphrase string, kdf KDF) (Bundle, *Keys, string, error) {
	master := make([]byte, KeySize)
	if _, err := rand.Read(master); err != nil {
		return Bundle{}, nil, "", err
	}
	rk := make([]byte, RecoveryKeyBytes)
	if _, err := rand.Read(rk); err != nil {
		return Bundle{}, nil, "", err
	}
	keys, err := FromMaster(master)
	if err != nil {
		return Bundle{}, nil, "", err
	}
	b, err := newBundle(keys, passphrase, kdf, rk)
	if err != nil {
		return Bundle{}, nil, "", err
	}
	return b, keys, FormatRecoveryKey(rk), nil
}

func newBundle(keys *Keys, passphrase string, kdf KDF, rk []byte) (Bundle, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return Bundle{}, err
	}
	kdf.Salt = make([]byte, 16)
	if _, err := rand.Read(kdf.Salt); err != nil {
		return Bundle{}, err
	}
	if !kdf.valid() {
		return Bundle{}, ErrBundle
	}
	b := Bundle{Version: FormatVersion, KDF: kdf, RecoverySalt: make([]byte, 16), Fingerprint: keys.Fingerprint()}
	if _, err := rand.Read(b.RecoverySalt); err != nil {
		return Bundle{}, err
	}
	var err error
	if b.PassphraseWrap, err = wrap(passphraseKEK(passphrase, kdf), keys.master[:], adWrapPassphrase); err != nil {
		return Bundle{}, err
	}
	if b.RecoveryWrap, err = wrap(recoveryKEK(rk, b.RecoverySalt), keys.master[:], adWrapRecovery); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// MinPassphraseRunes is the minimum passphrase length. The passphrase
// protects the master key against offline guessing by anyone who has the
// server's database, so it must be stronger than a login password (which
// the server can rate-limit).
const MinPassphraseRunes = 10

// CheckPassphrase enforces the minimum length.
func CheckPassphrase(p string) error {
	if len([]rune(p)) < MinPassphraseRunes {
		return fmt.Errorf("e2ee: the encryption passphrase must be at least %d characters", MinPassphraseRunes)
	}
	if len(p) > 1024 {
		return errors.New("e2ee: the encryption passphrase is too long")
	}
	return nil
}

func (b Bundle) check() error {
	if b.Version != FormatVersion || !b.KDF.valid() || len(b.RecoverySalt) != 16 {
		return ErrBundle
	}
	return nil
}

func (b Bundle) unlocked(master []byte) (*Keys, error) {
	k, err := FromMaster(master)
	if err != nil {
		return nil, err
	}
	if k.Fingerprint() != b.Fingerprint {
		return nil, ErrBundle
	}
	return k, nil
}

// UnlockPassphrase unwraps the master key with the passphrase. A wrong
// passphrase returns ErrWrongKey.
func (b Bundle) UnlockPassphrase(passphrase string) (*Keys, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	m, err := unwrap(passphraseKEK(passphrase, b.KDF), b.PassphraseWrap, adWrapPassphrase)
	if err != nil {
		return nil, err
	}
	return b.unlocked(m)
}

// UnlockRecovery unwraps the master key with the recovery key.
func (b Bundle) UnlockRecovery(recoveryKey string) (*Keys, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	rk, err := ParseRecoveryKey(recoveryKey)
	if err != nil {
		return nil, err
	}
	m, err := unwrap(recoveryKEK(rk, b.RecoverySalt), b.RecoveryWrap, adWrapRecovery)
	if err != nil {
		return nil, err
	}
	return b.unlocked(m)
}

// WithPassphrase returns a copy of the bundle whose passphrase wrap is
// replaced (change passphrase, or set a new one after recovery). The
// recovery wrap is kept, so the existing recovery key stays valid.
func (b Bundle) WithPassphrase(keys *Keys, passphrase string, kdf KDF) (Bundle, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return Bundle{}, err
	}
	kdf.Salt = make([]byte, 16)
	if _, err := rand.Read(kdf.Salt); err != nil {
		return Bundle{}, err
	}
	if !kdf.valid() {
		return Bundle{}, ErrBundle
	}
	w, err := wrap(passphraseKEK(passphrase, kdf), keys.master[:], adWrapPassphrase)
	if err != nil {
		return Bundle{}, err
	}
	out := b
	out.KDF, out.PassphraseWrap, out.Fingerprint = kdf, w, keys.Fingerprint()
	return out, nil
}

// WithNewRecoveryKey replaces the recovery wrap with a new recovery key
// (for when the old one may have been exposed). Returns the new key.
func (b Bundle) WithNewRecoveryKey(keys *Keys) (Bundle, string, error) {
	rk := make([]byte, RecoveryKeyBytes)
	if _, err := rand.Read(rk); err != nil {
		return Bundle{}, "", err
	}
	out := b
	out.RecoverySalt = make([]byte, 16)
	if _, err := rand.Read(out.RecoverySalt); err != nil {
		return Bundle{}, "", err
	}
	w, err := wrap(recoveryKEK(rk, out.RecoverySalt), keys.master[:], adWrapRecovery)
	if err != nil {
		return Bundle{}, "", err
	}
	out.RecoveryWrap = w
	return out, FormatRecoveryKey(rk), nil
}

// ---- recovery key text form ----

// RecoveryKeyBytes: 160 random bits. The recovery key is not stretched
// (it is random, not chosen by a person), so its entropy alone has to
// resist offline guessing; 2^160 is far beyond reach.
const RecoveryKeyBytes = 20

// Crockford base32: no I, L, O, U, so it survives being read aloud,
// handwritten and retyped.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// FormatRecoveryKey renders 20 bytes as 32 Crockford base32 characters
// in 8 groups of 4: "7G4K-…".
func FormatRecoveryKey(b []byte) string {
	var sb strings.Builder
	var acc uint64
	bitsN := 0
	n := 0
	for _, x := range b {
		acc = acc<<8 | uint64(x)
		bitsN += 8
		for bitsN >= 5 {
			bitsN -= 5
			if n > 0 && n%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(crockford[(acc>>uint(bitsN))&31]) //nolint:gosec // bitsN in [0,7]
			n++
		}
	}
	return sb.String()
}

// ParseRecoveryKey accepts the text form leniently: any case, with or
// without dashes and spaces, and the usual look-alikes (O→0, I/L→1).
func ParseRecoveryKey(s string) ([]byte, error) {
	var acc uint64
	bitsN := 0
	out := make([]byte, 0, RecoveryKeyBytes)
	n := 0
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		i := strings.IndexRune(crockford, r)
		if i < 0 {
			return nil, ErrWrongKey
		}
		acc = acc<<5 | uint64(i) //nolint:gosec // i in [0,31]
		bitsN += 5
		n++
		if bitsN >= 8 {
			bitsN -= 8
			out = append(out, byte(acc>>uint(bitsN))) //nolint:gosec // bitsN in [0,7]
		}
	}
	if n != 32 || len(out) != RecoveryKeyBytes {
		return nil, ErrWrongKey
	}
	return out, nil
}

// ---- deterministic AEAD (SIV construction) ----

// siv seals plaintext deterministically: nonce = HMAC-SHA256(sivKey, ad ||
// 0x00 || plaintext)[:24], ciphertext = XChaCha20-Poly1305(encKey, nonce,
// plaintext, ad). Output: nonce || ciphertext || tag.
//
// Equal plaintexts give equal outputs (the point); different plaintexts
// get unrelated nonces, so XChaCha20's keystream is never reused for two
// different messages. The nonce is authenticated by Poly1305 like any
// AEAD nonce, so a modified nonce fails to open.
func siv(encKey, sivKey *[KeySize]byte, ad string, plaintext []byte) []byte {
	m := hmac.New(sha256.New, sivKey[:])
	m.Write([]byte(ad))
	m.Write([]byte{0})
	m.Write(plaintext)
	nonce := m.Sum(nil)[:chacha20poly1305.NonceSizeX]
	aead, err := chacha20poly1305.NewX(encKey[:])
	if err != nil {
		panic(err) // key size is fixed
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, []byte(ad))
}

func openSIV(encKey, sivKey *[KeySize]byte, ad string, blob []byte) ([]byte, error) {
	if len(blob) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, ErrCorrupt
	}
	aead, err := chacha20poly1305.NewX(encKey[:])
	if err != nil {
		return nil, err
	}
	nonce := blob[:chacha20poly1305.NonceSizeX]
	pt, err := aead.Open(nil, nonce, blob[chacha20poly1305.NonceSizeX:], []byte(ad))
	if err != nil {
		return nil, ErrCorrupt
	}
	// Recompute the synthetic nonce: rejects a ciphertext that is a valid
	// encryption under a nonce we would never have chosen (defence in
	// depth; Poly1305 already authenticates the nonce).
	m := hmac.New(sha256.New, sivKey[:])
	m.Write([]byte(ad))
	m.Write([]byte{0})
	m.Write(pt)
	if !hmac.Equal(m.Sum(nil)[:chacha20poly1305.NonceSizeX], nonce) {
		return nil, ErrCorrupt
	}
	return pt, nil
}
