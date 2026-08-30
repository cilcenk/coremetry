package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.194 kaynak-pini — genel bilgi cevabı (surface chat-general) KB adayı
// listesine giremez; süzgeç SQL'de, gerekçe ListKBCandidates başlığında.
func TestKBCandidatesExcludeGeneralAnswers(t *testing.T) {
	b, err := os.ReadFile("ai_feedback.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Store) ListKBCandidates(")
	if i < 0 {
		t.Fatal("ListKBCandidates bulunamadı")
	}
	if !strings.Contains(src[i:], "AND c.surface != 'chat-general'") {
		t.Fatal("ListKBCandidates chat-general cevaplarını süzmüyor — kanıtsız cevap rag_chunks'a girebilir")
	}
}
