package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.523 — mergeExceptionGroup UpsertExceptionGroup'un içinden ayrıldı ve
// artık SAF, yani ilk kez test edilebilir.
//
// Neden önemli: exception_groups ReplacingMergeTree, TAM SATIR replace.
// Upsert kendi üretmediği alanları ileri taşımazsa operatörün yazdığı not,
// atadığı kişi, sustuğu grup ve AI özeti SESSİZCE silinir. Bu mantık
// bugüne kadar bir store okumasıyla iç içeydi ve hiç pinlenmemişti.

func TestMergeExceptionGroupCarriesOperatorState(t *testing.T) {
	resolved := int64(1000)
	existing := &ExceptionGroup{
		Fingerprint: "fp", State: ExStateIgnored, Assignee: "cenk",
		Notes: "biliniyor", AISummary: "heap doldu", FirstSeen: 100,
		Occurrences: 500, ResolvedAt: &resolved,
	}
	fresh := ExceptionGroup{Fingerprint: "fp", LastSeen: 200, Occurrences: 3}

	got := mergeExceptionGroup(fresh, existing)

	if got.Assignee != "cenk" || got.Notes != "biliniyor" {
		t.Errorf("operatörün atadığı/yazdığı alanlar silinmiş: %+v", got)
	}
	if got.AISummary != "heap doldu" {
		t.Error("AI özeti silinmiş — refresher onu ezmemeli (v0.9.415)")
	}
	if got.FirstSeen != 100 {
		t.Errorf("first_seen ileri taşınmamış: %d", got.FirstSeen)
	}
	if got.State != ExStateIgnored {
		t.Errorf("ignored durumu korunmamış: %q — susturma anlamını yitirir", got.State)
	}
	// Yarış koruması: taze tarama daha DÜŞÜK sayı gördüyse kayıtlı sayı kalır.
	if got.Occurrences != 500 {
		t.Errorf("occurrence geriye düşmüş: %d (500 olmalı)", got.Occurrences)
	}
	if got.ResolvedAt == nil || *got.ResolvedAt != resolved {
		t.Error("resolved_at ileri taşınmamış")
	}
}

func TestMergeExceptionGroupNewIsNew(t *testing.T) {
	got := mergeExceptionGroup(ExceptionGroup{Fingerprint: "yeni", LastSeen: 42}, nil)
	if got.State != ExStateNew {
		t.Errorf("kayıtsız grup 'new' olmalı, got %q", got.State)
	}
	// first_seen boşsa last_seen'den doldurulur — yoksa grup 1970'te
	// başlamış görünür ve yaş tabanlı önceliklendirme bozulur.
	if got.FirstSeen != 42 {
		t.Errorf("first_seen last_seen'den doldurulmalı, got %d", got.FirstSeen)
	}
}

// Toplu yol tekil yolla AYNI birleştirmeyi yapmalı — ikisi ayrışırsa
// yenileme döngüsü ile API çağrısı farklı sonuç üretir.
func TestBatchAndSingleShareMergeLogic(t *testing.T) {
	b, err := readFileString("exception_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	if countOccurrences(b, "mergeExceptionGroup(") < 3 {
		t.Error("birleştirme tek yerden geçmiyor — tekil ve toplu yol ayrışabilir")
	}
	if countOccurrences(b, "s.GetExceptionGroup(ctx, g.Fingerprint)") > 1 {
		t.Error("sıcak döngüde tekil okuma geri gelmiş (v0.9.523 gerilemesi)")
	}
}

func readFileString(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

func countOccurrences(s, sub string) int { return strings.Count(s, sub) }
