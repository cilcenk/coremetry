package anomaly

// external_health_test.go — v0.10.588: kaynağa ERİŞİLEMEMESİ kendisi Problem.
//
// Oracle audit'i (2026-09-09): Influx poller'ı hata alınca yalnız durum
// kartına yazıyor, tarayıcı hiç koşmuyor; "kaynak sustu" kapanışı dürüst ama
// "kaynağa bağlanamıyorum" ALARMI yok. Sözleşme:
//   - tek/iki hata → hiçbir şey (tekil hata olağan, retry yok)
//   - ardışık externalDownAfter (3) hata → ext:<kaynak> özneli, critical,
//     kind=external Problem AÇILIR
//   - hata sürerken aynı satır TOUCH (yeni ID yok; süpürücü kapatmasın)
//   - ilk başarıda RESOLVE ve seri sıfırlanır (yeniden açılmak 3 hata ister)
//   - açık bir şey yokken başarı hiçbir şey yazmaz

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSourceHealth_OpensAfterConsecutiveFailures(t *testing.T) {
	f := &fakeExtStore{cfg: chstore.DefaultAnomalySensitivity()}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := newExtScanner(f, now)
	s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "ORA-12541: no listener", now)
	s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "ORA-12541: no listener", now.Add(time.Minute))
	if len(f.upserts) != 0 {
		t.Fatalf("iki hata Problem açmamalı (tekil hata olağan): %d upsert", len(f.upserts))
	}
	s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "ORA-12541: no listener", now.Add(2*time.Minute))
	if len(f.upserts) != 1 {
		t.Fatalf("3. ardışık hata TEK Problem açmalı: %d", len(f.upserts))
	}
	p := f.upserts[0]
	if p.Status != "open" || p.Kind != chstore.ProblemKindExternal || p.Severity != "critical" {
		t.Fatalf("critical, kind=external, open: %+v", p)
	}
	if p.Service != "ext:oracle-errlog" || !strings.HasPrefix(p.RuleID, "anomaly:ext-down:") {
		t.Fatalf("özne ext:<kaynak>, kural ext-down: %q %q", p.Service, p.RuleID)
	}
	if !strings.Contains(p.Description, "ORA-12541") || !strings.Contains(p.Description, "3") {
		t.Fatalf("gerekçe son hatayı ve ardışık sayıyı söylemeli: %q", p.Description)
	}
}

func TestSourceHealth_TouchesWhileDownThenResolves(t *testing.T) {
	f := &fakeExtStore{cfg: chstore.DefaultAnomalySensitivity()}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	s := newExtScanner(f, now)
	for i := 0; i < 5; i++ {
		s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "timeout", now.Add(time.Duration(i)*time.Minute))
	}
	// 3. → open, 4. ve 5. → touch: ÜÇ upsert, hepsi AYNI ID, hepsi open.
	if len(f.upserts) != 3 {
		t.Fatalf("open + 2 touch = 3 upsert, %d", len(f.upserts))
	}
	id := f.upserts[0].ID
	for _, p := range f.upserts {
		if p.ID != id || p.Status != "open" {
			t.Fatalf("touch aynı satırı yenilemeli (ID sabit, open): %+v", p)
		}
	}
	// Başarı → resolve.
	s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "", now.Add(6*time.Minute))
	last := f.upserts[len(f.upserts)-1]
	if last.ID != id || last.Status != "resolved" {
		t.Fatalf("ilk başarı Problem'i resolve etmeli: %+v", last)
	}
	// Seri sıfırlandı: iki hata daha AÇMAZ.
	n := len(f.upserts)
	s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "timeout", now.Add(7*time.Minute))
	s.ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "timeout", now.Add(8*time.Minute))
	if len(f.upserts) != n {
		t.Fatalf("resolve sonrası seri sıfırlanmalı; iki hata açmamalı: %d → %d", n, len(f.upserts))
	}
}

func TestSourceHealth_SuccessWithNothingOpenWritesNothing(t *testing.T) {
	f := &fakeExtStore{cfg: chstore.DefaultAnomalySensitivity()}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	newExtScanner(f, now).ReportSourceHealth(context.Background(), "o-1", "oracle-errlog", "", now)
	if len(f.upserts) != 0 {
		t.Fatalf("sağlıklı kaynak hiçbir şey yazmaz: %d", len(f.upserts))
	}
}
