package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1; every authenticator app uses it
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238): 6 digits, 30-second steps, HMAC-SHA1, the parameters
// every authenticator app assumes by default.
//
// This is ~30 lines over crypto/hmac and is checked against the RFC's
// published test vectors (auth_test.go). It is a protocol on top of a
// standard MAC, not new cryptography; pulling in a dependency (and its QR
// code library) for it would add more risk than it removes.
const (
	TOTPDigits = 6
	TOTPPeriod = 30
	// TOTPSkew accepts the previous and next step too: phone clocks drift
	// and people type slowly.
	TOTPSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret in base32 (RFC 4226
// recommends 160 bits).
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPURI builds the otpauth:// URI that authenticator apps import (as a
// QR code).
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(TOTPPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// HOTP is RFC 4226's HMAC-based one-time password for a counter.
func HOTP(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// TOTPStep is the time step for t.
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// TOTPCode is the code for a secret at a step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", err
	}
	return HOTP(key, uint64(step), TOTPDigits), nil //nolint:gosec // steps are positive
}

// VerifyTOTP checks code against the secret around now and returns the
// matching step. Steps at or before lastStep are rejected, so a code
// can't be replayed (someone watching over a shoulder, a phished code
// used twice).
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := int64(-TOTPSkew); d <= TOTPSkew; d++ {
		step := cur + d
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// NewRecoveryCodes returns n single-use recovery codes ("xxxxx-xxxxx",
// 50 bits each from Crockford-style lowercase base32).
func NewRecoveryCodes(n int) ([]string, error) {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	out := make([]string, n)
	for i := range out {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		var sb strings.Builder
		for j, x := range b {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[x&31])
		}
		out[i] = sb.String()
	}
	return out, nil
}

// NormalizeRecoveryCode lowercases and strips spaces and dashes.
func NormalizeRecoveryCode(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	c = strings.NewReplacer("-", "", " ", "").Replace(c)
	return c
}

// HashRecoveryCode hashes a normalized code for storage. Recovery codes
// are random (50 bits) and single-use, and attempts are rate limited, so
// a plain SHA-256 is enough (unlike a chosen password).
func HashRecoveryCode(c string) string {
	s := sha256.Sum256([]byte("nulldrive-recovery:" + NormalizeRecoveryCode(c)))
	return hex.EncodeToString(s[:])
}
