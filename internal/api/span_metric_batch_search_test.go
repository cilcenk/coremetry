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
	k1 := spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "timeout", aggs)
	k2 := spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "refused", aggs)
	if k1 == k2 {
		t.Error("iki farklı arama AYNI önbellek anahtarını üretti — operatör " +
			"birinin sonucunu ötekinin sorusuna karşılık görür (v0.5.187)")
	}
	// Boş arama, aramasız çağrıyla aynı kalmalı: aksi halde mevcut
	// çağıranların (servis detayı) önbelleği tek seferde soğurdu.
	if spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "", aggs) !=
		spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "", aggs) {
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

// TestBatchSearchSkipsFastPaths — v0.9.618, v0.9.601'in AÇIĞI.
//
// v0.9.601 batch yüzeyine Search ekledi ve yüklemi WHERE'e koydu. Ama
// WHERE yalnız fast-path'ler REDDEDERSE kuruluyor: QuerySpanMetricMulti
// önce tryOperationMVFastPathMulti, sonra tryNarrowRollupFastPathMulti
// çağırıyor ve İKİSİ DE Search'e bakmıyordu. Kabul ettiklerinde arama
// hiç uygulanmıyordu — /traces hacim şeridi filtrelenmemiş hacmi
// çizerken tablo filtreli sonucu gösterirdi. v0.9.601'in ÖNLEMEK İÇİN
// yazıldığı yalanın ta kendisi.
//
// Kaynak taraması, çünkü kapı bir sıralama sözleşmesi: fast-path
// çağrıları WHERE'den ÖNCE koşuyor ve gate onlardan da önce olmalı.
func TestBatchSearchSkipsFastPaths(t *testing.T) {
	src := readAPISourceNoComments(t, "../chstore/spanmetric.go")
	i := strings.Index(src, "func (s *Store) QuerySpanMetricMulti(")
	if i < 0 {
		t.Fatal("QuerySpanMetricMulti bulunamadı — test bayatladı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}

	gate := strings.Index(body, `fastPathOK := f.Search == ""`)
	if gate < 0 {
		t.Fatal("batch yolunda Search kapısı YOK — arama, MV/rollup fast-path'i " +
			"kabul ettiğinde sessizce düşer ve grafik ile tablo AYRI kümeleri gösterir")
	}
	// Kapı HER İKİ fast-path'ten de ÖNCE gelmeli.
	for _, fp := range []string{"tryOperationMVFastPathMulti(", "tryNarrowRollupFastPathMulti("} {
		at := strings.Index(body, fp)
		if at < 0 {
			continue // fast-path kaldırıldıysa sorun yok
		}
		if at < gate {
			t.Errorf("%s kapıdan ÖNCE çağrılıyor — o yol Search'ü yok sayar", fp)
		}
		// Çağrı fastPathOK bloğunun içinde mi: kapı ile çağrı arasında
		// bir `if fastPathOK {` bulunmalı.
		between := body[gate:at]
		if !strings.Contains(between, "if fastPathOK {") {
			t.Errorf("%s fastPathOK kapısının İÇİNDE değil", fp)
		}
	}
}

// v0.10.484 — Root / Errors bayrakları anahtara girer; ikisi boşken anahtar değişmez.
func TestSpanMetricBatchKeyFlags(t *testing.T) {
	aggs := []chstore.SpanMetricAggSpec{{Name: "count", Aggregation: "count"}}
	base := spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "q", aggs, false, false)
	if base != spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "q", aggs) {
		t.Fatal("bayraksız anahtar eski anahtarla aynı olmalı")
	}
	root := spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "q", aggs, true, false)
	errs := spanMetricBatchKey(1, 2, 30, 0, 0, nil, "", "", "q", aggs, false, true)
	if root == base || errs == base || root == errs {
		t.Fatalf("bayraklar anahtarı ayırmalı: base=%s root=%s errs=%s", base, root, errs)
	}
}
