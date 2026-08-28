package thanos

import (
	"regexp"
	"testing"
)

// v0.10.128 — K8s entity katmanı AŞAMA 3 adım 2: Remote Cluster kaydı
// entity hiyerarşisinin KÖKÜ olur (docs/plans/entity-layer-design-2026-08-28.md §1.1).
//
// Sözleşme:
//   - ID opak ve DEĞİŞMEZ; boşsa Name'den türetilir ("c-" + fnv64a(Name)
//     ilk 8 hex) — Name sonradan değişse de türetilmiş id kayıtta kalır
//     (BackfillClusterIDs bir kez yazar).
//   - Thanos etiketi: ad BOŞ = matcher YOK = eski davranış (cluster başına
//     URL modeli bozulmaz). Ad doluysa değer boşken Name kullanılır.
//   - Span cluster değeri boşsa Name (bugünkü join anahtarı).

func TestDerivedClusterIDIsStableAndOpaque(t *testing.T) {
	a := derivedClusterID("ocp-prod-1")
	b := derivedClusterID("ocp-prod-1")
	c := derivedClusterID("ocp-prod-2")
	if a != b {
		t.Fatalf("türetilmiş id kararlı olmalı: %s != %s", a, b)
	}
	if a == c {
		t.Fatalf("farklı adlar farklı id vermeli: %s", a)
	}
	if !regexp.MustCompile(`^c-[0-9a-f]{8}$`).MatchString(a) {
		t.Fatalf("id biçimi c-<8 hex> olmalı, alınan %q", a)
	}
	if derivedClusterID("") != "" {
		t.Fatal("boş ad boş id vermeli (kayıt zaten reddedilir)")
	}
}

func TestClusterConfigEffectiveValues(t *testing.T) {
	base := ClusterConfig{Name: "ocp-prod-1", URL: "https://thanos", Enabled: true}
	if got := base.EffectiveID(); got != derivedClusterID("ocp-prod-1") {
		t.Fatalf("ID boşken türetilmiş id dönmeli, alınan %q", got)
	}
	withID := base
	withID.ID = "c-deadbeef"
	if got := withID.EffectiveID(); got != "c-deadbeef" {
		t.Fatalf("kayıtlı ID kazanmalı, alınan %q", got)
	}
	// Thanos etiketi: ad boş → matcher yok.
	if name, val := base.EffectiveThanosLabel(); name != "" || val != "" {
		t.Fatalf("etiket adı boşken matcher olmamalı, alınan %q=%q", name, val)
	}
	lab := base
	lab.ThanosLabelName = "cluster"
	if name, val := lab.EffectiveThanosLabel(); name != "cluster" || val != "ocp-prod-1" {
		t.Fatalf("ad doluyken değer Name'e düşmeli, alınan %q=%q", name, val)
	}
	lab.ThanosLabelValue = "prod-1"
	if _, val := lab.EffectiveThanosLabel(); val != "prod-1" {
		t.Fatalf("açık değer kazanmalı, alınan %q", val)
	}
	if got := base.SpanClusterKey(); got != "ocp-prod-1" {
		t.Fatalf("span cluster değeri boşken Name, alınan %q", got)
	}
	sp := base
	sp.SpanClusterValue = "ocp-prod-1-spans"
	if got := sp.SpanClusterKey(); got != "ocp-prod-1-spans" {
		t.Fatalf("açık span değeri kazanmalı, alınan %q", got)
	}
}

func TestBackfillClusterIDs(t *testing.T) {
	cfg := Settings{Clusters: []ClusterConfig{
		{Name: "a", URL: "https://a", Enabled: true},
		{Name: "b", URL: "https://b", ID: "c-00000001"},
		{Name: "", URL: "https://x"}, // adı boş kayıt: id üretilmez, dokunulmaz
	}}
	out, changed := BackfillClusterIDs(cfg)
	if !changed {
		t.Fatal("boş id doldurulduğunda changed=true olmalı")
	}
	if out.Clusters[0].ID != derivedClusterID("a") {
		t.Fatalf("a için türetilmiş id beklenir, alınan %q", out.Clusters[0].ID)
	}
	if out.Clusters[1].ID != "c-00000001" {
		t.Fatalf("mevcut id korunmalı, alınan %q", out.Clusters[1].ID)
	}
	if out.Clusters[2].ID != "" {
		t.Fatalf("adsız kayda id yazılmamalı, alınan %q", out.Clusters[2].ID)
	}
	// Girdi DEĞİŞTİRİLMEZ (tam-blob yazım çağıranın işi).
	if cfg.Clusters[0].ID != "" {
		t.Fatal("BackfillClusterIDs girdiyi yerinde değiştirmemeli")
	}
	// İkinci koşum: değişiklik yok.
	if _, again := BackfillClusterIDs(out); again {
		t.Fatal("dolu kayıtta ikinci koşum changed=false olmalı")
	}
}

