package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h := Hasher{P: TestParams}
	encoded, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", encoded)
	}
	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{"correct", "correct horse battery staple", true},
		{"wrong", "Correct horse battery staple", false},
		{"empty", "", false},
		{"prefix", "correct horse", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := h.Verify(tt.password, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.want {
				t.Fatalf("Verify = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestHashIsSalted(t *testing.T) {
	h := Hasher{P: TestParams}
	a, _ := h.Hash("same")
	b, _ := h.Hash("same")
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt missing")
	}
}

func TestVerifyUsesStoredParams(t *testing.T) {
	// A hash created with one cost must verify after defaults change.
	old, _ := Hasher{P: TestParams}.Hash("pw")
	ok, err := Hasher{P: Params{Time: 5, Memory: 16, Threads: 2, KeyLen: 32, SaltLen: 16}}.Verify("pw", old)
	if err != nil || !ok {
		t.Fatalf("verify with different hasher params: %v %v", ok, err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"plaintext",
		"$bcrypt$...",
		"$argon2id$v=18$m=8,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$garbage$c2FsdA$a2V5",
		"$argon2id$v=19$m=8,t=1,p=1$!!!$a2V5",
		"$argon2id$v=19$m=8,t=1,p=1$c2FsdA$",
	} {
		if _, err := (Hasher{P: TestParams}).Verify("pw", bad); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("Verify(%q) err = %v, want ErrMalformedHash", bad, err)
		}
	}
}

func TestNewToken(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, hash, err := NewToken("ndd_")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tok, "ndd_") || len(tok) != 4+43 {
			t.Fatalf("bad token %q", tok)
		}
		if HashToken(tok) != hash || len(hash) != 64 {
			t.Fatalf("hash mismatch for %q", tok)
		}
		if strings.Contains(hash, tok) {
			t.Fatal("hash contains token")
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}

func TestPairingCodes(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, hash, err := NewPairingCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != len("NULL-7K2Q-9F") || code[:5] != "NULL-" || code[9] != '-' {
			t.Fatalf("bad format %q", code)
		}
		n, ok := NormalizePairingCode(code)
		if !ok || HashPairingCode(n) != hash {
			t.Fatalf("round trip failed for %q", code)
		}
		if seen[n] {
			t.Fatalf("duplicate code %q in 200 draws", n)
		}
		seen[n] = true
	}
	for in, want := range map[string]string{
		"NULL-7K2Q-9F": "7K2Q9F",
		"null 7k2q 9f": "7K2Q9F",
		"7k2q9f":       "7K2Q9F",
		" 7K2Q-9F ":    "7K2Q9F",
		"NULL-0IL0-AB": "0110AB",
		"NULL-OOOO-OO": "000000",
		"NULLAB12CD":   "AB12CD",
	} {
		got, ok := NormalizePairingCode(in)
		if !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "NULL", "7K2Q9", "7K2Q9FF", "7K2Q9U", "NULL-7K2Q-9F-X", "ÄK2Q9F"} {
		if got, ok := NormalizePairingCode(bad); ok {
			t.Errorf("Normalize(%q) accepted as %q", bad, got)
		}
	}
}

// RFC 6238 Appendix B test vectors (SHA-1, 8 digits, secret
// "12345678901234567890").
func TestTOTPRFCVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"},
		{1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		if got := HOTP(key, uint64(tc.unix/30), 8); got != tc.want {
			t.Errorf("t=%d: %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	step := TOTPStep(now)
	code, _ := TOTPCode(secret, step)
	if got, ok := VerifyTOTP(secret, code, now, 0); !ok || got != step {
		t.Fatal("current code rejected")
	}
	if _, ok := VerifyTOTP(secret, code[:3]+" "+code[3:], now, 0); !ok {
		t.Fatal("code with a space rejected")
	}
	if _, ok := VerifyTOTP(secret, code, now, step); ok {
		t.Fatal("replayed code accepted")
	}
	old, _ := TOTPCode(secret, step-2)
	if _, ok := VerifyTOTP(secret, old, now, 0); ok {
		t.Fatal("code from a minute ago accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := VerifyTOTP(secret, bad, now, 0); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
	codes, _ := NewRecoveryCodes(10)
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] {
			t.Fatalf("recovery code %q", c)
		}
		seen[c] = true
		if HashRecoveryCode(strings.ToUpper(c)) != HashRecoveryCode(strings.ReplaceAll(c, "-", "")) {
			t.Fatal("recovery code normalization")
		}
	}
}
