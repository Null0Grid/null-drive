// Package pathutil validates and normalizes the user-visible paths that
// clients send to the server (e.g. "Photos/2024/beach.jpg").
//
// Paths are always relative, use forward slashes, and must be safe to
// materialize on every platform the desktop client supports. Windows is the
// strictest target, so its rules (reserved names, forbidden characters,
// trailing dots/spaces) are enforced for everyone: a file that the server
// accepts but a Windows client cannot create would be an unsyncable file.
//
// The package lives in shared/ because the desktop client applies the exact
// same rules before uploading, so both sides agree on what a valid path is.
package pathutil

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxPathBytes caps the full normalized path.
	MaxPathBytes = 4096
	// MaxSegmentBytes caps a single path component (common FS limit).
	MaxSegmentBytes = 255
	// End-to-end encrypted names ("~" + base64url, see shared/e2ee) are
	// longer than the plaintext they hide: a 255-byte name becomes 396
	// bytes. Paths made only of such segments get these limits instead;
	// the client checks the plaintext against the normal ones first.
	MaxEncryptedSegmentBytes = 400
	MaxEncryptedPathBytes    = 16384
)

var (
	ErrEmpty     = errors.New("path is empty")
	ErrAbsolute  = errors.New("path must be relative")
	ErrTraversal = errors.New("path must not contain '..'")
	ErrInvalid   = errors.New("path contains invalid characters or names")
	ErrTooLong   = errors.New("path is too long")
)

// windowsReserved are device names that cannot be used as a file name on
// Windows, with or without an extension ("nul.txt" is also reserved).
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

const forbiddenChars = `<>:"|?*`

// Clean validates p and returns its canonical form: forward slashes, no
// leading/trailing slash, no empty or "." segments. It rejects anything that
// could escape the user's namespace ("..", absolute paths, drive letters) and
// anything a supported OS cannot store.
func Clean(p string) (string, error) {
	if p == "" {
		return "", ErrEmpty
	}
	if strings.IndexByte(p, 0) >= 0 {
		return "", fmt.Errorf("%w: null byte", ErrInvalid)
	}
	if !utf8.ValidString(p) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalid)
	}

	p = strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(p, "/") {
		return "", ErrAbsolute
	}
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return "", ErrAbsolute // "C:foo" or "C:/foo"
	}

	raw := strings.Split(p, "/")
	segs := make([]string, 0, len(raw))
	for _, s := range raw {
		switch s {
		case "", ".":
			continue // collapse "a//b" and "a/./b"
		case "..":
			return "", ErrTraversal
		}
		if err := checkSegment(s); err != nil {
			return "", err
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", ErrEmpty
	}
	out := strings.Join(segs, "/")
	limit := MaxPathBytes
	if allEncryptedShape(segs) {
		limit = MaxEncryptedPathBytes
	}
	if len(out) > limit {
		return "", ErrTooLong
	}
	return out, nil
}

func checkSegment(s string) error {
	if len(s) > MaxSegmentBytes && (len(s) > MaxEncryptedSegmentBytes || !encryptedShape(s)) {
		return ErrTooLong
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: control character in %q", ErrInvalid, s)
		}
		if strings.ContainsRune(forbiddenChars, r) {
			return fmt.Errorf("%w: %q not allowed in %q", ErrInvalid, r, s)
		}
	}
	if last := s[len(s)-1]; last == '.' || last == ' ' {
		return fmt.Errorf("%w: %q ends with a dot or space", ErrInvalid, s)
	}
	base, _, _ := strings.Cut(s, ".")
	if windowsReserved[strings.ToUpper(strings.TrimRight(base, " "))] {
		return fmt.Errorf("%w: %q is a reserved name", ErrInvalid, s)
	}
	return nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// Ancestors returns every parent folder of a cleaned path, shortest first:
// "a/b/c.txt" -> ["a", "a/b"].
func Ancestors(p string) []string {
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

// IsWithin reports whether child is strictly inside the folder parent.
func IsWithin(child, parent string) bool {
	return strings.HasPrefix(child, parent+"/")
}

// encryptedShape: "~" followed by base64url characters only. (The exact
// format check is e2ee.IsEncryptedName; this only decides length limits.)
func encryptedShape(s string) bool {
	if len(s) < 2 || s[0] != '~' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isB64URL(c) {
			return false
		}
	}
	return true
}

func allEncryptedShape(segs []string) bool {
	for _, s := range segs {
		if !encryptedShape(s) {
			return false
		}
	}
	return len(segs) > 0
}

func isB64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
