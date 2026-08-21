package chstore

// v0.9.1196 — KB aday sorgusunun şekil pinleri. Kaynak metni test edilir
// çünkü sorgu inline: korunan üç şey (a) verdict=1 süzgeci, (b) GLOBAL
// NOT IN curated dışlaması (GLOBAL'siz IN dağıtıkta her shard'da ayrı
// koşar — make audit CHECK 5 sınıfı), (c) boş-cevap eleme.

import (
	"os"
	"strings"
	"testing"
)

func TestKBCandidatesSQLShape(t *testing.T) {
	b, err := os.ReadFile("ai_feedback.go")
	if err != nil {
		t.Fatalf("ai_feedback.go okunamadı: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) ListKBCandidates(")
	if i < 0 {
		t.Fatal("ListKBCandidates bulunamadı")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{
		"f.verdict = 1",
		"GLOBAL NOT IN",
		"source = 'curated'",
		"source_ref != ''",
		"c.response_sample != ''",
		"FROM ai_feedback AS f FINAL",
		"FROM rag_chunks FINAL",
		"LIMIT ?",
		"max_execution_time = 10",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ListKBCandidates %q içermeli", want)
		}
	}
}
