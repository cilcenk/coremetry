// v0.9.1038 — spool drenaj ETA'sının saf testleri.
//
// NEDEN: 2026-08-12 arızasında operatörün ikinci sorusu "eriyor mu"
// değil "NE ZAMAN biter"di ve cevap elle hesaplanmıştı (18.9k dosya ×
// ~5 sn/dosya ≈ 10 saat). Aritmetik artık sinyalin içinde, dolayısıyla
// üç ayrı sessiz hata sınıfı doğdu:
//
//  1. BİRİM KARIŞMASI (v0.6.36 dersi) — sn/dk/sa/gün eşiklerinin HEPSİ
//     ayrı ayrı sınanır. "Değer + birim" taşıyan hiçbir şablon tek
//     birimle gönderilmez; off-axis dal sessizce yanlış kalır.
//  2. KAPSAM KARIŞMASI (v0.9.986 / v0.5.187 sınıfı) — küme geneli bir
//     ölçümle yalnız-bu-düğüm ölçümü kıyaslanırsa 41.274 → 19.020
//     farkı "saniyede 2000 dosya eriyor" gibi TAMAMEN UYDURMA bir ETA
//     üretir. Fallback'in kapsamı ETA'ya da taşınmalı.
//  3. UINT64 SARMASI — büyüyen kuyrukta `prev.Files - cur.Files`
//     çıkarması uint64'te sarmalanıp devasa bir "erime" üretirdi.
package chstore

import (
	"strings"
	"testing"
)

// sample — Generated damgalı bir ölçüm (ETA iki zaman damgasından çıkar).
func sample(files uint64, atSec int64) *DistributionQueue {
	return &DistributionQueue{Measured: true, Files: files, Generated: atSec * 1e9}
}

func TestFmtSpoolETAEveryUnit(t *testing.T) {
	cases := []struct {
		name string
		sec  float64
		want string
	}{
		{"sıfır", 0, "0 sn"},
		{"negatif (saat kayması) 0 sn", -5, "0 sn"},
		{"saniye", 42, "42 sn"},
		{"saniye üst sınırı", 59.4, "59 sn"},
		{"dakikaya geçiş — tam dakika sn yazmaz", 60, "1 dk"},
		{"dakika + saniye", 90, "1 dk 30 sn"},
		{"dakika üst sınırı", 3599, "59 dk 59 sn"},
		{"saate geçiş — tam saat dk yazmaz", 3600, "1 sa"},
		{"saat + dakika", 3600 + 40*60, "1 sa 40 dk"},
		{"saat üst sınırı", 86399, "23 sa 59 dk"},
		{"güne geçiş — tam gün sa yazmaz", 86400, "1 gün"},
		{"gün + saat", 86400*2 + 3600*20, "2 gün 20 sa"},
		{"tavan altı en büyük", 98 * 86400, "98 gün"},
		{"tavan", 99 * 86400, "99 gün (bu hızla pratikte kapanmıyor)"},
		{"tavan üstü", 4000 * 86400, "99 gün (bu hızla pratikte kapanmıyor)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fmtSpoolETA(c.sec); got != c.want {
				t.Fatalf("fmtSpoolETA(%v) = %q, want %q", c.sec, got, c.want)
			}
		})
	}
}

func TestFmtSpoolRateEveryUnit(t *testing.T) {
	cases := []struct {
		name   string
		perSec float64
		want   string
	}{
		// Dakika birimi: >= 1 dosya/dk.
		{"yüksek hız tam sayı", 2.0, "+120 dosya/dk"},
		{"ondalık korunur (<10)", 1.0 / 60 * 3.4, "+3.4 dosya/dk"},
		{"tam sayıda ondalık yok", 1.0 / 60 * 5, "+5 dosya/dk"},
		{"dakika alt sınırı", 1.0 / 60, "+1 dosya/dk"},
		// Saat birimi: dakikada 1'in altı. Dakika olarak yazılsa
		// yuvarlanıp "+0 dosya/dk" görünür ve "birikmiyor" diye okunurdu.
		{"dakika altı → saate düşer", 0.5 / 60, "+30 dosya/sa"},
		{"çok yavaş birikme", 1.0 / 3600, "+1 dosya/sa"},
		{"saatte ondalık", 3.5 / 3600, "+3.5 dosya/sa"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fmtSpoolRate(c.perSec); got != c.want {
				t.Fatalf("fmtSpoolRate(%v) = %q, want %q", c.perSec, got, c.want)
			}
		})
	}
}

