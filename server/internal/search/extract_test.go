package search

import (
	"context"
	"strings"
	"testing"

	"github.com/Null0Grid/null-drive/server/internal/search/searchtest"
)

func readerOf(b []byte) func(int64) ([]byte, error) {
	return func(n int64) ([]byte, error) {
		if int64(len(b)) > n {
			return b[:n], nil
		}
		return b, nil
	}
}

func TestExtract(t *testing.T) {
	ctx := context.Background()
	md := []byte("---\ntitle: Plan\nid: 3F2504E0-4F89-11D3-9A0C-0305E82C3301\n---\n# Plan\nShip the LibreAI demo.\n")
	e, err := extract(ctx, "plan.md", int64(len(md)), readerOf(md))
	if err != nil {
		t.Fatal(err)
	}
	if e.fmID != "3f2504e0-4f89-11d3-9a0c-0305e82c3301" || strings.Contains(e.body, "title:") || !strings.Contains(e.body, "LibreAI demo") {
		t.Fatalf("markdown: %+v", e)
	}

	// No frontmatter / not at the top / unterminated: body kept whole.
	for _, s := range []string{"# hi\nid: 3f2504e0-4f89-11d3-9a0c-0305e82c3301\n", "\n---\nid: x\n---\n", "---\nid: 1\nno end"} {
		e, _ := extract(ctx, "a.md", int64(len(s)), readerOf([]byte(s)))
		if e.fmID != "" || e.body != s {
			t.Fatalf("%q: %+v", s, e)
		}
	}

	txt := []byte("hello\x00world \xff")
	e, _ = extract(ctx, "a.txt", int64(len(txt)), readerOf(txt))
	if e.body != "hello world �" {
		t.Fatalf("txt: %q", e.body)
	}

	pdf := searchtest.PDF("Quarterly invoice", "Payable to Zebra Corp (net 30)")
	e, err = extract(ctx, "inv.pdf", int64(len(pdf)), readerOf(pdf))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.body, "Quarterly invoice") || !strings.Contains(e.body, "Zebra Corp") {
		t.Fatalf("pdf text: %q", e.body)
	}

	if _, err := extract(ctx, "bad.pdf", 9, readerOf([]byte("%PDF-junk"))); err == nil {
		t.Fatal("broken pdf: want an error")
	}

	lock := []byte("NNLOCK1\nciphertext")
	if e, _ := extract(ctx, "secret.md.lock", int64(len(lock)), readerOf(lock)); !e.locked {
		t.Fatal("locked note not detected")
	}
	if e, _ := extract(ctx, "yarn.lock", 4, readerOf([]byte("deps"))); e.locked {
		t.Fatal("ordinary .lock file treated as a locked note")
	}
	if e, _ := extract(ctx, "photo.jpg", 3, readerOf([]byte("abc"))); e.body != "" {
		t.Fatal("other types: name only")
	}
	big := strings.Repeat("é", maxTextBytes)
	if e, _ := extract(ctx, "big.txt", int64(len(big)), readerOf([]byte(big))); len(e.body) > maxTextBytes || !strings.HasPrefix(big, e.body) {
		t.Fatalf("clip: %d bytes", len(e.body))
	}
}

func TestQuery(t *testing.T) {
	q := parseQuery("  Libre-AI  notes libre ")
	if strings.Join(q.terms, ",") != "libre,ai,notes" || q.squash != "libreainotes" {
		t.Fatalf("%+v", q)
	}
	if !parseQuery("").empty() || !parseQuery(" -- ").empty() {
		t.Fatal("empty query")
	}
	m, likes, args := parseQuery(`zebra "x" a%`).termsMatch()
	if m != `{name body} : "zebra"` || len(likes) != 2 || args[0] != "%x%" || args[2] != `%a%` {
		t.Fatalf("%q %v %v", m, likes, args)
	}
	if !parseQuery("libre ai").nameHit("LibreAI.md") || !parseQuery("libreai").nameHit("Libre AI notes.md") || parseQuery("zebra").nameHit("a.md") {
		t.Fatal("nameHit")
	}
	body := strings.Repeat("filler ", 30) + "the ZEBRA crossed\n\n the   road" + strings.Repeat(" more", 40)
	s := parseQuery("zebra").snippet(body)
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "the ZEBRA crossed the road") {
		t.Fatalf("snippet %q", s)
	}
	if parseQuery("nothing").snippet(body) != "" {
		t.Fatal("snippet without a match")
	}
}
