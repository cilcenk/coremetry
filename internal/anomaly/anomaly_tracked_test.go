package anomaly

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.800 — kapalı bir metrik HİÇ ölçülmemeli.
//
// Vidanın tek anlamlı sözü bu: request_rate kapalıyken tarayıcı onun
// için ne consecutive ne de seasonal toplu okumayı açmalı (yan kazanç:
// tik başına 2 MV sorgusu düşer), ve serisi olmadığı için checkOne da
// onun adına hiç karar vermemeli. "Ayar kaydediliyor ama detektör yine
// de ölçüyor" bu depoda tekrarlayan sessiz-başarısızlık sınıfı olurdu.

// TestBatchSeriesSkipsDisabledMetric — casus okuyucular: izlenen
// listede olmayan metrik için TEK bir sorgu bile açılmaz.
func TestBatchSeriesSkipsDisabledMetric(t *testing.T) {
	var bucketCalls, seasonalCalls []string
	fetchBuckets := func(m string) (map[string][]float64, map[string][]float64, error) {
		bucketCalls = append(bucketCalls, m)
		return map[string][]float64{"svc-a": {1, 2, 3}}, map[string][]float64{"svc-a": {9, 9, 9}}, nil
	}
	fetchSeasonal := func(m string) (map[string][]float64, error) {
		seasonalCalls = append(seasonalCalls, m)
		return map[string][]float64{"svc-a": {1, 2, 3}}, nil
	}

	// Varsayılan ayar seti = tarayıcının gerçekte dolaşacağı liste.
	tracked := chstore.DefaultAnomalyTracked().Enabled()
	if !reflect.DeepEqual(tracked, []string{"error_rate", "p99_ms"}) {
		t.Fatalf("varsayılan izlenen set %v — bu test request_rate'in KAPALI olduğunu varsayıyor", tracked)
	}

	buckets, seasonal, _ := batchSeries(tracked, fetchBuckets, fetchSeasonal)

	if !reflect.DeepEqual(bucketCalls, []string{"error_rate", "p99_ms"}) {
		t.Errorf("consecutive okumalar %v — kapalı metrik için sorgu açıldı", bucketCalls)
	}
	if !reflect.DeepEqual(seasonalCalls, []string{"error_rate", "p99_ms"}) {
		t.Errorf("seasonal okumalar %v — kapalı metrik için sorgu açıldı", seasonalCalls)
	}
	if _, ok := buckets["request_rate"]; ok {
		t.Errorf("kapalı metrik seriye girdi: %v", buckets)
	}
	if _, ok := seasonal["request_rate"]; ok {
		t.Errorf("kapalı metrik seasonal seriye girdi: %v", seasonal)
	}
	// Serisi olmayan metrik için checkOne'ın eline boş dizi geçer —
	// enoughHistory eler, yani karar da üretilmez.
	if got := seriesFor(buckets["request_rate"], "svc-a"); len(got) != 0 {
		t.Errorf("seriesFor(kapalı metrik) = %v, want boş", got)
	}
	if enoughHistory(0, defDwell()) {
		t.Errorf("enoughHistory(0) = true — kapalı metrik yine de değerlendirilirdi")
	}
}

// TestBatchSeriesAllOn — operatör vidayı geri açtığında üç metrik de
// ölçülür (geri dönüş tek tık: sürüm değil ayar).
func TestBatchSeriesAllOn(t *testing.T) {
	var calls []string
	fetch := func(m string) (map[string][]float64, map[string][]float64, error) {
		calls = append(calls, m)
		return nil, nil, nil
	}
	cfg := chstore.AnomalyTrackedConfig{"error_rate": true, "p99_ms": true, "request_rate": true}
	batchSeries(cfg.Enabled(), fetch, nil)
	if !reflect.DeepEqual(calls, []string{"error_rate", "p99_ms", "request_rate"}) {
		t.Errorf("hepsi açıkken okunan metrikler %v, want üçü de kanonik sırada", calls)
	}
}

// TestBatchSeriesErrorBehaviour — v0.8.507 hata davranışı korunuyor:
// consecutive hatası metriği tamamen atlar (seasonal okumasına bile
// geçilmez), seasonal hatası best-effort'tur ve consecutive seri
// yerinde kalır.
func TestBatchSeriesErrorBehaviour(t *testing.T) {
	boom := errors.New("ch timeout")
	seasonalSeen := []string{}
	buckets, seasonal, _ := batchSeries([]string{"error_rate", "p99_ms"},
		func(m string) (map[string][]float64, map[string][]float64, error) {
			if m == "error_rate" {
				return nil, nil, boom
			}
			return map[string][]float64{"svc-a": {1, 2}}, map[string][]float64{"svc-a": {5, 5}}, nil
		},
		func(m string) (map[string][]float64, error) {
			seasonalSeen = append(seasonalSeen, m)
			return nil, boom
		})

	if _, ok := buckets["error_rate"]; ok {
		t.Errorf("consecutive hatası sonrası metrik seriye girdi: %v", buckets)
	}
	if !reflect.DeepEqual(seasonalSeen, []string{"p99_ms"}) {
		t.Errorf("seasonal okumaları %v — consecutive hatası alan metrik için seasonal açılmamalıydı", seasonalSeen)
	}
	if len(buckets["p99_ms"]) == 0 {
		t.Errorf("seasonal hatası consecutive seriyi düşürdü: %v", buckets)
	}
	if _, ok := seasonal["p99_ms"]; ok {
		t.Errorf("hatalı seasonal okuması haritaya girdi: %v", seasonal)
	}
}

// TestTrackedMetricSetMatchesDetectorKnowledge — chstore'daki kanonik
// liste ile dedektörün bildiği metrikler AYNI küme olmalı. chstore
// anomaly'yi import edemediği için liste orada tekrarlanıyor
// (Seasonal* alanlarındaki v0.8.250 tekrarı gibi); bu test o iki
// kopyanın ayrışmasını yakalar.
func TestTrackedMetricSetMatchesDetectorKnowledge(t *testing.T) {
	for _, m := range chstore.AnomalyTrackedMetrics {
		if _, err := metricValueExpr(m); err != nil {
			t.Errorf("kanonik metrik %q dedektörde tanımsız: %v", m, err)
		}
		if _, ok := metricDirections[m]; !ok {
			t.Errorf("kanonik metrik %q için yön tanımı yok", m)
		}
		// v0.9.826 — eşikler de kanonik varsayılan sette olmalı; eksikse
		// operatör o metriğin hassasiyetini ayarlayamaz.
		if _, ok := chstore.DefaultAnomalySensitivity().Metrics[m]; !ok {
			t.Errorf("kanonik metrik %q için varsayılan hassasiyet yok", m)
		}
	}
	for m := range metricDirections {
		found := false
		for _, c := range chstore.AnomalyTrackedMetrics {
			if c == m {
				found = true
			}
		}
		if !found {
			t.Errorf("dedektör %q metriğini biliyor ama kanonik ayar listesinde yok — operatör onu açıp kapatamaz", m)
		}
	}
}
