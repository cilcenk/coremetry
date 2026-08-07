package api

import (
	"strings"
	"testing"
	"time"
	"os"
)

// v0.9.665 — metrik türevli throughput planlayıcısı.
//
// Uçtan uca test mümkün DEĞİL: Server.store somut bir *chstore.Store,
// yani sahte store enjekte edilemiyor ve handler canlı ClickHouse ister.
// Bu yüzden hata riski taşıyan karar mantığı saf bir planlayıcıya
// çıkarıldı ve testler oraya bakıyor.

func planFixture(t *testing.T) (string, time.Time, time.Time) {
	t.Helper()
	to := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	return "cm-put-service", to.Add(-time.Hour), to
}

// CLAUDE.md sert kısıtı: önbellek anahtarı TÜM girdileri hash'ler.
// metric ve jobLabel sorgudan geliyor; anahtardan düşerlerse iki farklı
// metrik birbirinin sonucunu okur — v0.5.187 çapraz-zehirlenme sınıfı,
// ve bu sefer operatör YANLIŞ TRAFİK GRAFİĞİ görürdü.
func TestMetricThroughputCacheKeyCoversEveryInput(t *testing.T) {
	svc, from, to := planFixture(t)
	base, _ := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180)

	cases := map[string]string{
		"servis":       mustKey(metricThroughputPlan("other-service", "m1", "job", from, to, 600, "", 180)),
		"metrik adı":   mustKey(metricThroughputPlan(svc, "m2", "job", from, to, 600, "", 180)),
		"etiket adı":   mustKey(metricThroughputPlan(svc, "m1", "service_job", from, to, 600, "", 180)),
		"zaman kovası": mustKey(metricThroughputPlan(svc, "m1", "job", from.Add(-48*time.Hour), to, 600, "", 180)),
		// v0.9.706 — nokta bütçesi de girdi: farklı mdp farklı adım üretir,
		// anahtardan düşerse 400px'lik panel 1200px'in sonucunu okur.
		"nokta bütçesi": mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 1200, "", 180)),
		// v0.9.718 — kırılım ve pencere de girdi (hash-all-inputs):
		"kırılım":       mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "route", 180)),
		"rate penceresi": mustKey(metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 300)),
	}
	for name, k := range cases {
		if k == base {
			t.Errorf("%s değişti ama önbellek anahtarı AYNI kaldı — çapraz zehirlenme", name)
		}
	}

	// Aynı girdi → aynı anahtar (yoksa önbellek hiç isabet etmez).
	if again, _ := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180); again != base {
		t.Error("aynı girdi farklı anahtar üretti — önbellek asla isabet etmez")
	}
}

func mustKey(k string, _ any) string { return k }

// v0.9.774 (operatör-bildirimi: prod'da "Response time · metrik" paneli
// boş) — REGRESYON KAPISI, iki parça:
//
//  1. ZARF SÜRÜMÜ. Yanıttan latency/latencyUnit/latencyUnitKnown/
//     latencyDiag kalktı, metricUnit geldi. Girdiler DEĞİŞMEDİĞİ için
//     anahtar da değişmezdi ve rolling deploy sırasında yeni kod eski
//     zarfı 30 sn servis ederdi (v0.9.443/458 dersi: zarf değişimi
//     anahtar sürümü ister).
//  2. ÖLÜ YOL. attachMetricLatency ve yazdığı alanlar kaynakta
//     KALMAMALI; panel artık Explore'un avg yolundan besleniyor.
func TestMetricThroughputCacheKeyIsEnvelopeVersioned(t *testing.T) {
	svc, from, to := planFixture(t)
	k, _ := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180)
	if !strings.HasPrefix(k, "svc-metric-tput:v2:") {
		t.Errorf("zarf sürümü anahtarda değil: %q", k)
	}
}

