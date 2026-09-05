package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.414 — A7: GetLogs SkipTotal'da count() sorgusunu HİÇ koşmaz.
func TestGetLogsSkipsCountOnSkipTotal(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) GetLogs(")
	if i < 0 {
		t.Fatal("GetLogs yok")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	// v0.10.420 — KAPSAMA: dalın gövdesi (aynı girintideki kapanış
	// süslüsüne dek) count() sorgusunu içermeli; sıralama yetmezdi.
	i = strings.Index(body, "\tif !f.SkipTotal {")
	if i < 0 {
		t.Fatal("GetLogs'ta `if !f.SkipTotal {` yok")
	}
	block := body[i:]
	if end := strings.Index(block, "\n\t}\n"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "SELECT count() FROM (SELECT 1 FROM logs") {
		t.Fatal("count() sorgusu !f.SkipTotal dalının İÇİNDE değil")
	}
	if strings.Count(body, "SELECT count() FROM (SELECT 1 FROM logs") != 1 {
		t.Fatal("count() sorgusu dal dışında da var")
	}
}
