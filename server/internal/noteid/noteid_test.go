package noteid

import "testing"

func TestFromMarkdown(t *testing.T) {
	const id = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	cases := []struct{ in, want, body string }{
		{"---\nid: " + id + "\ntitle: x\n---\nHello", id, "\nHello"},
		{"---\r\nid: \"3F2504E0-4F89-11D3-9A0C-0305E82C3301\"\r\n---\r\nHi", id, "\nHi"},
		{"---\ntitle: x\nid: '" + id + "'\n...\nbody", id, "\nbody"},
		{"---\nid: not-a-uuid\n---\nb", "", "\nb"},
		{"# no frontmatter\nid: " + id + "\n", "", "# no frontmatter\nid: " + id + "\n"},
		{"\n---\nid: " + id + "\n---\n", "", "\n---\nid: " + id + "\n---\n"},
		{"---\nid: " + id + "\nunterminated", "", "---\nid: " + id + "\nunterminated"},
		{"---\nnested:\n  id: " + id + "\n---\n", "", "\n"},
	}
	for _, c := range cases {
		got, body := FromMarkdown([]byte(c.in))
		if got != c.want || string(body) != c.body {
			t.Errorf("%q: id %q body %q", c.in, got, body)
		}
	}
	if ID("a.txt", []byte("---\nid: "+id+"\n---\n")) != "" || ID("A.MD", []byte("---\nid: "+id+"\n---\n")) != id {
		t.Fatal("ID by extension")
	}
}
