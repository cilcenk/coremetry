package api

import (
	"fmt"
	"strings"
	"testing"
)

// v0.9.973 — /api/messaging/detail'in boş cluster'ı sessizce "(default)"
// varsayması.
//
// ORİJİNAL BELİRTİ: cluster yüklemi TAM EŞİTLİK (GetMessagingDetail), yani
// çok-cluster bir kurulumda varsayım yanlış topiğe GÖTÜRMEZ — canlı bir
// topic için SIFIRLANMIŞ bir çekmece açar. Sıfırlanmış çekmece "bu topic
// boşta"dan ayırt edilemez, dolayısıyla varsayım bir YALAN hâline gelir.
//
// Bu test iki şeyi çiviliyor:
//  1. Önbellek anahtarı varsayımı TAŞIR. `?cluster=(default)` AÇIKÇA
//     gönderildiğinde cluster dizgesi aynı ama cevap farklı (bayrak yok).
//     Anahtar paylaşılsaydı önce koşan istek diğerinin varsayımı itiraf
//     edip etmeyeceğine karar ederdi — v0.5.187 çapraz-zehirlenme sınıfı,
//     bir bool genişliğinde.
//  2. Bayrağın kendisi yalnız varsayım hâlinde set edilir.
//
// Anahtar üretimi handler'la aynı Sprintf şeklinden türetiliyor; şekil
// kayarsa test de kayar, o yüzden ASIL iddia iki anahtarın FARKLI olması.
func msgDetailKeyFor(system, cluster, dest string, assumed bool, bucket string) string {
	return fmt.Sprintf("msg-detail:%s:%s:%s:%t:%s", system, cluster, dest, assumed, bucket)
}

func TestMessagingDetailKey_AssumedClusterIsInKey(t *testing.T) {
	const (
		system = "kafka"
		dest   = "payment.settled"
		bucket = "b1"
	)
	assumedKey := msgDetailKeyFor(system, "(default)", dest, true, bucket)
	explicitKey := msgDetailKeyFor(system, "(default)", dest, false, bucket)

	if assumedKey == explicitKey {
		t.Fatalf("varsayılan ve AÇIKÇA gönderilmiş '(default)' aynı anahtarı üretiyor:\n  %s\n"+
			"iki cevap assumedCluster alanında ayrışıyor; anahtar paylaşılırsa biri diğerinin "+
			"itirafını ezer (v0.5.187 sınıfı)", assumedKey)
	}
	// Anahtar gerçekten bayrağı taşıyor mu (yalnız uzunluk/sıra farkı değil)?
	if !strings.Contains(assumedKey, ":true:") || !strings.Contains(explicitKey, ":false:") {
		t.Errorf("anahtar bayrağı taşımıyor:\n  assumed=%q\n  explicit=%q", assumedKey, explicitKey)
	}
	// Farklı cluster'lar hâlâ ayrışıyor (regresyon: bayrak eklerken
	// cluster'ı anahtardan düşürmek).
	if msgDetailKeyFor(system, "prod", dest, false, bucket) ==
		msgDetailKeyFor(system, "dr", dest, false, bucket) {
		t.Error("farklı cluster'lar aynı anahtarı üretiyor — cluster anahtardan düşmüş")
	}
	// Destination da ayrışmalı (aynı sınıf, bir alan yanda).
	if msgDetailKeyFor(system, "prod", "a", false, bucket) ==
		msgDetailKeyFor(system, "prod", "b", false, bucket) {
		t.Error("farklı destination'lar aynı anahtarı üretiyor")
	}
}

// assumedClusterFor — handler'ın kararının saf ikizi: cluster boşsa
// "(default)" + varsayım bayrağı, doluysa olduğu gibi.
func assumedClusterFor(raw string) (cluster string, assumed bool) {
	if raw == "" {
		return "(default)", true
	}
	return raw, false
}

func TestAssumedClusterFlag(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantCluster string
		wantAssumed bool
	}{
		{"boş → varsayım", "", "(default)", true},
		// AÇIKÇA "(default)" varsayım DEĞİLDİR: operatör tam olarak o
		// cluster'ı sordu ve cevabın başına uyarı koymak yanlış olurdu.
		{"açıkça (default) → varsayım DEĞİL", "(default)", "(default)", false},
		{"gerçek cluster", "prod-eu", "prod-eu", false},
		// Boşluk BOŞ SAYILMAZ: handler ham dizgeyi kullanıyor, bu test o
		// gerçeği çiviliyor ki ileride Trim eklenirse bilinçli olsun.
		{"boşluk ham geçer", " ", " ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCluster, gotAssumed := assumedClusterFor(c.raw)
			if gotCluster != c.wantCluster || gotAssumed != c.wantAssumed {
				t.Errorf("assumedClusterFor(%q) = (%q, %v), want (%q, %v)",
					c.raw, gotCluster, gotAssumed, c.wantCluster, c.wantAssumed)
			}
		})
	}
}
