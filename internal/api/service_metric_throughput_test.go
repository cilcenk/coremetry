package api

import (
	"strings"
	"testing"
	"time"
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
	base, _ := metricThroughputPlan(svc, "m1", "job", from, to)

	cases := map[string]string{
		"servis":       mustKey(metricThroughputPlan("other-service", "m1", "job", from, to)),
		"metrik adı":   mustKey(metricThroughputPlan(svc, "m2", "job", from, to)),
		"etiket adı":   mustKey(metricThroughputPlan(svc, "m1", "service_job", from, to)),
		"zaman kovası": mustKey(metricThroughputPlan(svc, "m1", "job", from.Add(-48*time.Hour), to)),
	}
	for name, k := range cases {
		if k == base {
			t.Errorf("%s değişti ama önbellek anahtarı AYNI kaldı — çapraz zehirlenme", name)
		}
	}

	// Aynı girdi → aynı anahtar (yoksa önbellek hiç isabet etmez).
	if again, _ := metricThroughputPlan(svc, "m1", "job", from, to); again != base {
		t.Error("aynı girdi farklı anahtar üretti — önbellek asla isabet etmez")
	}
}

func mustKey(k string, _ any) string { return k }

// Operatör `=` kullansaydık desen DÜZ METİN olarak aranır ve hiçbir job
// eşleşmezdi; grafik sessizce boş kalır, sebebi de görünmezdi.
func TestMetricThroughputFilterUsesRegexOperator(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "m1", "job", from, to)

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
	_, f := metricThroughputPlan(svc, "m1", "kubernetes_job", from, to)
	if f.Filters[0].Key != "kubernetes_job" {
		t.Errorf("özel etiket adı filtreye geçmedi: %q", f.Filters[0].Key)
	}
}

// Pencere ve metrik adı filtreye aynen geçmeli — biri düşerse sorgu
// başka bir şey ölçer.
func TestMetricThroughputFilterCarriesWindowAndName(t *testing.T) {
	svc, from, to := planFixture(t)
	_, f := metricThroughputPlan(svc, "http_requests_total", "job", from, to)
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
