package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"

	"github.com/nulldrive/nulldrive/server/internal/noteid"
)

// What gets read from a file, and how much.
const (
	// maxTextBytes caps the text indexed per file. Enough for any note or
	// document people search; keeps one huge log file from bloating the
	// index (a trigram index is ~3x the text).
	maxTextBytes = 512 << 10
	// maxPlainRead is how much of a .md/.txt file is read.
	maxPlainRead = 4 << 20
	// maxPDFSize: larger PDFs are indexed by name only (they're usually
	// scans or image-heavy, and parsing them is slow).
	maxPDFSize = 64 << 20
	// pdfTimeout bounds text extraction of one PDF.
	pdfTimeout = 20 * time.Second
)

// lockedMagic starts a Null Notes locked note ("<note>.<ext>.lock"). The
// Notes format says locked notes are never indexed or returned by search,
// so such files are skipped entirely, name included.
var lockedMagic = []byte("NNLOCK1\n")

type kind int

const (
	kindNameOnly kind = iota
	kindMarkdown
	kindText
	kindPDF
)

func kindOf(name string) kind {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return kindMarkdown
	case ".txt":
		return kindText
	case ".pdf":
		return kindPDF
	}
	return kindNameOnly
}

// extracted is what indexing needs from a file's content.
type extracted struct {
	body   string
	fmID   string // `id:` from Markdown frontmatter (a Null Notes note id)
	locked bool   // a Null Notes locked note: don't index at all
}

// extract turns content into indexable text. read returns up to n bytes
// of the file.
func extract(ctx context.Context, name string, size int64, read func(n int64) ([]byte, error)) (extracted, error) {
	if strings.HasSuffix(strings.ToLower(name), ".lock") {
		head, err := read(int64(len(lockedMagic)))
		if err != nil {
			return extracted{}, err
		}
		if bytes.Equal(head, lockedMagic) {
			return extracted{locked: true}, nil
		}
	}
	switch kindOf(name) {
	case kindMarkdown, kindText:
		b, err := read(maxPlainRead)
		if err != nil {
			return extracted{}, err
		}
		var e extracted
		if kindOf(name) == kindMarkdown {
			e.fmID, b = frontmatter(b)
		}
		e.body = clip(cleanText(b))
		return e, nil
	case kindPDF:
		if size > maxPDFSize {
			return extracted{}, nil
		}
		b, err := read(size)
		if err != nil {
			return extracted{}, err
		}
		text, err := pdfText(ctx, b)
		if err != nil {
			return extracted{}, err
		}
		return extracted{body: clip(text)}, nil
	}
	return extracted{}, nil
}

// frontmatter splits off Markdown frontmatter and returns its note id.
func frontmatter(b []byte) (string, []byte) { return noteid.FromMarkdown(b) }

// cleanText makes bytes safe to index: valid UTF-8, no NULs.
func cleanText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "�")
	return strings.ReplaceAll(s, "\x00", " ")
}

// clip cuts s to maxTextBytes at a rune boundary.
func clip(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	cut := maxTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// pdfText extracts a PDF's text. The parser is pure Go but handles hostile
// or broken files by panicking or looping, so it runs with a recover and a
// time limit; a PDF that fails is indexed by name only.
func pdfText(ctx context.Context, b []byte) (string, error) {
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("pdf: %v", r)}
			}
		}()
		r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			done <- result{err: err}
			return
		}
		tr, err := r.GetPlainText()
		if err != nil {
			done <- result{err: err}
			return
		}
		out, err := io.ReadAll(io.LimitReader(tr, maxTextBytes))
		done <- result{text: cleanText(out), err: err}
	}()
	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	select {
	case r := <-done:
		return r.text, r.err
	case <-ctx.Done():
		return "", errors.New("pdf: text extraction timed out")
	}
}