func TestDistributionDrainNote(t *testing.T) {
	partial := func(q *DistributionQueue) *DistributionQueue { q.Partial = true; return q }

	cases := []struct {
		name      string
		cur, prev *DistributionQueue
		want      string // "" = not YOK
	}{
		{
			name: "tek örneklem — yön izlenmedi, ETA YOK",
			cur:  sample(5000, 100), prev: nil, want: "",
		},
		{
			name: "önceki ölçülemedi — ETA YOK",
			cur:  sample(5000, 130),
			prev: &DistributionQueue{Measured: false, Generated: 100 * 1e9},
			want: "",
		},
		{
			// v0.9.986 kapsam kilidi: küme geneli → yerel daralması
			// "saniyede 2000 dosya eriyor" gibi uydurma bir ETA verirdi.
			name: "kapsam DEĞİŞTİ (küme → yerel) — ETA YOK",
			cur:  partial(sample(19020, 130)), prev: sample(41274, 100),
			want: "",
		},
		{
			name: "iki yerel örnek — kapsam AYNI, ETA var",
			cur:  partial(sample(1000, 200)), prev: partial(sample(2000, 100)),
			want: "; bu hızla boşalması ≥1 dk 40 sn sürer",
		},
		{
			name: "aynı zaman damgası — hız hesaplanamaz",
			cur:  sample(1000, 100), prev: sample(2000, 100), want: "",
		},
		{
			name: "saat geri gitti — hız hesaplanamaz",
			cur:  sample(1000, 90), prev: sample(2000, 100), want: "",
		},
		{
			name: "sabit kuyruk — hız 0, not YOK (SABİT dalı zaten söylüyor)",
			cur:  sample(2000, 130), prev: sample(2000, 100), want: "",
		},
		{
			// CANLI VAKA (2026-08-12 16:07): 44.320 → 44.318, 11 sn.
			// İki dosya eridi; teknik olarak "azalıyor" ama ~2.8 gün.
			name: "canlı flap örneği — 'azalıyor' ama günler sürer",
			cur:  sample(44318, 11), prev: sample(44320, 0),
			want: "; bu hızla boşalması ≥2 gün 19 sa sürer",
		},
		{
			// UINT64 SARMASI: prev-cur uint64'te yapılsaydı devasa bir
			// "erime" ve saniyelik bir ETA üretirdi.
			name: "büyüyen kuyruk — birikme hızı, sarma YOK",
			cur:  sample(44320, 60), prev: sample(44300, 0),
			want: "; birikiyor (+20 dosya/dk)",
		},
		{
			name: "çok yavaş birikme saat birimine düşer",
			cur:  sample(1010, 3600), prev: sample(1000, 0),
			want: "; birikiyor (+10 dosya/sa)",
		},
		{
			name: "boşalmış kuyruk — 'ne zaman biter' sorusu yok",
			cur:  sample(0, 130), prev: sample(500, 100), want: "",
		},
		{
			name: "drenaj çok yavaş — tavan metni",
			cur:  sample(1_000_000, 3600), prev: sample(1_000_001, 0),
			want: "; bu hızla boşalması ≥99 gün (bu hızla pratikte kapanmıyor) sürer",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := distributionDrainNote(c.cur, c.prev); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// ETA'nın verdict METNİNE gerçekten girdiği — saf fonksiyonu test etmek
// onun BAĞLANDIĞINI ölçmez (v0.9.1001 / v0.9.1024 dersi). Üç dal da
// ayrı ayrı yoklanıyor: mutlak taban üstü (azalan + büyüyen) ve giriş
// kapısı (drene oluyor).
func TestDistributionVerdictCarriesETA(t *testing.T) {
	// (1) Mutlak taban ÜSTÜNDE azalıyor — canlı flap senaryosu.
	st := DistributionVerdict(DistributionState{}, sample(44318, 11), sample(44320, 0))
	if !st.Degraded {
		t.Fatalf("taban üstü degraded olmalıydı: %q", st.Detail)
	}
	if !strings.Contains(st.Detail, "bu hızla boşalması ≥") {
		t.Fatalf("azalan taban-üstü metni ETA taşımalıydı: %q", st.Detail)
	}

	// (2) Mutlak taban ÜSTÜNDE büyüyor — birikme hızı.
	st = DistributionVerdict(DistributionState{}, sample(44340, 30), sample(44300, 0))
	if !strings.Contains(st.Detail, "birikiyor (+") {
		t.Fatalf("büyüyen taban-üstü metni birikme hızı taşımalıydı: %q", st.Detail)
	}

	// (3) Giriş kapısı (taban ALTINDA, sessiz eşiğin üstünde) — drene oluyor.
	st = DistributionVerdict(DistributionState{}, sample(400, 30), sample(1900, 0))
	if st.Degraded {
		t.Fatalf("eriyen kuyruk taban altında degraded olmamalı: %q", st.Detail)
	}
	if !strings.Contains(st.Detail, "drene oluyor") ||
		!strings.Contains(st.Detail, "bu hızla boşalması ≥") {
		t.Fatalf("giriş kapısı metni ETA taşımalıydı: %q", st.Detail)
	}

	// /api/health JSON'una giriyor: ETA hiçbir dalda satır sonu getirmemeli.
	if strings.ContainsAny(st.Detail, "\n\r") {
		t.Fatalf("detail satır sonu taşıyor: %q", st.Detail)
	}
}
