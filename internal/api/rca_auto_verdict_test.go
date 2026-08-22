// v0.9.1281 — otomatik verdict üretiminin İKİ saf kararı.
//
// Semptom (bu dilimden ÖNCE): verdict yalnız ✨ Explain tıklamasında
// üretiliyordu ve gövdesi 10dk'lık explain önbelleği dışında hiçbir yerde
// yaşamıyordu. Otomatik üretim eklenirken iki şey ters gidebilir ve
// ikisi de SESSİZ:
//
//  1. DEDUP KAÇAĞI — sentezleyici 30 SANİYEDE bir koşuyor. Kapı
//     ısırmazsa açık bir P1 çözülene kadar saatte 120 LLM çağrısı üretir;
//     hiçbir hata logu düşmez, yalnız kota biter (v0.9.200'ün devre
//     kesicisini kuran olayın aynı şekli).
//  2. YANLIŞ GÖVDE — kayda operatörün GÖRMEDİĞİ metin yazılırsa
//     "operatöre ne gösterdik" sorusuna yanlış cevap veren bir kayıt
//     kalır. Kaydın var olma sebebi tam da bu soru, yani hata kaydı
//     yararsız değil YANILTICI yapar.
package api

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestShouldGenerateAutoVerdict(t *testing.T) {
	const window = 30 * time.Minute
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC).UnixNano()
	ago := func(d time.Duration) int64 { return now - d.Nanoseconds() }

	tests := []struct {
		name         string
		lastNs       int64
		wantGenerate bool
	}{
		{
			// Hiç kayıt yok → üret. Dedup'ın "her şeyi engelle"ye
			// dönüşmediğini pinleyen POZİTİF KONTROL: bu vaka olmadan
			// "hep false dönen" bir kapı da testten geçerdi.
			name: "kayıt yok", lastNs: 0, wantGenerate: true,
		},
		{
			name: "negatif damga (bozuk satır) üretir", lastNs: -1, wantGenerate: true,
		},
		{
			name: "1 dk önce üretildi — üretme", lastNs: ago(time.Minute),
		},
		{
			name: "29 dk önce — hâlâ taze", lastNs: ago(29 * time.Minute),
		},
		{
			// SINIR: tam pencere kadar eski. `>` yerine `>=` yazılsaydı
			// burası üretirdi ve dedup penceresi fiilen bir tik kısalırdı.
			name: "tam pencere kadar eski — üretme", lastNs: ago(window),
		},
		{
			name:   "pencereden 1 ns fazla — üret",
			lastNs: ago(window) - 1, wantGenerate: true,
		},
		{
			name: "2 saat önce — üret", lastNs: ago(2 * time.Hour), wantGenerate: true,
		},
		{
			// Saat kayması (çok podlu kurulumda beklenen): gelecek
			// tarihli kayıt negatif yaş verir. GÜVENLİ YÖN üretmemek —
			// aksi hâlde kayan saatli bir pod kotayı yakan döngüye
			// girerdi. Bu vaka olmadan `nowNs-lastNs > window`'un
			// abs() ile yazılması da testten geçerdi.
			name: "gelecek tarihli kayıt — üretme", lastNs: now + time.Hour.Nanoseconds(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldGenerateAutoVerdict(tc.lastNs, now, window); got != tc.wantGenerate {
				t.Fatalf("shouldGenerateAutoVerdict(last=%d) = %v, beklenen %v",
					tc.lastNs, got, tc.wantGenerate)
			}
		})
	}
}

func TestRCAVerdictBodyOf(t *testing.T) {
	str := func(s string) *string { return &s }

	tests := []struct {
		name    string
		prose   *string
		summary string
		want    string
	}{
		{
			// Model çözümlendi: operatör LLM anlatımını okur.
			name: "prose kazanır", prose: str("payments-db havuzu tükendi"),
			summary: "deterministik yedek", want: "payments-db havuzu tükendi",
		},
		{
			// Model çözümlenemedi: buildRCAVerdict sözleşmesi gereği
			// prose nil KALIR ve yedek cümle summary'ye yazılır. Kayıt o
			// cümleyi taşımalı — ekranda görünen o.
			name: "prose yoksa summary", prose: nil,
			summary: "Kanıt yetersiz; en güçlü aday payments-db.",
			want:    "Kanıt yetersiz; en güçlü aday payments-db.",
		},
		{
			// Boş-ama-non-nil pointer: nil kontrolü tek başına yetmez.
			name: "boş prose summary'ye düşer", prose: str(""),
			summary: "yedek cümle", want: "yedek cümle",
		},
		{
			name: "yalnız boşluklu prose summary'ye düşer", prose: str("  \n "),
			summary: "yedek cümle", want: "yedek cümle",
		},
		{
			name: "prose kırpılır", prose: str("  anlatım  "),
			summary: "yedek", want: "anlatım",
		},
		{
			// İkisi de boş: UYDURMA. Gövdesiz kayıt dürüst bir hâl;
			// frontend gövdesiz satırı çizmez.
			name: "ikisi de boş", prose: nil, summary: "", want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rcaVerdictBodyOf(tc.prose, tc.summary); got != tc.want {
				t.Fatalf("rcaVerdictBodyOf = %q, beklenen %q", got, tc.want)
			}
		})
	}
}

