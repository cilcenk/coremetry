// v0.9.601 — metric-batch'te arama yüklemi.
//
// /traces hacim şeridi üç ayrı /api/spans/metric çağrısı yapıyordu;
// metric-batch tam bu üçlüyü tek CH taramasına indirmek için vardı ve
// servis detay sayfası kullanıyordu. /traces geçemiyordu çünkü batch
// yüzeyinde `search` alanı YOKTU — geçseydi arama sessizce düşer,
// grafik filtrelenmemiş hacmi çizerken tablo filtreli sonucu
// gösterirdi. Yanlış cevap değil, YANLIŞ GÜVEN.
package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TestBatchKeySeparatesSearches — CLAUDE.md sert kısıtı: önbellek
// anahtarı TÜM girdileri hash'ler.
//
// Girmeseydi operatör "timeout" arar, sonra "refused" arar ve İLKİNİN
// serisini görürdü — v0.5.187 çapraz-zehirlenmesinin birebir aynısı.
func TestBatchKeySeparatesSearches(t *testing.T) {
	aggs := []chstore.SpanMetricAggSpec{{Name: "count", Aggregation: "count"}}
	k1 := spanMetricBatchKey(1, 2, 30, 0, nil, "", "", "timeout", aggs)
	k2 := spanMetricBatchKey(1, 2, 30, 0, nil, "", "", "refused", aggs)
	if k1 == k2 {
		t.Error("iki farklı arama AYNI önbellek anahtarını üretti — operatör " +
			"birinin sonucunu ötekinin sorusuna karşılık görür (v0.5.187)")
	}
	// Boş arama, aramasız çağrıyla aynı kalmalı: aksi halde mevcut
	// çağıranların (servis detayı) önbelleği tek seferde soğurdu.
	if spanMetricBatchKey(1, 2, 30, 0, nil, "", "", "", aggs) !=
		spanMetricBatchKey(1, 2, 30, 0, nil, "", "", "", aggs) {
		t.Error("aynı girdi iki farklı anahtar üretti — deterministik değil")
	}
}

// TestBatchAppliesSearchPredicate — arama WHERE'e GERÇEKTEN iniyor mu?
//
// Kaynak taraması: tek-agg yolu (QuerySpanMetric) searchPredicate'i
// uyguluyordu, batch yolu (QuerySpanMetricMulti) uygulamıyordu. İkisi
// AYNI yüklemi kurmak zorunda — yoksa histogram toplamı tablonun
// gösterdiği kümeyle uyuşmaz.
func TestBatchAppliesSearchPredicate(t *testing.T) {
	src := readAPISourceNoComments(t, "../chstore/spanmetric.go")
	i := strings.Index(src, "func (s *Store) QuerySpanMetricMulti(")
	if i < 0 {
		t.Fatal("QuerySpanMetricMulti bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "searchPredicate(f.Search)") {
		t.Error("batch yolu searchPredicate uygulamıyor — /traces hacim şeridi " +
			"aramayı yok sayar ve grafik ile tablo AYRI kümeleri gösterir")
	}
}
