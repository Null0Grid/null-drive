package pathutil

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"simple file", "a.txt", "a.txt", nil},
		{"nested", "Photos/2024/beach.jpg", "Photos/2024/beach.jpg", nil},
		{"backslashes normalized", `docs\notes\todo.md`, "docs/notes/todo.md", nil},
		{"double slashes collapsed", "a//b///c", "a/b/c", nil},
		{"dot segments dropped", "./a/./b", "a/b", nil},
		{"trailing slash dropped", "a/b/", "a/b", nil},
		{"unicode ok", "Ünïcødé/日本語.txt", "Ünïcødé/日本語.txt", nil},
		{"dotfile ok", ".config/app", ".config/app", nil},
		{"reserved-looking but fine", "console.log", "console.log", nil},

		{"empty", "", "", ErrEmpty},
		{"only dots", "./.", "", ErrEmpty},
		{"only slashes", "///", "", ErrAbsolute},
		{"absolute unix", "/etc/passwd", "", ErrAbsolute},
		{"absolute backslash", `\Windows\system32`, "", ErrAbsolute},
		{"drive letter", "C:/Windows", "", ErrAbsolute},
		{"drive relative", "c:foo", "", ErrAbsolute},
		{"UNC", `\\server\share`, "", ErrAbsolute},

		{"parent at start", "../secret", "", ErrTraversal},
		{"parent in middle", "a/../../b", "", ErrTraversal},
		{"parent at end", "a/..", "", ErrTraversal},
		{"parent backslash", `a\..\..\b`, "", ErrTraversal},

		{"null byte", "a\x00b", "", ErrInvalid},
		{"control char", "a\nb", "", ErrInvalid},
		{"colon", "notes:2024/c", "", ErrInvalid},
		{"question mark", "what?.txt", "", ErrInvalid},
		{"trailing dot", "folder./x", "", ErrInvalid},
		{"trailing space", "name ", "", ErrInvalid},
		{"reserved name", "CON", "", ErrInvalid},
		{"reserved name lowercase ext", "docs/nul.txt", "", ErrInvalid},
		{"reserved com port", "com1.log", "", ErrInvalid},
		{"invalid utf8", "a\xffb", "", ErrInvalid},
		{"segment too long", strings.Repeat("x", 256), "", ErrTooLong},
		{"path too long", strings.Repeat("abcdefgh/", 600), "", ErrTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Clean(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Clean(%q) err = %v, want %v", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Clean(%q) unexpected err: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAncestors(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a.txt", nil},
		{"a/b.txt", []string{"a"}},
		{"a/b/c.txt", []string{"a", "a/b"}},
	}
	for _, tt := range tests {
		if got := Ancestors(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Ancestors(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsWithin(t *testing.T) {
	tests := []struct {
		child, parent string
		want          bool
	}{
		{"a/b", "a", true},
		{"a/b/c", "a", true},
		{"a", "a", false},
		{"ab/c", "a", false},
	}
	for _, tt := range tests {
		if got := IsWithin(tt.child, tt.parent); got != tt.want {
			t.Errorf("IsWithin(%q, %q) = %v, want %v", tt.child, tt.parent, got, tt.want)
		}
	}
}
