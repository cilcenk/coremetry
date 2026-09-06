package mcptools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.472 (Faz 3, F3-1) — attribute keşfi saf parçaları. Adlar SENTETİK.

func TestValueShapeAndDictionary(t *testing.T) {
	if valueShape("apigateway.example.com") != "host" || valueShape("/payment/3dsecure") != "path" || valueShape("ABC123") != "other" || valueShape("a b.c") != "other" {
		t.Fatal("şekil sınıflandırması")
	}
	if ks := dictionaryKeys("host"); len(ks) == 0 || ks[0] != "server.address" {
		t.Errorf("host sözlüğü: %v", ks)
	}
	if ks := dictionaryKeys("path"); len(ks) == 0 || ks[0] != "http.route" {
		t.Errorf("path sözlüğü: %v", ks)
	}
	if dictionaryKeys("other") != nil {
		t.Error("other sözlüğü boş olmalı")
	}
}

func TestMatchSamplesAndTagKeys(t *testing.T) {
	rows := []chstore.ServiceAttrRow{
		{Key: "server.address", Scope: "span", Occurrences: 90, SampleValues: []string{"apigateway.example.com", "db.example.com"}},
		{Key: "url.full", Scope: "span", Occurrences: 80, SampleValues: []string{"https://apigateway.example.com/pay"}},
		{Key: "http.route", Scope: "span", Occurrences: 70, SampleValues: []string{"/pay", "/health"}},
		{Key: "k8s.pod.name", Scope: "resource", Occurrences: 100, SampleValues: []string{"api-1"}},
	}
	m := matchSamples(rows, "APIGATEWAY.example.com")
	if len(m) != 2 || m[0].Key != "server.address" || m[0].Match != "exact" || m[0].FilterOp != "=" || m[1].Key != "url.full" || m[1].Match != "substring" || m[1].FilterOp != "LIKE" {
		t.Fatalf("eşleşmeler: %+v", m)
	}
	if m[0].Basis != "sample" || m[0].Confirmed {
		t.Error("prob öncesi yalnız örneklem kanıtı")
	}
	tags := tagKeys(rows, false)
	byKey := map[string]AttrKeyInfo{}
	for _, k := range tags {
		byKey[k.Key] = k
	}
	if byKey["http.route"].Column != "http_route" || !byKey["http.route"].Indexed || byKey["k8s.pod.name"].Column != "k8s_pod" {
		t.Errorf("kolon etiketleri: %+v", byKey)
	}
	if byKey["server.address"].Indexed || byKey["server.address"].Column != "" {
		t.Errorf("indeks yokken dizi anahtarı indeksli sayılmamalı: %+v", byKey["server.address"])
	}
	if tk := tagKeys(rows, true); !tk[0].Indexed {
		t.Error("kvh varken dizi anahtarı indeksli")
	}
}

func TestAttrScopeRequired(t *testing.T) {
	d := Deps{Clusters: func() []ClusterRef { return ecClusters }}
	if _, err := (attrScopeArgs{}).scope(d); err == nil {
		t.Fatal("kapsamsız istek reddedilmeli")
	}
	sc, err := (attrScopeArgs{Cluster: "eu-west", Namespace: "shop"}).scope(d)
	if err != nil || len(sc.Clusters) != 2 || sc.Namespace != "shop" {
		t.Fatalf("cluster → span değerleri: %+v %v", sc, err)
	}
	if _, err := (attrScopeArgs{Cluster: "zzz"}).scope(d); err == nil {
		t.Error("bilinmeyen cluster hata")
	}
	if clampProbeRange(0) != 1800 || clampProbeRange(99999) != attrProbeMaxRangeS || clampProbeRange(600) != 600 {
		t.Error("pencere kelepçesi")
	}
	// Tool: kapsam yoksa Store'a dokunmadan hata (Store nil).
	if _, err := describeAttributesTool(Deps{}).Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("describe kapsamsız hata")
	}
	if _, err := findAttributeByValueTool(Deps{}).Handler(context.Background(), json.RawMessage(`{"value":"x"}`)); err == nil {
		t.Error("find value kısa/kapsamsız hata")
	}
}
