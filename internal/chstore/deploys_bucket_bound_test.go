// v0.9.1172 regresyon testleri — ÜST KOVA SINIRI, deploy + korelasyon.
//
// Sınıfın sekizinci ve son dalgası (823 · 1156 · 1167 · 1168 · 1169 · 1170 ·
// 1171). Grup F, üç okuma:
//
//	serviceVersionMVSQL  → tek servisin sürüm/dağıtım listesi
//	deploysWindowMVSQL   → filo-geneli /deploys penceresi
//	GetCorrelatedChangesMV → korele değişiklikler (cari/temel bölmesi)
//
// Deploy tarafında zarar SAYI değil ÜYELİK: `<= to` ile pencereden SONRA
// doğan bir sürüm (first_seen ∈ [to, to+5dk)) HAVING'in alt kapısını geçip
// "bu pencerede dağıtıldı" diye listeleniyordu. Yani bir dağıtım, olmadığı
// bir zaman aralığına atfediliyordu — ve deploy↔regresyon ilişkisi tam da bu
// atıf üzerinden kuruluyor.
//
// Korelasyon tarafında belirti YANLIŞ BULGU: `is_cur` bölmesi tek taramada
// yapıldığı için pencereler örtüşmüyor (1167'nin hatası burada yok), ama
// curSeconds paydayı (winTo - atB)'den alırken sayaç bir kova fazla
// topluyordu — cari taraf şişip "trafik sıçraması" üretebiliyordu.
package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

func deploysBucketReads(t *testing.T) []struct {
	name string
	sql  string
} {
	t.Helper()
	return []struct {
		name string
		sql  string
	}{
		// İkisi de paket sabiti — kaynak dilimlemeye gerek yok.
		{"serviceVersionMVSQL (servis dağıtımları)", serviceVersionMVSQL},
		{"deploysWindowMVSQL (filo /deploys)", deploysWindowMVSQL},
		{"GetCorrelatedChangesMV (korele değişiklikler)",
			funcBody(t, "correlate.go", "func (s *Store) GetCorrelatedChangesMV(")},
	}
}

// TestDeploysReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu.
func TestDeploysReadsExcludeUpperBucket(t *testing.T) {
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

	for _, r := range deploysBucketReads(t) {
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

// TestDeploysNoPostWindowVersionLeaks — YENİ sınıf: bir dağıtım OLMADIĞI bir
// pencereye atfedilemez.
//
// Deploy okumaları alt tarafı `HAVING first_seen >= fromNs` ile kapatıyor ama
// ÜST tarafta hiçbir HAVING yok — sınır tek başına "bu sürüm pencerede doğdu
// mu" sorusunu cevaplıyor. `<= to` ile pencereden sonra doğan bir sürüm
// listeye giriyordu; bu test tam o sızıntıyı ölçer: `to` sonrası doğan bir
// first_seen'in taşıyıcı kovası ALINMAMALI.
func TestDeploysNoPostWindowVersionLeaks(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	// Hizalı pencerede `to`'dan sonra doğan sürümler ve taşıyıcı kovaları.
	births := []struct {
		name  string
		birth time.Time
	}{
		{"to'dan 1 saniye sonra", to.Add(time.Second)},
		{"to'dan 1 dakika sonra", to.Add(time.Minute)},
		{"to'dan 4dk59sn sonra", to.Add(5*time.Minute - time.Second)},
	}
	for _, r := range deploysBucketReads(t)[:2] { // yalnız deploy okumaları
		op := bucketUpperOp(t, r.name, r.sql)
		for _, b := range births {
			t.Run(r.name+"/"+b.name, func(t *testing.T) {
				carrier := b.birth.Truncate(5 * time.Minute)
				if admitsBucket(op, carrier, from, to) {
					t.Errorf("%s: %v'de doğan sürümün kovası (%v) pencereye alınıyor — "+
						"HAVING yalnız ALT tarafı kapatıyor, dağıtım olmadığı bir "+
						"pencereye atfedilir", r.name, b.birth.Format("15:04:05"),
						carrier.Format("15:04"))
				}
			})
		}
	}
}

// TestCorrelateCurrentSideMatchesItsDenominator — cari tarafın sayacı,
// curSeconds'ın ölçtüğü aralığı AŞAMAZ.
//
// GetCorrelatedChangesMV cari oranı sum(cnt)/curSeconds ile kuruyor ve
// curSeconds = winTo - atB. `<= winTo` ile sayaç [winTo, +5dk) kovasını da
// topluyordu: pay geniş, payda dar — ve bu sürümde hatanın belirtisi eksik
// sayı değil, UYDURMA BİR BULGU (var olmayan trafik sıçraması).
//
// Ölçülen tam olarak şu: alınan hiçbir kova curSeconds'ın aralığına SIFIR
// saniye katkı yapamaz, yani hiçbir kovanın BAŞLANGICI winTo'da ya da
// sonrasında olamaz. winTo'yu İÇEREN kovanın kısmen taşması ayrı bir
// mesele ve kabul edilen granülarite bedeli — alt sınırın from'dan önceki
// birkaç dakikayı içermesiyle aynı takas (v0.9.555).
func TestCorrelateCurrentSideMatchesItsDenominator(t *testing.T) {
	reads := deploysBucketReads(t)
	op := bucketUpperOp(t, reads[2].name, reads[2].sql)
	const grid = 5 * time.Minute

	for _, winSec := range []int{300, 600, 1800, 3600, 907 /* hizasız */} {
		win := time.Duration(winSec) * time.Second
		t.Run(win.String(), func(t *testing.T) {
			at := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
			atB := at.Truncate(grid)
			winTo := at.Add(win)
			baseFrom := atB.Add(-4 * grid)

			for b := baseFrom; !b.After(winTo.Add(2 * grid)); b = b.Add(grid) {
				if !admitsBucket(op, b, baseFrom, winTo) {
					continue
				}
				if !b.Before(winTo) {
					t.Errorf("winSec=%d: %v kovası alınıyor ama curSeconds %v'de bitiyor — "+
						"o kova [%v, +5dk) aralığını taşır, paydaya SIFIR saniye katar; "+
						"cari taraf şişip uydurma sıçrama üretir",
						winSec, b.Format("15:04"), winTo.Format("15:04:05"), b.Format("15:04"))
				}
			}
		})
	}
}

// TestNoInclusiveUpperBucketBoundInDeploys — dosya-geneli kapı. deploys.go ve
// correlate.go'da 5dk MV kova penceresi soran hiçbir sorgu `<= ?` taşıyamaz.
func TestNoInclusiveUpperBucketBoundInDeploys(t *testing.T) {
	for _, f := range []string{"deploys.go", "correlate.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if n := strings.Count(string(b), "time_bucket <= ?"); n > 0 {
			t.Errorf("%s: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823/1156/1167/1168/1169/1170/1171/1172 sınıfı)", f, n)
		}
	}
}
