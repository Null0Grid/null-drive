package auth

import (
	"crypto/rand"
	"math/big"
	"strings"
)

// Pairing codes look like NULL-7K2Q-9F: a fixed "NULL" prefix (so the
// code is recognisable when read aloud or pasted into the wrong box) and
// six random characters from Crockford's base32 alphabet, which omits
// I, L, O and U so nothing is ambiguous to read or type.
//
// Six characters = 32^6 ≈ 1.07 billion codes. That is far too few for an
// offline-guessable secret, and it does not need to be: a code lives for
// five minutes, is single-use, and the claim endpoint is rate limited
// (see DESIGN.md "Pairing threat model" for the arithmetic).
const (
	pairingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	pairingLen      = 6
	pairingPrefix   = "NULL"
)

// NewPairingCode returns a display-formatted code and the hash to store.
func NewPairingCode() (code, hash string, err error) {
	b := make([]byte, pairingLen)
	max := big.NewInt(int64(len(pairingAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", "", err
		}
		b[i] = pairingAlphabet[n.Int64()]
	}
	raw := string(b)
	return pairingPrefix + "-" + raw[:4] + "-" + raw[4:], HashPairingCode(raw), nil
}

// NormalizePairingCode turns user input ("null 7k2q 9f", "7K2Q9F",
// "NULL-7K2Q-9F", with O/I/L typed for 0/1/1) into the six canonical
// characters. ok is false if the input cannot be a code.
func NormalizePairingCode(in string) (string, bool) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '\t':
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(in)))
	// Strip the prefix before mapping look-alikes: "NULL" contains L.
	if len(s) == len(pairingPrefix)+pairingLen && strings.HasPrefix(s, pairingPrefix) {
		s = s[len(pairingPrefix):]
	}
	if len(s) != pairingLen {
		return "", false
	}
	out := []byte(s)
	for i, c := range out {
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if !strings.ContainsRune(pairingAlphabet, rune(c)) {
			return "", false
		}
		out[i] = c
	}
	return string(out), true
}

// HashPairingCode hashes a normalized code for storage. A plain SHA-256
// is enough here: unlike a password, the code is worthless after five
// minutes, so an attacker who steals the database gains nothing by
// brute-forcing the hashes offline.
func HashPairingCode(normalized string) string { return HashToken("pair:" + normalized) }