// TestRCAVerdictRecordOfCarriesProvenance — kaynak etiketi ve gövde
// KAYDA giriyor mu.
//
// Neden ayrı test: rcaVerdictBodyOf tek başına doğru olabilir ama
// rcaVerdictRecordOf onu rec.Body'ye BAĞLAMAYI unutmuş olabilir —
// alanı kurmayan bir dönüşüm derlenir, tüm diğer testlerden geçer ve
// yalnız gerçek ClickHouse satırında (boş gövde) görülür. Aynı sınıf
// hata bu tabloda bir kez yaşandı (v0.9.543 struct alanı ↔ kolon tipi).
func TestRCAVerdictRecordOfCarriesProvenance(t *testing.T) {
	v := &RCAVerdict{
		Verdict: "probable_cause",
		Summary: "deterministik yedek cümle",
	}
	v.RootCause.Entity = "payments-db"
	h := &chstore.RootCauseHypothesis{Service: "checkout", Version: 42}

	t.Run("operatör yolu prose taşır", func(t *testing.T) {
		prose := "havuz tükendi"
		rec, ok := rcaVerdictRecordOf("ex1", "problem", "p-1",
			chstore.RCAVerdictSourceOperator, h, v, &prose)
		if !ok {
			t.Fatal("kayıt üretilmedi")
		}
		if rec.Source != chstore.RCAVerdictSourceOperator {
			t.Fatalf("source = %q, beklenen %q", rec.Source, chstore.RCAVerdictSourceOperator)
		}
		if rec.Body != prose {
			t.Fatalf("body = %q, beklenen %q", rec.Body, prose)
		}
		// Hipotezden gelen alanlar korunuyor mu (tam-satır replace
		// sözleşmesi: eksik alan bir sonraki yazımda SİLİNMİŞ olur).
		if rec.Service != "checkout" || rec.HypoVersion != 42 {
			t.Fatalf("hipotez alanları taşınmadı: %+v", rec)
		}
	})

	t.Run("otomatik yol summary'ye düşer ve auto etiketlenir", func(t *testing.T) {
		rec, ok := rcaVerdictRecordOf("ex2", "problem", "p-1",
			chstore.RCAVerdictSourceAuto, h, v, nil)
		if !ok {
			t.Fatal("kayıt üretilmedi")
		}
		if rec.Source != chstore.RCAVerdictSourceAuto {
			t.Fatalf("source = %q, beklenen %q", rec.Source, chstore.RCAVerdictSourceAuto)
		}
		if rec.Body != v.Summary {
			t.Fatalf("body = %q, beklenen %q", rec.Body, v.Summary)
		}
	})

	t.Run("iki kaynak AYIRT EDİLİR", func(t *testing.T) {
		// /ai kalite kırılımının tamamı buna dayanıyor: aynı etiketle
		// yazsalardı arka plan maliyeti tıklamalı trafiğin içinde
		// kaybolurdu.
		if chstore.RCAVerdictSourceAuto == chstore.RCAVerdictSourceOperator {
			t.Fatal("kaynak sabitleri aynı — atıf ayrımı çöker")
		}
		if rcaVerdictAutoSurface == rcaVerdictSurface {
			t.Fatalf("yüzey etiketleri aynı (%q) — /ai otomatik ile tıklamalıyı ayıramaz",
				rcaVerdictAutoSurface)
		}
	})

	t.Run("verdict nil ise kayıt yok", func(t *testing.T) {
		if _, ok := rcaVerdictRecordOf("ex3", "problem", "p-1",
			chstore.RCAVerdictSourceAuto, h, nil, nil); ok {
			t.Fatal("nil verdict'ten kayıt üretildi — ölçümde 'karar verildi' diye sayılırdı")
		}
	})
}
