package rag

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// pdfReadMax bounds a fetched PDF (crawl path) and the extracted text. Uploads
// are additionally bounded by ragMaxUploadBytes at the API layer.
const pdfReadMax = 8 << 20 // 8MB

// LooksLikePDF reports whether the bytes / content-type / name indicate a PDF.
// The %PDF- magic header is authoritative; content-type + extension are hints
// (servers sometimes mislabel a PDF as octet-stream).
func LooksLikePDF(data []byte, contentType, name string) bool {
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return true
	}
	if strings.Contains(strings.ToLower(contentType), "application/pdf") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(name), ".pdf")
}

// ExtractPDFText pulls the plain text out of a text-based PDF. Pure-Go
// (ledongthuc/pdf, no cgo) so the single static binary + air-gapped installs
// keep working. Scanned / image-only PDFs have no text layer → returns "" (no
// OCR). ledongthuc/pdf can panic on malformed input, so extraction is
// recover-guarded: a bad PDF becomes an error, never a crashed ingest.
func ExtractPDFText(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("pdf ayrıştırma paniği: %v", r)
		}
	}()
	rd, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf aç: %w", err)
	}
	tr, err := rd.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("pdf metin: %w", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(tr, pdfReadMax)); err != nil {
		return "", fmt.Errorf("pdf oku: %w", err)
	}
	return buf.String(), nil
}
