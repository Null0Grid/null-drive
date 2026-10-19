// Package auth implements password hashing and opaque bearer tokens.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id cost parameters.
//
// Defaults follow RFC 9106's second recommended option (memory-constrained):
// t=3 passes over 64 MiB with 4 lanes. That costs tens of milliseconds on a
// Raspberry Pi 4 class device, acceptable for a login, while making
// GPU/ASIC guessing expensive because every guess needs 64 MiB of RAM.
type Params struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

var DefaultParams = Params{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16}

// TestParams are deliberately cheap; never use them outside tests.
var TestParams = Params{Time: 1, Memory: 8, Threads: 1, KeyLen: 32, SaltLen: 16}

var ErrMalformedHash = errors.New("malformed password hash")

// Hasher hashes and verifies passwords.
type Hasher struct{ P Params }

// Hash returns a self-describing PHC-format string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt b64>$<key b64>
//
// Parameters travel with the hash, so they can be raised later without
// invalidating existing passwords.
func (h Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.P.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, h.P.Time, h.P.Memory, h.P.Threads, h.P.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.P.Memory, h.P.Time, h.P.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded. The comparison is
// constant-time so response timing does not leak how many bytes matched.
func (Hasher) Verify(password, encoded string) (bool, error) {
	p, salt, key, err := decode(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(key))) //nolint:gosec // len bounded to 1024 in decode
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 || len(key) > 1024 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
