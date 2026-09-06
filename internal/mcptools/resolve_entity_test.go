package mcptools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cilcenk/coremetry/internal/entity"
)

// v0.10.469 (Faz 2, F2-2) — serbest metin → varlık adayları. Adlar SENTETİK.

func reIndex() EntityCatalogIndex {
	return EntityCatalogIndex{
		EntityLayer: true,
		Services:    []string{"shop-payment-api", "shop-payment-worker", "core-auth", "inventory"},
		Namespaces: []EntityCandidate{
			{Kind: entity.TypeNamespace, Cluster: "eu-west", ClusterID: "c-1", Name: "shop-payment"},
			{Kind: entity.TypeNamespace, Cluster: "eu-central", ClusterID: "c-2", Name: "shop-payment"},
			{Kind: entity.TypeNamespace, Cluster: "eu-west", ClusterID: "c-1", Name: "shop"},
			{Kind: entity.TypeNamespace, Cluster: "eu-west", ClusterID: "c-1", Name: "core"},
		},
		Workloads: []EntityCandidate{
			{Kind: entity.TypeWorkload, Cluster: "eu-west", ClusterID: "c-1", Namespace: "shop-payment", Name: "payment-api", WlKind: "Deployment"},
			{Kind: entity.TypeWorkload, Cluster: "eu-west", ClusterID: "c-1", Namespace: "shop", Name: "catalog", WlKind: "Deployment"},
		},
	}
}

func TestResolveEntitiesShapes(t *testing.T) {
	idx := reIndex()
	// Tam eş: iki cluster'daki namespace + tam eşler yalnız (servis önekleri düşer).
	c := ResolveEntities("shop-payment", idx, 12)
	if len(c) != 2 || c[0].Kind != entity.TypeNamespace || c[1].Kind != entity.TypeNamespace || c[0].Cluster == c[1].Cluster {
		t.Fatalf("tam eş iki cluster: %+v", c)
	}
	if ResolvedOne(c) != nil {
		t.Error("iki cluster → çözülmüş sayılmamalı")
	}
	// Önek: "shop-payment-" → servisler (prefix) ve namespace substring? "shop-payment-a" → sadece prefix servis.
	c = ResolveEntities("shop-payment-api", idx, 12)
	if one := ResolvedOne(c); one == nil || one.Kind != entity.TypeService || one.Name != "shop-payment-api" {
		t.Fatalf("tek tam servis: %+v", c)
	}
	// Jeton kapsaması: "shop pay" → namespace'ler (2 cluster) + servisler + workload? "payment-api" jetonları pay(ment) evet shop hayır → düşer.
	c = ResolveEntities("shop pay", idx, 12)
	kinds := map[string]int{}
	for _, x := range c {
		kinds[x.Kind]++
	}
	if kinds[entity.TypeNamespace] != 2 || kinds[entity.TypeService] != 2 || kinds[entity.TypeWorkload] != 0 {
		t.Fatalf("jeton kapsaması türleri: %+v", c)
	}
	// Yazım hatası → fuzzy.
	c = ResolveEntities("core-auht", idx, 12)
	if one := ResolvedOne(c); one == nil || one.Name != "core-auth" || one.Match != "fuzzy" {
		t.Fatalf("yazım hatası: %+v", c)
	}
	// Yok → boş; kısa → boş.
	if c = ResolveEntities("zzqx", idx, 12); len(c) != 0 {
		t.Errorf("uydurma: %+v", c)
	}
	if c = ResolveEntities("sh", idx, 12); len(c) != 0 {
		t.Errorf("kısa metin: %+v", c)
	}
	// Bayrak kapalı: yalnız servis ekseni (Namespaces boş verilir).
	off := EntityCatalogIndex{Services: idx.Services}
	if c = ResolveEntities("shop-payment", off, 12); len(c) != 2 || c[0].Kind != entity.TypeService {
		t.Errorf("kapalı katman → servis önekleri: %+v", c)
	}
}

func TestPodShaped(t *testing.T) {
	for _, ok := range []string{"payment-api-7d9f8c6b5-x2k9q", "db-0", "Payment-API-5c8d9-abcde"} {
		if !podShaped(ok) {
			t.Errorf("%q pod-şekilli olmalı", ok)
		}
	}
	for _, no := range []string{"shop-payment", "payment-api", "core"} {
		if podShaped(no) {
			t.Errorf("%q pod-şekilli DEĞİL", no)
		}
	}
}

func TestResolveEntityToolArgs(t *testing.T) {
	tool := resolveEntityTool(Deps{})
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"text":"ab"}`)); err == nil {
		t.Error("kısa metin BadArgs")
	}
	if tool.InputSchema["required"].([]string)[0] != "text" {
		t.Error("text zorunlu")
	}
}
