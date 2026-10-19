package sync

import (
	"bufio"
	"io"
	"path"
	"strings"
)

// IgnoreFile is the per-folder rules file, synced like any other file so
// every device applies the same rules.
const IgnoreFile = ".nullignore"

// partSuffix marks in-progress downloads (written next to the target, then
// renamed into place). They are always ignored.
const partSuffix = ".nulldrive-part"

// DefaultIgnore are editor/OS temp files that should never sync: they are
// noisy, machine-specific, and often locked while in use.
var DefaultIgnore = []string{
	"~$*",         // Office lock files
	"*.tmp",       // generic temp files
	"*.swp",       // vim swap
	".~lock.*#",   // LibreOffice lock files
	".DS_Store",   // macOS Finder metadata
	"._*",         // macOS resource forks on non-HFS volumes
	"Thumbs.db",   // Windows thumbnail cache
	"desktop.ini", // Windows folder settings
	"*" + partSuffix,
	"*" + partSuffix + ".state*", // resume state of a chunked download
}

// Ignore matches paths against gitignore-style rules (a deliberately small
// subset):
//
//	# comment          blank lines and comments are skipped
//	*.log              no '/': matches the base name at any depth
//	build/             trailing '/': folders only (and everything inside)
//	docs/drafts/*.md   contains '/': matched against the full path
//	!keep.log          negation: re-include something an earlier rule excluded
//
// Matching is case-insensitive, because the same rules must behave the same
// on Windows/macOS (case-insensitive) and Linux.
type Ignore struct {
	rules []rule
}

type rule struct {
	pattern  string
	dirOnly  bool
	anchored bool // contains a '/': match the full path
	negate   bool
}

// NewIgnore builds rules from the defaults plus extra lines (typically the
// contents of .nullignore).
func NewIgnore(extra []string) *Ignore {
	ig := &Ignore{}
	for _, l := range append(append([]string{}, DefaultIgnore...), extra...) {
		ig.add(l)
	}
	return ig
}

// ParseIgnore reads .nullignore content.
func ParseIgnore(r io.Reader) []string {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func (ig *Ignore) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	r := rule{}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	line = strings.TrimPrefix(line, "/")
	r.anchored = strings.Contains(line, "/")
	r.pattern = strings.ToLower(line)
	if r.pattern != "" {
		ig.rules = append(ig.rules, r)
	}
}

// Match reports whether p (server-style path) is ignored. A path is also
// ignored if any parent folder is, so excluding "node_modules/" excludes
// its whole subtree.
func (ig *Ignore) Match(p string, isDir bool) bool {
	parts := strings.Split(p, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		dir := isDir || i < len(parts)-1
		if ig.matchOne(prefix, dir) {
			return true
		}
	}
	return false
}

func (ig *Ignore) matchOne(p string, isDir bool) bool {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	ignored := false
	for _, r := range ig.rules {
		if r.dirOnly && !isDir {
			continue
		}
		target := base
		if r.anchored {
			target = lower
		}
		if ok, _ := path.Match(r.pattern, target); ok {
			ignored = !r.negate
		}
	}
	return ignored
}