func TestMetricLatencyPathIsGone(t *testing.T) {
	raw, err := os.ReadFile("service_metric_throughput.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripAPILineComments(string(raw))
	for _, gone := range []string{
		"attachMetricLatency",
		`out["latency"]`,
		`out["latencyUnit"]`,
		`out["latencyUnitKnown"]`,
		`out["latencyDiag"]`,
		"QueryMetricHistogramQuantiles",
		"HistogramLatencyDiag",
		"LatencyScaleToMs",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("panele özel yüzdelik yolu hâlâ kaynakta: %s", gone)
		}
	}
	// Birim yerine geçti — iki eşleşme yolunda da (bağ hızlı yolu +
	// tam keşif). Biri düşerse panelin ekseni birimsiz kalır.
	if got := strings.Count(src, `out["metricUnit"] = s.metricUnitFor(`); got != 2 {
		t.Errorf("metricUnit %d yerde yazılıyor, beklenen 2 (bağ + keşif)", got)
	}
}

// Operatör `=` kullansaydık desen DÜZ METİN olarak aranır ve hiçbir job
// eşleşmezdi; grafik sessizce boş kalır, sebebi de görünmezdi.
func TestMetricThroughputFilterUsesRegexOperator(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "m1", "job", from, to, 600, "", 180)

	if len(f.Filters) != 1 {
		t.Fatalf("tek filtre bekleniyordu, alınan %d", len(f.Filters))
	}
	fe := f.Filters[0]
	if fe.Op != "=~" {
		t.Errorf("operatör =~ olmalı (CH match()), alınan %q", fe.Op)
	}
	if fe.Key != "job" {
		t.Errorf("etiket adı filtreye geçmeli, alınan %q", fe.Key)
	}
	if len(fe.Values) != 1 || !strings.Contains(fe.Values[0], "cm-put-service") {
		t.Errorf("desen servis adını taşımalı, alınan %v", fe.Values)
	}
	// Desen ÇAPALI olmalı — çapasız hâli komşu servisleri de eşler.
	if !strings.HasPrefix(fe.Values[0], "^") || !strings.HasSuffix(fe.Values[0], "$") {
		t.Errorf("desen ^...$ ile çapalanmalı, alınan %q", fe.Values[0])
	}
}

// Etiket adı sorgudan geliyor; filtreye AKTARILMAZSA her kurulum "job"
// arar ve farklı etiket kullananlarda özellik sessizce çalışmaz.
func TestMetricThroughputHonoursCustomJobLabel(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "m1", "kubernetes_job", from, to, 600, "", 180)
	if f.Filters[0].Key != "kubernetes_job" {
		t.Errorf("özel etiket adı filtreye geçmedi: %q", f.Filters[0].Key)
	}
}

// Pencere ve metrik adı filtreye aynen geçmeli — biri düşerse sorgu
// başka bir şey ölçer.
func TestMetricThroughputFilterCarriesWindowAndName(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "http_requests_total", "job", from, to, 600, "", 180)
	if f.Name != "http_requests_total" {
		t.Errorf("metrik adı: %q", f.Name)
	}
	if !f.From.Equal(from) || !f.To.Equal(to) {
		t.Errorf("pencere aktarılmadı: %v..%v", f.From, f.To)
	}
	// Sayaç toplamı SUM olmalı: avg (varsayılan) çok seriyi ortalar ve
	// toplam trafiği olduğundan küçük gösterirdi.
	if f.Aggregation != "sum" {
		t.Errorf("toplama sum olmalı, alınan %q", f.Aggregation)
	}
}

// v0.9.719 — kimlik BAĞI kaynak pinleri: hızlı yol + iki pozitif + iki
// negatif yazım. Saf test Redis ister; pin, akışın kaynakta durduğunu
// çiviler (bugünün sekiz kez işe yarayan deseni).
func TestTputBindingWiring(t *testing.T) {
	raw, err := os.ReadFile("service_metric_throughput.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripAPILineComments(string(raw))
	for what, want := range map[string]int{
		"s.loadTputBinding(":  1,
		"s.storeTputBinding(": 4, // 2 pozitif eşleşme + 2 negatif çıkış
	} {
		if got := strings.Count(src, what); got != want {
			t.Errorf("%s %d yerde, beklenen %d — bağ akışı eksik/fazla", what, got, want)
		}
	}
	// Elle ?metric= ezmesi bağı NE OKUMALI NE YAZMALI.
	if !strings.Contains(src, `if metric == "" && jobLabel == "" {`) {
		t.Error("bağ hızlı yolu elle-ezme korumasız")
	}
}
