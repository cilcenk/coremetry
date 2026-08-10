package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.943 — UX denetimi B3 / Ö5: /traces `cluster` ölü paramdı.
//
// ORİJİNAL BELİRTİ: /endpoints'te bir cluster seçip satırın "Traces →"
// linkine basan operatör TÜM cluster'ların trace'lerini görüyordu. Link
// v0.9.307'den beri `&cluster=` yazıyor, /traces ise okumuyordu — pivot
// soruyu SESSİZCE genişletiyordu.
//
// H15 KAPISI (denetimin en yüksek riskli bulgusu): cluster conjunct'ı
// KOŞULSUZ eklenirse `/api/traces` — uygulamanın en pahalı okuması —
// trace_summary_5m hızlı yolunu HER İSTEKTE kaybeder. Bu dosyanın asıl
// işi o kapıyı çivilemek: SQL ŞEKLİ testi, cluster boşken conjunct
// ÇIKMAMALI ve MV uygunluğu BOZULMAMALI.

func TestGetTracesWhere_ClusterConjunctIsConditional(t *testing.T) {
	from := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	t.Run("boş cluster hiçbir conjunct üretmez", func(t *testing.T) {
		wc := buildGetTracesWhere(TraceFilter{From: from, To: to}, clusterColExpr)
		sql := wc.sql()
		if strings.Contains(sql, "cluster") {
			t.Fatalf("boş cluster conjunct ÜRETMEMELİ; got %q", sql)
		}
		for _, a := range wc.args {
			if s, ok := a.(string); ok && s == "" {
				t.Fatalf("boş cluster bind arg BIRAKMAMALI; args=%v", wc.args)
			}
		}
	})

	t.Run("dolu cluster conjunct üretir ve DEĞERİ bind eder", func(t *testing.T) {
		wc := buildGetTracesWhere(TraceFilter{From: from, To: to, Cluster: "ocp-a"}, clusterColExpr)
		sql := wc.sql()
		if !strings.Contains(sql, clusterColExpr+" = ?") {
			t.Fatalf("cluster conjunct yok; got %q", sql)
		}
		found := false
		for _, a := range wc.args {
			if s, ok := a.(string); ok && s == "ocp-a" {
				found = true
			}
		}
		if !found {
			t.Fatalf("cluster DEĞERİ bind edilmemiş; args=%v", wc.args)
		}
	})

	t.Run("ifade ÇAĞIRANDAN gelir — derive dalı da geçer", func(t *testing.T) {
		// Harici Distributed'da `cluster` kolonu yoktur ve Store
		// clusterDeriveExpr'e düşer (v0.8.162, code 47 olayı). Saf
		// fonksiyon o kararı VERMEZ, taşır.
		wc := buildGetTracesWhere(TraceFilter{From: from, To: to, Cluster: "ocp-a"}, clusterDeriveExpr)
		if !strings.Contains(wc.sql(), "k8s.cluster.name") {
			t.Fatalf("derive ifadesi geçilince SQL onu taşımalı; got %q", wc.sql())
		}
	})

	t.Run("cluster env ve filtrelerle BİRLİKTE yaşar (supersede kuralına takılmaz)", func(t *testing.T) {
		// TraceFilter.Env'in birinci-sınıf olma gerekçesinin aynısı:
		// FilterRoot, Filters'ı SUPERSEDE eder; cluster bir leaf olsaydı
		// gruplu bir sorguda sessizce kaybolurdu.
		wc := buildGetTracesWhere(TraceFilter{
			From: from, To: to, Env: "uat", Cluster: "ocp-a",
			FilterRoot: &FilterGroup{Join: "OR", Filters: []FilterExpr{
				{Key: "http.status_code", Op: "=", Values: []string{"500"}},
				{Key: "http.status_code", Op: "=", Values: []string{"503"}},
			}},
		}, clusterColExpr)
		sql := wc.sql()
		if !strings.Contains(sql, "deploy_env = ?") {
			t.Errorf("env conjunct kayboldu: %q", sql)
		}
		if !strings.Contains(sql, clusterColExpr+" = ?") {
			t.Errorf("cluster conjunct kayboldu: %q", sql)
		}
		if !strings.Contains(sql, "OR") {
			t.Errorf("gruplu filtre kayboldu: %q", sql)
		}
	})
}

// tracesMVEligible — H15'in ASIL kapısı. Boş cluster hızlı yolu AÇIK
// bırakmalı; dolu cluster kapatmalı.
func TestTracesMVEligible_ClusterGate(t *testing.T) {
	from := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	base := TraceFilter{From: from, To: from.Add(time.Hour)}

	if !tracesMVEligible(base) {
		t.Fatal("önkoşul kırık: filtresiz istek MV'ye UYGUN olmalı")
	}
	if !tracesMVEligible(func() TraceFilter { f := base; f.Cluster = ""; return f }()) {
		t.Error("BOŞ cluster hızlı yolu KAPATMAMALI — /api/traces'in en pahalı okuması her istekte ham spans'e düşerdi")
	}
	if tracesMVEligible(func() TraceFilter { f := base; f.Cluster = "ocp-a"; return f }()) {
		t.Error("DOLU cluster MV'yi diskalifiye ETMELİ — trace_summary_5m'de cluster boyutu YOK, sessizce yanlış küme dönerdi")
	}
}

// addClusterConjunct — liste ve toplu görünümün PAYLAŞTIĞI tek yazım
// yeri. Toplu görünüm listeyle aynı kapsamı okumalı: /traces'te sekme
// değiştirmek soruyu genişletemez.
func TestAddClusterConjunct(t *testing.T) {
	cases := []struct {
		name, cluster string
		wantSQL       bool
	}{
		{"boş", "", false},
		{"dolu", "ocp-a", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var wc whereClause
			addClusterConjunct(&wc, clusterColExpr, c.cluster)
			got := strings.Contains(wc.sql(), clusterColExpr+" = ?")
			if got != c.wantSQL {
				t.Errorf("conjunct=%v, beklenen %v; sql=%q", got, c.wantSQL, wc.sql())
			}
			if !c.wantSQL && len(wc.args) != 0 {
				t.Errorf("boş cluster arg BIRAKMAMALI: %v", wc.args)
			}
		})
	}
}
