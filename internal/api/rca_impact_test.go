// v0.9.559 — RCA impact testleri.
//
// Korunan sözleşme: ÖLÇEMEDİĞİMİZ bir şeyi SIFIR diye raporlamayız.
//
// "Bu sayı ClickHouse'tan geliyor, model uyduramaz" güvencesi, okuma
// yanlışsa uydurmadan TEHLİKELİDİR: operatör sayıyı sorgulamaz. Boş
// sonucu 0 basmak, tam patlama anında "etkilenen istek yok" demek
// olurdu — ve ✨ Explain'e basılan an tam olarak o andır.
package api

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSummariseRCAImpactEmptyIsNotZero(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	got := summariseRCAImpact(nil, "odeme-db", "odeme-api", now.Add(-10*time.Minute), now)

	if got.ErrorCount != nil || got.RequestCount != nil || got.ErrorShare != nil {
		t.Errorf("boş sonuç SIFIR olarak raporlandı: %+v — operatör "+
			"'etkilenen istek yok' okur, oysa ölçüm yapılamadı", got)
	}
	if got.Note == "" {
		t.Error("ölçülemedi ama sebep yazılmamış — sessiz boşluk yanıltır")
	}
}

func TestSummariseRCAImpactCounts(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	rows := []chstore.ServiceSummaryRow{
		{SpanCount: 800, ErrorCount: 40},
		{SpanCount: 200, ErrorCount: 60},
	}
	got := summariseRCAImpact(rows, "odeme-db", "odeme-api", now.Add(-10*time.Minute), now)

	if got.RequestCount == nil || *got.RequestCount != 1000 {
		t.Errorf("istek sayısı %v, beklenen 1000", got.RequestCount)
	}
	if got.ErrorCount == nil || *got.ErrorCount != 100 {
		t.Errorf("hata sayısı %v, beklenen 100", got.ErrorCount)
	}
	if got.ErrorShare == nil || *got.ErrorShare != 0.1 {
		t.Errorf("hata oranı %v, beklenen 0.1", got.ErrorShare)
	}
	// Sayılar KİMİN olduğu açıkça taşınmalı.
	if got.Entity != "odeme-db" || got.AnchorService != "odeme-api" {
		t.Errorf("varlık ayrımı kayıp: entity=%q anchor=%q", got.Entity, got.AnchorService)
	}
}

func TestSummariseRCAImpactZeroTrafficIsNotEmpty(t *testing.T) {
	// Satır VAR ama hiç span yok: gerçek bir sıfır trafik. Bu, "MV
	// materyalize olmadı"dan FARKLI bir şey ve öyle raporlanmalı.
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	rows := []chstore.ServiceSummaryRow{{SpanCount: 0, ErrorCount: 0}}
	got := summariseRCAImpact(rows, "a", "a", now.Add(-10*time.Minute), now)

	if got.RequestCount == nil || *got.RequestCount != 0 {
		t.Errorf("sıfır trafik ölçüldü olarak raporlanmalı: %v", got.RequestCount)
	}
	if got.ErrorShare != nil {
		t.Error("sıfır istekte hata oranı UYDURULMUŞ — tanımsız olmalı")
	}
	if got.Note == "" {
		t.Error("tanımsız oran açıklanmamış")
	}
}

func TestRCAImpactWindowClampsToNow(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	// 3 dakika önce açılmış problem: boundAnalysisWindow sona 10dk
	// ekler, yani pencere sonu 7 dakika GELECEKTE kalırdı.
	started := now.Add(-3 * time.Minute)
	from, to := rcaImpactWindow(started.UnixNano(), now)

	if to.After(now) {
		t.Errorf("pencere sonu gelecekte: %v > %v — 'sorgulanan aralık' yalan söyler", to, now)
	}
	if !from.Equal(started) {
		t.Errorf("pencere başı ankorun başlangıcı olmalı: %v vs %v", from, started)
	}
}

func TestRCAImpactWindowRejectsFutureAnchor(t *testing.T) {
	// Saat kayması: ankor gelecekte damgalı. Ters pencereyle sorgu
	// çalıştırmaktansa ölçüm yapılamaz demek doğru.
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	from, to := rcaImpactWindow(now.Add(10*time.Minute).UnixNano(), now)
	if to.After(from) {
		t.Errorf("gelecek ankorda geçerli pencere üretildi: %v → %v", from, to)
	}
	got := summariseRCAImpact(nil, "a", "a", from, to)
	if got.Note == "" {
		t.Error("tutarsız pencere sessizce geçti")
	}
}
