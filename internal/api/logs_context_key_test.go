package api

import (
	"strings"
	"testing"
)

// v0.9.1249 — bağlam modalının cache anahtarı pod'u da taşır.
//
// v0.5.187 çapraz-zehirlenme sınıfı, bu yüzeyde somut hâli: operatör
// "⌖ Yalnız bu pod" düğmesine basar, 15 sn'lik TTL içinde aynı ts/n
// için tutulmuş POD'SUZ yanıt servis edilir — düğme çalışmıyor
// görünür, üstelik gösterilen satırlar YANLIŞ podlarındır. Aynı
// simetri env (v0.8.400) ve q (v0.9.1224) için de kuruldu; pod onların
// yanına giriyor.

func TestLogsContextKey_CarriesPod(t *testing.T) {
	base := logsContextKey(1700000000000000000, "payment-api", "prod", "", "", 50)
	podA := logsContextKey(1700000000000000000, "payment-api", "prod", "payment-api-aaa", "", 50)
	podB := logsContextKey(1700000000000000000, "payment-api", "prod", "payment-api-bbb", "", 50)
	if podA == base || podB == base || podA == podB {
		t.Fatalf("pod kapsamları ayrı anahtar üretmeli: base=%q a=%q b=%q", base, podA, podB)
	}
	if !strings.Contains(podA, "pod=payment-api-aaa") {
		t.Fatalf("anahtar pod değerini taşımalı; got %q", podA)
	}
	if logsContextKey(1700000000000000000, "payment-api", "prod", "payment-api-aaa", "", 50) != podA {
		t.Fatal("logsContextKey deterministik olmalı")
	}
}

// Diğer daraltıcı girdiler de anahtarda kalmalı — pod eklerken biri
// düşerse regresyon sessiz olur.
func TestLogsContextKey_CarriesEveryNarrowingInput(t *testing.T) {
	ref := logsContextKey(1, "svc", "prod", "pod-1", "timeout", 50)
	cases := map[string]string{
		"ts":      logsContextKey(2, "svc", "prod", "pod-1", "timeout", 50),
		"service": logsContextKey(1, "svc2", "prod", "pod-1", "timeout", 50),
		"env":     logsContextKey(1, "svc", "uat", "pod-1", "timeout", 50),
		"pod":     logsContextKey(1, "svc", "prod", "pod-2", "timeout", 50),
		"search":  logsContextKey(1, "svc", "prod", "pod-1", "retry", 50),
		"n":       logsContextKey(1, "svc", "prod", "pod-1", "timeout", 100),
	}
	for name, got := range cases {
		if got == ref {
			t.Errorf("%s girdisi anahtarı değiştirmiyor: %q", name, got)
		}
	}
}
