package search

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Matching rules (search contract v1: case-insensitive, "fuzzy" on title
// and content, SQLite FTS5 trigram as the reference).
//
// The trigram tokenizer gives case-insensitive *substring* matching, which
// is what makes "libre" find "LibreAI". Two gaps are closed here:
//
//   - Terms shorter than 3 characters have no trigrams, so a MATCH on them
//     finds nothing. They're matched with LIKE instead (a scan of one
//     user's index: fine at personal scale).
//   - Separators: "libre ai" / "libre-ai" / "LibreAI" should find each
//     other. The query is split into words on anything that isn't a letter
//     or digit, and every word must occur (AND). Titles are also indexed
//     squashed (lowercase, letters and digits only) and the squashed query
//     is matched against them, so "libreai" finds "Libre AI.md".
//
// It is substring matching, not typo tolerance: "libre" finds "LibreAI",
// "lirbe" doesn't.

const (
	maxTerms   = 10
	maxTermLen = 64
)

type query struct {
	terms  []string // lowercased words
	squash string   // terms joined, for the title-squash match
}

func parseQuery(q string) query {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out query
	seen := map[string]bool{}
	for _, w := range words {
		if utf8.RuneCountInString(w) > maxTermLen {
			w = string([]rune(w)[:maxTermLen])
		}
		if seen[w] {
			continue
		}
		seen[w] = true
		out.terms = append(out.terms, w)
		if len(out.terms) == maxTerms {
			break
		}
	}
	out.squash = squash(strings.Join(out.terms, ""))
	return out
}

func (q query) empty() bool { return len(q.terms) == 0 }

// squash lowercases and keeps letters and digits only.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func long(t string) bool { return utf8.RuneCountInString(t) >= 3 }

// ftsPhrase quotes a term as an FTS5 string (quotes doubled), so user
// input can never be read as FTS5 query syntax.
func ftsPhrase(t string) string { return `"` + strings.ReplaceAll(t, `"`, `""`) + `"` }

// likePattern escapes a term for LIKE ... ESCAPE '\'.
func likePattern(t string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(t) + "%"
}

// termsMatch builds the AND-of-terms query: an FTS5 MATCH expression over
// name and body for the long terms (empty if none), plus LIKE conditions
// and their arguments for the short ones.
func (q query) termsMatch() (match string, likes []string, args []any) {
	var parts []string
	for _, t := range q.terms {
		if long(t) {
			parts = append(parts, "{name body} : "+ftsPhrase(t))
			continue
		}
		p := likePattern(t)
		likes = append(likes, `(name LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\')`)
		args = append(args, p, p)
	}
	return strings.Join(parts, " AND "), likes, args
}

// titleSquashMatch is the MATCH for the squashed title column, or "".
func (q query) titleSquashMatch() string {
	if !long(q.squash) {
		return ""
	}
	return "{key} : " + ftsPhrase(q.squash)
}

// nameHit reports whether the title alone matches (all terms, or the
// squashed query). Title hits rank first.
func (q query) nameHit(name string) bool {
	if q.squash != "" && strings.Contains(squash(name), q.squash) {
		return true
	}
	low := strings.ToLower(name)
	for _, t := range q.terms {
		if !strings.Contains(low, t) {
			return false
		}
	}
	return true
}

// snippet returns a short window of body around the first match of the
// longest term that occurs, whitespace collapsed, or "" if none does
// (the hit was on the title).
func (q query) snippet(body string) string {
	terms := append([]string(nil), q.terms...)
	for i := 1; i < len(terms); i++ { // longest first: the most specific context
		for j := i; j > 0 && len(terms[j]) > len(terms[j-1]); j-- {
			terms[j], terms[j-1] = terms[j-1], terms[j]
		}
	}
	for _, t := range terms {
		loc := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(t)).FindStringIndex(body)
		if loc == nil {
			continue
		}
		const before, after = 60, 100
		start, end := loc[0]-before, loc[1]+after
		prefix, suffix := "…", "…"
		if start <= 0 {
			start, prefix = 0, ""
		}
		if end >= len(body) {
			end, suffix = len(body), ""
		}
		for start > 0 && !utf8.RuneStart(body[start]) {
			start--
		}
		for end < len(body) && !utf8.RuneStart(body[end]) {
			end++
		}
		return prefix + strings.Join(strings.Fields(body[start:end]), " ") + suffix
	}
	return ""
}
