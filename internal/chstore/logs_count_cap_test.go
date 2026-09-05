package chstore

// v0.10.382 — dış skill denetimi C5: GetLogs'un toplam sayacı tavanlı.
// Sınırsız count() her sayfa açılışında eşleşen tüm pencereyi tarıyordu.

import (
	"strconv"
	"strings"
	"testing"
)

func TestGetLogsCountIsCapped(t *testing.T) {
	src, err := readRepoFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(src, "func (s *Store) GetLogs(")
	if i < 0 {
		t.Fatal("GetLogs bulunamadı")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if strings.Contains(body, `"SELECT count() FROM logs "+wc.sql()`) {
		t.Fatal("GetLogs hâlâ sınırsız count() koşuyor")
	}
	if !strings.Contains(body, `SELECT count() FROM (SELECT 1 FROM logs "+wc.sql()+" LIMIT "+strconv.Itoa(LogsCountCap)+")`) {
		t.Fatal("GetLogs sayacı LogsCountCap alt sorgusuyla tavanlanmalı")
	}
	if LogsCountCap != 100000 {
		t.Fatalf("LogsCountCap = %d, want 100000 (UI '100k+' beyanı)", LogsCountCap)
	}
	_ = strconv.Itoa
}
