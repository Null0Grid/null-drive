// Package searchtest has fixtures for search tests.
package searchtest

import (
	"bytes"
	"fmt"
	"strings"
)

// PDF returns a minimal, valid one-page PDF showing lines of text in
// Helvetica (one text object per line), with a correct xref table.
func PDF(lines ...string) []byte {
	var content strings.Builder
	content.WriteString("BT /F1 12 Tf 72 720 Td 14 TL\n")
	for _, l := range lines {
		esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(l)
		fmt.Fprintf(&content, "(%s) Tj T*\n", esc)
	}
	content.WriteString("ET")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", content.Len(), content.String()),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}
