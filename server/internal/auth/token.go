package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// NewToken returns a random bearer token with the given prefix and the hash
// to store in the database.
//
// Tokens carry 256 bits of randomness, so a single fast SHA-256 is enough to
// store them safely: unlike passwords they cannot be brute-forced, so a slow
// KDF would add latency to every request for no security gain. Storing only
// the hash means a leaked database does not leak usable credentials.
func NewToken(prefix string) (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = prefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the hex SHA-256 used to look a token up.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
