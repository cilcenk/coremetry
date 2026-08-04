// v0.9.619 — resource.* filtresi tipli kolona düşmeliydi.
//
// FAZ 1 ortak katman denetimi: `resource.` öneki görülür görülmez
// koşulsuz dizi araması kuruluyordu. `resource.service.name = X`
// spans'in birincil anahtar budamasını (ORDER BY service_name, time)
// TAMAMEN kaybediyordu — aynı soru `service.name = X` yazılınca
// indeksli kolona düşerken.
//
// Üstelik UI operatörü oraya İTİYOR: FilterBuilder'ın öneri listesi
// `resource.service.name` sunuyor. Önerilen yol, yavaş yoldu.
package chstore

import (
	"strings"
	"testing"
)

func sqlFor(t *testing.T, key, op string, vals ...string) string {
	t.Helper()
	var wc whereClause
	ApplyFilters(&wc, []FilterExpr{{Key: key, Op: op, Values: vals}})
	return wc.sql()
}

// TestResourceKeysUseTypedColumns — üç eşleme de indeksli kolona.
func TestResourceKeysUseTypedColumns(t *testing.T) {
	cases := map[string]string{
		"resource.service.name":                "service_name",
		"resource.host.name":                   "host_name",
		"resource.deployment.environment":      "deploy_env",
		"resource.deployment.environment.name": "deploy_env",
	}
	for key, col := range cases {
		t.Run(key, func(t *testing.T) {
			got := sqlFor(t, key, "=", "x")
			if !strings.Contains(got, col) {
				t.Errorf("%s tipli kolona düşmedi (%s bekleniyordu): %s", key, col, got)
			}
			if strings.Contains(got, "res_values[indexOf") {
				t.Errorf("%s HÂLÂ dizi araması yapıyor — birincil anahtar budaması "+
					"kayboluyor ve UI operatörü tam bu forma yönlendiriyor: %s", key, got)
			}
		})
	}
}

// TestResourcePrefixDoesNotStealSpanColumns — EN KRİTİK VAKA.
//
// wellKnown'ın çoğu SPAN attribute'undan doluyor. `resource.` dalına
// o haritayı vermek `resource.http.method` sorgusunu http_method
// kolonuna düşürürdü — o kolon resource'tan DOLMUYOR, yani sessizce
// YANLIŞ sonuç. Hızlı ve yanlış, yavaş ve doğrudan kötüdür.
func TestResourcePrefixDoesNotStealSpanColumns(t *testing.T) {
	for _, key := range []string{
		"resource.http.method", "resource.db.system", "resource.rpc.method",
		"resource.peer.service", "resource.messaging.system", "resource.http.route",
		"resource.status_code", "resource.kind",
	} {
		t.Run(key, func(t *testing.T) {
			got := sqlFor(t, key, "=", "x")
			if !strings.Contains(got, "res_values[indexOf") {
				t.Errorf("%s dizi aramasından ÇIKMIŞ: %s\n\nBu kolonlar SPAN "+
					"attribute'undan doluyor; resource sorgusunu oraya düşürmek "+
					"sessizce yanlış sonuç verir.", key, got)
			}
		})
	}
}

// TestSpanPrefixUnchanged — span dalı bozulmadı.
func TestSpanPrefixUnchanged(t *testing.T) {
	if got := sqlFor(t, "span.http.method", "=", "GET"); !strings.Contains(got, "http_method") {
		t.Errorf("span.http.method tipli kolonu kaybetti: %s", got)
	}
	if got := sqlFor(t, "span.custom.thing", "=", "x"); !strings.Contains(got, "attr_values[indexOf") {
		t.Errorf("span.<bilinmeyen> dizi aramasında kalmalı: %s", got)
	}
}

// TestResourceUnknownStaysArray — bilinmeyen resource anahtarı.
func TestResourceUnknownStaysArray(t *testing.T) {
	got := sqlFor(t, "resource.k8s.pod.name", "=", "p1")
	if !strings.Contains(got, "res_values[indexOf") {
		t.Errorf("bilinmeyen resource anahtarı dizi aramasında kalmalı: %s", got)
	}
}

// TestMetricPathKeepsArrayForResource — TABLO AYRIMI.
//
// Bu düzeltme ilk yazımda metrik yoluna SIZDI ve mevcut bir test onu
// yakaladı: metric_points'te deploy_env kolonu YOK, dolayısıyla
// `resource.deployment.environment` orada dizi aramasında KALMALI.
//
// Ders: resource→kolon eşlemesi TABLOYA ÖZEL. Builder wellKnown'ı
// zaten parametre olarak alıyordu; resource haritasını sabit yazmak
// o ayrımı sessizce deliyordu.
func TestMetricPathKeepsArrayForResource(t *testing.T) {
	for _, key := range []string{
		"resource.deployment.environment", "resource.service.name", "resource.host.name",
	} {
		t.Run(key, func(t *testing.T) {
			var wc whereClause
			ApplyMetricFilters(&wc, []FilterExpr{{Key: key, Op: "=", Values: []string{"x"}}})
			got := wc.sql()
			if !strings.Contains(got, "res_values[indexOf") {
				t.Errorf("metrik yolunda %s dizi aramasından çıkmış: %s\n\n"+
					"metric_points'te spans'in tipli kolonları YOK — oraya "+
					"düşmek ClickHouse code 47 (bilinmeyen kolon) demek.", key, got)
			}
		})
	}
}