func TestClusterByIDAndName(t *testing.T) {
	s := &Service{}
	s.Configure(Settings{Clusters: []ClusterConfig{
		{Name: "a", URL: "https://a", Enabled: true, ID: "c-aaaaaaaa"},
		{Name: "b", URL: "https://b", Enabled: false, ID: "c-bbbbbbbb"},
	}})
	if c, ok := s.ClusterByID("c-aaaaaaaa"); !ok || c.Name != "a" {
		t.Fatalf("id ile bulunmalı, alınan %+v %v", c, ok)
	}
	if _, ok := s.ClusterByID("c-bbbbbbbb"); ok {
		t.Fatal("kapalı kayıt id ile de dönmemeli (ClusterByName ile aynı sözleşme)")
	}
	// Geriye uyumluluk: ?cluster= eski URL'lerde Name taşır.
	if c, ok := s.ClusterByRef("a"); !ok || c.Name != "a" {
		t.Fatalf("ad ile de çözülmeli, alınan %+v %v", c, ok)
	}
	if c, ok := s.ClusterByRef("c-aaaaaaaa"); !ok || c.Name != "a" {
		t.Fatalf("id ile de çözülmeli, alınan %+v %v", c, ok)
	}
}

// PUT sözleşmesi: ID sunucu sahipli. İstemci gelen kayıt için (a) gönderdiği
// ID saklı bir kayda aitse o kayıt yeniden adlandırılıyordur → id VE token
// korunur; (b) aynı ad saklıysa saklı id (+ boş token yerine saklı token);
// (c) yeni kayıt → Name'den türetilmiş id (istemcinin uydurduğu id
// alınmaz — sunucu sahipli). Etiket adı geçersizse hata.
func TestReconcileClusterSettings(t *testing.T) {
	cur := Settings{Clusters: []ClusterConfig{
		{ID: "c-aaaaaaaa", Name: "a", URL: "https://a", Token: "tok-a", Enabled: true},
		{ID: "c-bbbbbbbb", Name: "b", URL: "https://b", Token: "tok-b", Enabled: true},
	}}
	in := Settings{Clusters: []ClusterConfig{
		{ID: "c-aaaaaaaa", Name: "a-renamed", URL: "https://a", Enabled: true},              // (a) yeniden adlandırma
		{Name: "b", URL: "https://b", Token: "", Enabled: true, ThanosLabelName: "cluster"}, // (b) aynı ad
		{ID: "c-uydurma1", Name: "n", URL: "https://n", Token: "tok-n", Enabled: true},      // (c) yeni
	}}
	out, err := ReconcileClusterSettings(in, cur)
	if err != nil {
		t.Fatal(err)
	}
	if out.Clusters[0].ID != "c-aaaaaaaa" || out.Clusters[0].Token != "tok-a" {
		t.Fatalf("yeniden adlandırma id+token korumalı: %+v", out.Clusters[0])
	}
	if out.Clusters[1].ID != "c-bbbbbbbb" || out.Clusters[1].Token != "tok-b" || out.Clusters[1].ThanosLabelName != "cluster" {
		t.Fatalf("aynı ad saklı id+token almalı, alanlar korunmalı: %+v", out.Clusters[1])
	}
	if out.Clusters[2].ID != derivedClusterID("n") || out.Clusters[2].Token != "tok-n" {
		t.Fatalf("yeni kayıt türetilmiş id almalı (istemci id'si alınmaz): %+v", out.Clusters[2])
	}
	bad := Settings{Clusters: []ClusterConfig{{Name: "x", URL: "https://x", ThanosLabelName: "not a label"}}}
	if _, err := ReconcileClusterSettings(bad, cur); err == nil {
		t.Fatal("geçersiz etiket adı reddedilmeli")
	}
	if ValidThanosLabelName("") != true || ValidThanosLabelName("cluster_id") != true || ValidThanosLabelName("9x") != false {
		t.Fatal("etiket adı doğrulaması: boş ok, cluster_id ok, 9x hayır")
	}
}
