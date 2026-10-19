// Package noteid reads Null Notes note ids from Markdown frontmatter.
//
// Null Notes keeps a stable id in each Markdown note's YAML frontmatter as
// `id: <uuid>` (null-grid/null-notes-on-disk-format.md). Null Drive stores
// it on uploaded files as synced_id, so a backed-up note and the note in
// Null Notes can be recognized as the same note (search dedupe).
package noteid

import (
	"bytes"
	"path"
	"regexp"
	"strings"
)

// MaxRead is how much of a file is looked at: the frontmatter is at the
// top, and Null Notes writes the id as its first key.
const MaxRead = 64 << 10

var (
	endRE = regexp.MustCompile(`(?m)^(---|\.\.\.)[ \t]*\r?$`)
	idRE  = regexp.MustCompile(`(?m)^id:[ \t]*["']?([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})["']?[ \t]*\r?$`)
)

// IsMarkdown reports whether a file name is a Markdown note.
func IsMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// FromMarkdown splits a leading YAML frontmatter block off b and returns
// its `id:` (a UUID, lowercased; "" if there is none) and the rest. Only a
// block that starts on the first line counts, as in Null Notes; without
// one, b is returned whole.
func FromMarkdown(b []byte) (id string, body []byte) {
	rest, ok := bytes.CutPrefix(b, []byte("---\n"))
	if !ok {
		rest, ok = bytes.CutPrefix(b, []byte("---\r\n"))
	}
	if !ok {
		return "", b
	}
	end := endRE.FindIndex(rest)
	if end == nil {
		return "", b
	}
	if m := idRE.FindSubmatch(rest[:end[0]]); m != nil {
		id = strings.ToLower(string(m[1]))
	}
	return id, rest[end[1]:]
}

// ID is FromMarkdown's id for a file named name: "" unless it's Markdown.
func ID(name string, head []byte) string {
	if !IsMarkdown(name) {
		return ""
	}
	id, _ := FromMarkdown(head)
	return id
}
