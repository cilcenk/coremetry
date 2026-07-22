package rag

import (
	"strings"
	"testing"
)

// TestLooksLikePDF — the %PDF- magic header is authoritative; content-type and
// filename extension are hints (servers sometimes mislabel). v0.9.175.
func TestLooksLikePDF(t *testing.T) {
	cases := []struct {
		desc string
		data []byte
		ct   string
		name string
		want bool
	}{
		{"magic header", []byte("%PDF-1.7\n1 0 obj"), "", "", true},
		{"content-type", []byte("garbage"), "application/pdf", "", true},
		{"content-type with charset", nil, "application/pdf; charset=binary", "", true},
		{"extension (case-insensitive)", nil, "", "runbook.PDF", true},
		{"html is not pdf", []byte("<html>"), "text/html", "page.html", false},
		{"plain text is not pdf", []byte("hello"), "text/plain", "notes.txt", false},
		{"empty", nil, "", "", false},
	}
	for _, c := range cases {
		if got := LooksLikePDF(c.data, c.ct, c.name); got != c.want {
			t.Errorf("%s: LooksLikePDF(%q,%q,%q) = %v, want %v", c.desc, c.data, c.ct, c.name, got, c.want)
		}
	}
}

// TestExtractPDFTextBadInput — non-PDF / malformed bodies must yield an error
// or empty text, NEVER a panic. ledongthuc/pdf can panic on malformed input;
// ExtractPDFText is recover-guarded (reaching these asserts at all proves no
// panic escaped). v0.9.175.
func TestExtractPDFTextBadInput(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		[]byte(""),
		[]byte("not a pdf at all"),
		[]byte("%PDF-1.4\ntruncated garbage that is not a valid pdf body"),
	} {
		text, err := ExtractPDFText(b)
		if err == nil && strings.TrimSpace(text) != "" {
			t.Errorf("ExtractPDFText(%q): garbage yielded text %q with no error", b, text)
		}
	}
}
