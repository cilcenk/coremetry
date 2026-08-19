// v0.9.1171 regresyon testleri — ÜST KOVA SINIRI, topoloji/çağıran ailesi.
//
// Sınıfın yedinci dalgası (823 · 1156 · 1167 · 1168 · 1169 · 1170).
// Grup E, üç okuma — üçü de FINAL'lı ReplacingMergeTree MV'leri:
//
//	GetServiceGraphTopN    (topology_edges_5m)   → servis grafiği kenarları
//	ReadServiceCallersAgg  (service_callers_5m)  → çağıranlar tablosu
//	GetServiceBlastRadius  (service_callers_5m)  → etki alanı
//
// Üçünün de alt sınırı zaten hizalıydı — v0.9.555 bu iki dosyayı "repo'nun
// kendi doğru deseni" diye ANIYOR. Kaçan yalnız üst sınırdı.
//
// Bu grubun kendine ait katkısı İKİ TANE:
//
//  1. AYNI MV'yi okuyan iki yüzey (çağıranlar tablosu + etki alanı) sınırı
//     paylaşmak zorunda. Ayrışırlarsa aynı servis için "12 çağıran, 40k
//     çağrı" ile "12 çağıran, 41.2k çağrı" yan yana durur ve hangisinin
//     doğru olduğunu söyleyecek hiçbir şey yoktur.
//  2. GetServiceBlastRadius WindowSec'i (to-from)'dan hesaplıyor ve oran
//     ondan türüyor: `<= to` sayacı pencereden GENİŞ bir aralıktan
//     toplarken paydayı dar bırakıyordu. Pay ile paydanın aynı pencereyi
//     ölçtüğü, sınır testinden ayrı bir şart.
package chstore

import (
	"strings"
	"testing"
	"time"
)

func topologyBucketReads(t *testing.T) []struct {
	name string
	sql  string
} {
	t.Helper()
	return []struct {
		name string
		sql  string
	}{
		{"GetServiceGraphTopN (topology_edges_5m)",
			funcBody(t, "repo.go", "func (s *Store) GetServiceGraphTopN(")},
		// Saf builder — kaynak dilimlemeye gerek yok.
		{"serviceCallersAggSQL (çağıranlar)", serviceCallersAggSQL(100)},
		{"GetServiceBlastRadius (etki alanı)",
			funcBody(t, "blast_radius.go", "func (s *Store) GetServiceBlastRadius(")},
	}
}

// TestTopologyReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu.
func TestTopologyReadsExcludeUpperBucket(t *testing.T) {
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

	for _, r := range topologyBucketReads(t) {
		op := bucketUpperOp(t, r.name, r.sql)
		for _, c := range cases {
			t.Run(r.name+"/"+c.name, func(t *testing.T) {
				if got := admitsBucket(op, c.bucket, from, to); got != c.want {
					t.Errorf("%s: `time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v",
						r.name, op, c.bucket.Format("15:04"), got, c.want)
				}
			})
		}
	}
}

// TestServiceCallersReadsShareTheBound — service_callers_5m'i okuyan İKİ
// yüzey (çağıranlar tablosu + etki alanı) aynı pencereyi ölçmek zorunda.
// Ayrı ayrı bakıldığında ikisi de "doğru" görünür; hata yalnız yan yana
// konunca çıkar — bu yüzden tek tek sınır testleri bu sınıfı yakalayamaz.
func TestServiceCallersReadsShareTheBound(t *testing.T) {
	reads := topologyBucketReads(t)
	callersOp := bucketUpperOp(t, reads[1].name, reads[1].sql)
	blastOp := bucketUpperOp(t, reads[2].name, reads[2].sql)
	if callersOp != blastOp {
		t.Fatalf("çağıranlar `%s` / etki alanı `%s` — AYNI MV (service_callers_5m), "+
			"ayrışan sınır: aynı servis için iki farklı çağrı toplamı",
			callersOp, blastOp)
	}
}

// TestBlastRadiusRateDenominatorMatchesQueriedWindow — pay ile payda aynı
// pencereyi ölçmeli.
//
// WindowSec = to - from (Go tarafı), sayaçlar ise yüklemin ALDIĞI kovaların
// toplamı. `<= to` ile sayaç [from, to+5dk) aralığından toplarken payda
// (to-from) kalıyordu: çağrı/sn beş dakikalık yabancı trafik kadar şişikti.
// `<` ile sayacın kapsadığı en geç an tam `to` olur ve iki taraf örtüşür.
func TestBlastRadiusRateDenominatorMatchesQueriedWindow(t *testing.T) {
	reads := topologyBucketReads(t)
	op := bucketUpperOp(t, reads[2].name, reads[2].sql)

	for _, win := range []time.Duration{
		15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour,
		// Hizasız pencere: iki operatör aynı sonucu verir.
		time.Hour + 7*time.Minute,
	} {
		t.Run(win.String(), func(t *testing.T) {
			to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
			from := to.Add(-win)
			bucketStart := from.Truncate(5 * time.Minute)
			// Sayacın kapsadığı en geç an: alınan son kovanın SONU.
			var covered time.Time
			for b := bucketStart; !b.After(to.Add(10 * time.Minute)); b = b.Add(5 * time.Minute) {
				if admitsBucket(op, b, bucketStart, to) {
					covered = b.Add(5 * time.Minute)
				}
			}
			if covered.After(to) {
				t.Errorf("`time_bucket %s ?`: sayaç %v'ye kadar topluyor ama WindowSec "+
					"%v'ye göre hesaplanıyor — oran %v kadar şişik",
					op, covered.Format("15:04"), to.Format("15:04"), covered.Sub(to))
			}
		})
	}
}

// TestTopologyReadsAlignLowerBound — v0.9.555'in "repo'nun kendi doğru
// deseni zaten vardı" dediği alt sınır hizası, üst sınırı düzeltirken
// kaybolmasın. Artı gövde-kapsamlı kapı.
func TestTopologyReadsAlignLowerBound(t *testing.T) {
	for _, f := range []struct{ file, sig string }{
		{"repo.go", "func (s *Store) GetServiceGraphTopN("},
		{"backtrace.go", "func (s *Store) ReadServiceCallersAgg("},
		{"blast_radius.go", "func (s *Store) GetServiceBlastRadius("},
	} {
		body := funcBody(t, f.file, f.sig)
		if !strings.Contains(body, ".Truncate(5 * time.Minute)") {
			t.Errorf("%s: alt sınır 5dk ızgarasına inmiyor (v0.9.555 deseni)", f.sig)
		}
	}
	// Kapı: bu üç dosyada serviceCallersAggSQL/blast/graph okumaları
	// `<= ?` taşıyamaz. repo.go dosya-geneli DEĞİL (traces ailesi ayrı
	// dilim, orada `<=` bilinçli — bkz. v0.9.1169 commit gövdesi), o
	// yüzden gövde-kapsamlı.
	for _, src := range []string{
		funcBody(t, "repo.go", "func (s *Store) GetServiceGraphTopN("),
		serviceCallersAggSQL(100),
		funcBody(t, "blast_radius.go", "func (s *Store) GetServiceBlastRadius("),
	} {
		if n := strings.Count(src, "time_bucket <= ?"); n > 0 {
			t.Errorf("%d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823/1156/1167/1168/1169/1170/1171 sınıfı)", n)
		}
	}
}
