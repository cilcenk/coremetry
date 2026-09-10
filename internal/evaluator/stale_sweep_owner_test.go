package evaluator

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.592 — bayat süpürme poller'ın Problem'lerini ATLAR.
//
// Evaluator somut *chstore.Store ile kurulur; sahte yok, süpürücüyü CH'siz
// koşturamayız. Bu yüzden karar SAF fonksiyonda ve burada tablo testli;
// KABLOLAMA ise yorum-süzülmüş kaynak piniyle: sweepStaleProblems'ın
// döngüsü `stale` üzerinden değil `toClose` üzerinden dönmeli — aksi hâlde
// saf fonksiyon yeşil, ekranda hiçbir şey değişmemiş olur.
func TestStaleSweepCandidates(t *testing.T) {
	stale := []chstore.Problem{
		{ID: "a", RuleID: "anomaly:shop-payment:p99_ms"},
		{ID: "b", RuleID: chstore.RuleExtDownPrefix + "ext:oracle-errlog"},
		{ID: "c", RuleID: "anomaly:ext:ggfail/OP1/E1:ext:tfail_adet"}, // seri Problem'i — SÜPÜRÜLÜR
		{ID: "d", RuleID: chstore.RuleExtCapPrefix + "ext:ggfail:ext:tfail_adet"},
	}
	toClose, skipped := staleSweepCandidates(stale, nil) // nil = 592 davranışı
	ids := func(ps []chstore.Problem) string {
		var b []string
		for _, p := range ps {
			b = append(b, p.ID)
		}
		return strings.Join(b, ",")
	}
	if got := ids(toClose); got != "a,c" {
		t.Fatalf("süpürülecekler a,c olmalı (seri Problem'i dahil), %q", got)
	}
	if got := ids(skipped); got != "b,d" {
		t.Fatalf("atlananlar b,d (ext-down, ext-cap), %q", got)
	}
	if tc, sk := staleSweepCandidates(nil, nil); len(tc) != 0 || len(sk) != 0 {
		t.Fatal("boş girdi boş çıktı")
	}
	// v0.10.605 — canlılık: oracle-errlog yaşıyor, ggfail silinmiş → d (ggfail
	// ext-cap) SÜPÜRÜLÜR, b (oracle ext-down) muaf kalır. Özne ext-cap'ten
	// metriksiz çıkarılır ("ext:ggfail:ext:tfail_adet" → "ext:ggfail").
	live := func(subject string) bool { return subject == "ext:oracle-errlog" }
	toClose, skipped = staleSweepCandidates(stale, live)
	if got := ids(toClose); got != "a,c,d" {
		t.Fatalf("ölü kaynağın ext-cap'i süpürülmeli: a,c,d bekleniyor, %q", got)
	}
	if got := ids(skipped); got != "b" {
		t.Fatalf("yalnız canlı kaynağın ext-down'ı muaf: b bekleniyor, %q", got)
	}
	none := func(string) bool { return false }
	if tc, sk := staleSweepCandidates(stale, none); len(tc) != 4 || len(sk) != 0 {
		t.Fatalf("hiç kaynak yaşamıyorsa hepsi süpürülür: %d/%d", len(tc), len(sk))
	}
}

func TestSweepStaleProblemsUsesCandidates(t *testing.T) {
	raw, err := os.ReadFile("evaluator.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(raw), "")
	start := strings.Index(code, "func (e *Evaluator) sweepStaleProblems(")
	if start < 0 {
		t.Fatal("sweepStaleProblems bulunamadı")
	}
	end := strings.Index(code[start:], "\n}\n")
	body := code[start : start+end]
	if !strings.Contains(body, "toClose, skipped := staleSweepCandidates(stale, e.pollerSourceLive)") {
		t.Fatal("süpürücü staleSweepCandidates'ı çağırmıyor — poller Problem'leri yine süpürülür")
	}
	if !strings.Contains(body, "for i := range toClose {") || strings.Contains(body, "for i := range stale {") {
		t.Fatal("döngü toClose üzerinden dönmeli, stale üzerinden değil")
	}
}
