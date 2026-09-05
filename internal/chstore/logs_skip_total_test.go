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
	if !strings.Contains(body, "if !f.SkipTotal {") || !strings.Contains(body, "SELECT count() FROM (SELECT 1 FROM logs") {
		t.Fatal("GetLogs count() sorgusu !f.SkipTotal dalında olmalı")
	}
	if strings.Index(body, "if !f.SkipTotal {") > strings.Index(body, "SELECT count() FROM (SELECT 1 FROM logs") {
		t.Fatal("SkipTotal dalı count() sorgusunu sarmalamıyor")
	}
}
