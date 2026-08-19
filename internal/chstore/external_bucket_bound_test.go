// v0.9.1174 regresyon testleri — ÜST KOVA SINIRI, dış bağımlılık kataloğu +
// span-metrik MV yolu.
//
// ONUNCU dalga, ve varlık sebebi bir HATA: v0.9.1167-1173 taramasının
// envanteri `grep … | head -40` ile çıkarılmıştı ve grep tam 40 satırda
// KESİLMİŞTİ. Üç dosya listeye hiç girmedi — external.go (6),
// spanmetric.go (3), internal/api/api.go (4). "Tarama kapandı" raporu bu
// yüzden erken verildi. Ders envanterin kendisinde: kesilebilen bir
// komutun çıktısı envanter değildir; sayım `wc -l` ile ayrıca doğrulanır.
//
// Bu dosya chstore'daki dokuzu kapatıyor:
//
//	GetExternalHosts / … (external.go ×3 fonksiyon, her biri 2 yüklem)
//	  → topology_edges_5m FINAL + service_summary_5m dışlama alt-sorgusu
//	mvSpanMetric / mvSpanMetricMulti / … (spanmetric.go ×3) → seri
//
// external.go'nun kendine ait riski: dışlama alt-sorgusu ("bu host aslında
// enstrümante bir servis mi") DIŞ sorguyla AYNI pencereyi kullanmak
// zorunda. Sınırlar ayrışırsa bir servis kenar tarafında "dış bağımlılık",
// dışlama tarafında "bilinen servis" sayılır — katalog kendi kendisiyle
// çelişir. Aynı sorgunun iki yarısı olduğu için hiçbir tekil sınır testi
// bunu yakalayamaz.
package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// externalPairRe — external.go'nun her okumasındaki İKİ yüklemi ayrı ayrı
// yakalar (dış sorgu + dışlama alt-sorgusu).
var externalPairRe = regexp.MustCompile(`time_bucket\s*(<=?)\s*\?`)

func TestExternalCatalogueBoundsAgreeWithinQuery(t *testing.T) {
	b, err := os.ReadFile("external.go")
	if err != nil {
		t.Fatalf("external.go okunamadı: %v", err)
	}
	ops := externalPairRe.FindAllStringSubmatch(string(b), -1)
	if len(ops) == 0 {
		t.Fatal("external.go'da kova yüklemi bulunamadı — yapı değiştiyse testi güncelle")
	}
	if len(ops)%2 != 0 {
		t.Errorf("%d yüklem — external.go'nun her okuması ÇİFT yüklem taşır "+
			"(kenarlar + dışlama alt-sorgusu); tek sayı, bir okumanın yarısının "+
			"denetimsiz kaldığını gösterir", len(ops))
	}
	for i := 0; i < len(ops); i += 2 {
		outer, inner := ops[i][1], ops[i+1][1]
		if outer != inner {
			t.Errorf("okuma #%d: kenar sorgusu `%s`, dışlama alt-sorgusu `%s` — "+
				"ayrışan pencere: bir servis kenarda 'dış bağımlılık', dışlamada "+
				"'bilinen servis' sayılır", i/2+1, outer, inner)
		}
	}
}

// TestExternalAndSpanMetricExcludeUpperBucket — DAVRANIŞ testi, 823'ün
// tablosu. Her iki dosyanın TÜM yüklemleri aynı operatörü taşımalı, o
// yüzden dosya başına tek operatör çıkarılıyor.
func TestExternalAndSpanMetricExcludeUpperBucket(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{"pencereden ÖNCEKİ kova dışarıda", from.Add(-5 * time.Minute), false},
		{"tam from'daki kova içeride", from, true},
		{"ortadaki kova içeride", from.Add(30 * time.Minute), true},
		{"son tam kova içeride", to.Add(-5 * time.Minute), true},
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to'dan sonraki kova dışarıda", to.Add(5 * time.Minute), false},
	}

	for _, file := range []string{"external.go", "spanmetric.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", file, err)
		}
		ms := externalPairRe.FindAllStringSubmatch(string(b), -1)
		if len(ms) == 0 {
			t.Fatalf("%s: kova yüklemi bulunamadı", file)
		}
		// Dosyadaki her yüklem aynı operatörü taşımalı — karışık bir dosya
		// zaten kendi içinde tutarsızdır.
		op := ms[0][1]
		for _, m := range ms[1:] {
			if m[1] != op {
				t.Fatalf("%s: dosya içinde KARIŞIK operatör (`%s` ve `%s`) — "+
					"aynı MV ailesini iki farklı pencereyle okuyor", file, op, m[1])
			}
		}
		for _, c := range cases {
			t.Run(file+"/"+c.name, func(t *testing.T) {
				if got := admitsBucket(op, c.bucket, from, to); got != c.want {
					t.Errorf("%s: `time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v",
						file, op, c.bucket.Format("15:04"), got, c.want)
				}
			})
		}
	}
}

// TestNoInclusiveUpperBucketBoundInExternalAndSpanMetric — dosya-geneli kapı.
func TestNoInclusiveUpperBucketBoundInExternalAndSpanMetric(t *testing.T) {
	for _, f := range []string{"external.go", "spanmetric.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if n := strings.Count(string(b), "time_bucket <= ?"); n > 0 {
			t.Errorf("%s: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823→1174 sınıfı)", f, n)
		}
	}
}
