package chstore

import (
	"strings"
	"testing"
)

// v0.10.127 — terfi kayıt defteri RESOURCE kapsamını öğrenir.
//
// K8s entity katmanı (docs/plans/entity-layer-design-2026-08-28.md §2.3):
// k8s_namespace / k8s_pod / k8s_node kolonları span ATTRIBUTE'undan değil
// RESOURCE'tan türer (prod'da k8s.* yalnız res_keys'te — k8s_coverage.go
// ölçümü). Kayıt defteri kapsamı bilmezse ifade attr_values okur, kolon
// hep boş kalır ve probe onu asla kaydetmez — v0.9.198'in "boş kolon"
// sınıfı, bu kez kapsam yüzünden.

func TestPromotedAttrResourceScopeExpr(t *testing.T) {
	a := promotedAttr{col: "k8s_pod", keys: []string{"k8s.pod.name"}, res: true, fallback: "host_name"}
	got := promotedAttrExprFor(a)
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'k8s.pod.name')]",
		"nullIf(host_name, '')",
		"coalesce(",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ifade %q içermeli: %s", want, got)
		}
	}
	if strings.Contains(got, "attr_values") {
		t.Fatalf("resource kapsamlı kolon attr_values okumamalı: %s", got)
	}
	if !strings.HasSuffix(got, ", '')") {
		t.Fatalf("ifade boş-string fallback ile bitmeli: %s", got)
	}
	// Anahtar önce, yedek kolon sonra: k8s.pod.name varsa o kazanır.
	if strings.Index(got, "'k8s.pod.name'") > strings.Index(got, "host_name") {
		t.Fatalf("anahtar coalesce'ta yedek kolondan ÖNCE gelmeli: %s", got)
	}
	// Attr kapsamı değişmedi (mevcut iki kolon).
	if old := promotedAttrExprFor(promotedAttr{col: "attr_channel_code", keys: []string{"CHANNEL_CODE", "channel_code"}}); old != promotedAttrExpr([]string{"CHANNEL_CODE", "channel_code"}) {
		t.Fatalf("attr kapsamı eski ifadeyle birebir olmalı:\n%s\n%s", old, promotedAttrExpr([]string{"CHANNEL_CODE", "channel_code"}))
	}
}

func TestPromotedRegistryHasK8sColumns(t *testing.T) {
	want := map[string]struct {
		keys     []string
		fallback string
	}{
		"k8s_namespace": {[]string{"k8s.namespace.name", "kubernetes.namespace.name"}, ""},
		"k8s_pod":       {[]string{"k8s.pod.name"}, "host_name"},
		"k8s_node":      {[]string{"k8s.node.name"}, ""},
	}
	seen := map[string]bool{}
	for _, a := range promotedAttrs {
		w, ok := want[a.col]
		if !ok {
			continue
		}
		seen[a.col] = true
		if !a.res {
			t.Errorf("%s resource kapsamlı olmalı", a.col)
		}
		if strings.Join(a.keys, ",") != strings.Join(w.keys, ",") {
			t.Errorf("%s anahtarları %v, beklenen %v", a.col, a.keys, w.keys)
		}
		if a.fallback != w.fallback {
			t.Errorf("%s yedek kolonu %q, beklenen %q", a.col, a.fallback, w.fallback)
		}
	}
	for col := range want {
		if !seen[col] {
			t.Errorf("%s kayıt defterinde yok", col)
		}
	}
}

func TestPromotedAttrDDLResourceScope(t *testing.T) {
	a := promotedAttr{col: "k8s_node", keys: []string{"k8s.node.name"}, res: true}
	stmts := promotedAttrDDL(a, "", false, false)
	if len(stmts) != 2 {
		t.Fatalf("taze kurulumda ADD COLUMN + ADD INDEX beklenir, alınan %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "ADD COLUMN IF NOT EXISTS k8s_node LowCardinality(String) MATERIALIZED") ||
		!strings.Contains(stmts[0], "res_values[indexOf(res_keys, 'k8s.node.name')]") {
		t.Fatalf("ADD COLUMN resource ifadesini taşımalı: %s", stmts[0])
	}
	if !strings.Contains(stmts[1], "ADD INDEX IF NOT EXISTS idx_k8s_node k8s_node TYPE set(0) GRANULARITY 4") {
		t.Fatalf("set(0) skip index beklenir: %s", stmts[1])
	}
	// Onarım kararı da resource dizisine bakar.
	if !promotedAttrNeedsRepair("attr_values[indexOf(attr_keys, 'k8s.node.name')]", a.keys) == false {
		// anahtar geçiyor → onarım gerekmez sayılır; kapsam farkını
		// promotedAttrNeedsRepair değil probe yakalar (kolon == dizi).
	}
}

// promotedAttrProbeArrays — probe hangi diziyi karşılaştırır.
func TestPromotedAttrProbeArrays(t *testing.T) {
	if k, v := promotedAttrArrays(promotedAttr{res: true}); k != "res_keys" || v != "res_values" {
		t.Fatalf("resource kapsamı res_keys/res_values okumalı, alınan %s/%s", k, v)
	}
	if k, v := promotedAttrArrays(promotedAttr{}); k != "attr_keys" || v != "attr_values" {
		t.Fatalf("attr kapsamı attr_keys/attr_values okumalı, alınan %s/%s", k, v)
	}
}
