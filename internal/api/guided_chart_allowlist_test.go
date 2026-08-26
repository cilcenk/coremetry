package api

import (
	"strings"
	"testing"
)

// guided_chart_allowlist_test.go — v0.10.70.
//
// v0.10.68, çit sökmesinin guided'a TAŞINAMAYACAĞINI ve sebebini yazdı:
// orada grafik ekrana ancak model çiti AKTARIRSA ulaşıyor. Ama açık bir
// risk bıraktı — model aktardığı spec'i DEĞİŞTİREBİLİR ya da kendi
// uydurduğunu araya sokabilir. Sonuç "doğru veriyle çizilmiş yanlış
// kapsam": düzyazıdaki yanlıştan daha ikna edici, çünkü grafik.
//
// ⚠ Çözüm imza DEĞİL. İmza arayüzde doğrulanamaz (gizli anahtar yok) ve
// sunucuda doğrulanacaksa zaten gereksiz: SUNUCU NE YAZDIĞINI BİLİYOR.
// İzin listesi kanıt bloğundan türüyor ve kanıt sunucu-yazımı; model
// listeye üye EKLEYEMEZ.

const evidenceWithChart = "Servis sağlığı:\n" +
	"```chart\n" +
	`{"title":"checkout · p99","service":"checkout-service","agg":"p99","rangeS":1800}` + "\n" +
	"```\n"

func TestServerAuthoredChartPassesThrough(t *testing.T) {
	allowed := serverChartScopes(evidenceWithChart)
	if len(allowed) != 1 {
		t.Fatalf("izin listesi %d kapsam taşıyor, 1 bekleniyordu", len(allowed))
	}
	answer := "Şöyle görünüyor:\n" +
		"```chart\n" +
		`{"title":"checkout · p99","service":"checkout-service","agg":"p99","rangeS":1800}` + "\n" +
		"```\n"
	got, n := filterModelChartFences(answer, allowed)
	if n != 0 {
		t.Errorf("meşru çit sökülmüş (%d) — guided grafikleri kaybolur:\n%s", n, got)
	}
	if !strings.Contains(got, "checkout-service") {
		t.Errorf("çit gövdesi kaybolmuş:\n%s", got)
	}
}

// TestMutatedScopeIsRejected — ASIL RİSK.
//
// Model spec'i aktarırken KAPSAMI değiştirirse grafik yine gerçek veriyle
// çizilir, ama SORULMAYAN veriyle. Operatör grafiğe düzyazıdan çok
// güvenir; bu yüzden sessiz geçmemeli.
func TestMutatedScopeIsRejected(t *testing.T) {
	allowed := serverChartScopes(evidenceWithChart)
	for _, tc := range []struct{ name, body string }{
		{"başka servis", `{"service":"payments-service","agg":"p99","rangeS":1800}`},
		{"başka agg", `{"service":"checkout-service","agg":"error_rate","rangeS":1800}`},
		{"başka pencere", `{"service":"checkout-service","agg":"p99","rangeS":86400}`},
		{"eklenmiş kırılım", `{"service":"checkout-service","agg":"p99","rangeS":1800,"groupBy":"host"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := "```chart\n" + tc.body + "\n```\n"
			got, n := filterModelChartFences(answer, allowed)
			if n != 1 {
				t.Errorf("değiştirilmiş kapsam GEÇTİ — sorulmayan veri çizilir:\n%s", got)
			}
			if !strings.Contains(got, "çizilmedi") {
				t.Errorf("sökülen çit sessizce silinmiş; operatör ne olduğunu okumalı:\n%s", got)
			}
		})
	}
}

// TestTitleDifferenceIsTolerated — ÇİZİMİ ETKİLEMEYEN FARK DÜŞÜRMEZ.
//
// Arayüz `title`ı ZATEN yok sayıyor (v0.10.43 — başlık agg'den türüyor).
// Başlığı karşılaştırmak, çizimi hiç etkilemeyen bir farktan meşru bir
// grafiği düşürmek olurdu.
func TestTitleDifferenceIsTolerated(t *testing.T) {
	allowed := serverChartScopes(evidenceWithChart)
	answer := "```chart\n" +
		`{"title":"MODELİN UYDURDUĞU BAŞLIK","service":"checkout-service","agg":"p99","rangeS":1800}` +
		"\n```\n"
	if got, n := filterModelChartFences(answer, allowed); n != 0 {
		t.Errorf("yalnız başlığı farklı olan meşru grafik düşürüldü:\n%s", got)
	}
}

// TestNoServerChartMeansNoChart — LİSTE BOŞSA HİÇBİR ÇİT GEÇMEZ.
func TestNoServerChartMeansNoChart(t *testing.T) {
	answer := "```chart\n" + `{"service":"uydurma","agg":"p99","rangeS":1800}` + "\n```\n"
	got, n := filterModelChartFences(answer, serverChartScopes("kanıt, grafik yok"))
	if n != 1 {
		t.Errorf("sunucu grafik vermediği hâlde model çiti geçti:\n%s", got)
	}
}

// TestNonChartFencesUntouched — kod blokları modelin meşru çıktısı.
func TestNonChartFencesUntouched(t *testing.T) {
	answer := "```sql\nSELECT 1\n```\n"
	got, n := filterModelChartFences(answer, serverChartScopes(evidenceWithChart))
	if n != 0 || !strings.Contains(got, "SELECT 1") {
		t.Errorf("chart olmayan çit bozuldu:\n%s", got)
	}
}

// TestGuidedAnswerIsFiltered — MUHAFIZ ULAŞILABİLİR OLMALI.
func TestGuidedAnswerIsFiltered(t *testing.T) {
	src := readSourceFile(t, "copilot_guided.go")
	if !strings.Contains(src, "filterModelChartFences(answer, serverChartScopes(evidence))") {
		t.Error("guided cevabı izin listesinden GEÇMİYOR — model uydurma bir " +
			"kapsam aktarırsa gerçek veriyle çizilir")
	}
	// Süzme, cevabın kurulmasından SONRA olmalı; öncesinde `answer` yok.
	if strings.Index(src, "filterModelChartFences(answer") < strings.Index(src, `answer := strings.TrimSpace(raw)`) {
		t.Error("süzme cevap kurulmadan çağrılıyor")
	}
}
