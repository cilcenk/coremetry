package chstore

import (
	"strings"
	"testing"
)

// v0.10.472 (Faz 3, F3-1) — kapsamlı attribute keşfi + değer probu şekilleri.

func TestScopedAttrsSQL(t *testing.T) {
	if _, err := scopedAttrsSQL("attr_keys", "attr_values", AttrScope{}); err == nil {
		t.Fatal("boş kapsam reddedilmeli (filo geneli dizi taraması yok)")
	}
	sql, err := scopedAttrsSQL("res_keys", "res_values", AttrScope{Namespace: "shop", Clusters: []string{"prod-eu-west"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"k8s_namespace = ?", "cluster IN (?)", "res_keys[idx]", "LIMIT ?", "max_execution_time = 10", "time >= ? AND time <= ?"} {
		if !strings.Contains(sql, want) {
			t.Errorf("%q yok", want)
		}
	}
}

func TestAttrKeyColumn(t *testing.T) {
	cases := map[string]string{"http.route": "http_route", "span.http.route": "http_route", "k8s.namespace.name": "k8s_namespace", "resource.k8s.pod.name": "k8s_pod", "service.name": "service_name", "peer.service": "peer_service"}
	for k, want := range cases {
		if col, _, ok := AttrKeyColumn(k); !ok || col != want {
			t.Errorf("%s → %q/%v, want %s", k, col, ok, want)
		}
	}
	for _, k := range []string{"cluster", "duration_ms", "server.address", "url.full", "http.host"} {
		if col, _, ok := AttrKeyColumn(k); ok {
			t.Errorf("%s türetilmiş/dizi anahtarı kolon sayılmamalı: %q", k, col)
		}
	}
}

func TestAttrValueProbeSQL(t *testing.T) {
	scope := AttrScope{Service: "checkout"}
	sql, args, basis, ok := attrValueProbeSQL("http.route", "/pay", scope, false)
	if !ok || basis != ProbeColumn || !strings.Contains(sql, "http_route = ?") || len(args) != 2 {
		t.Fatalf("kolon probu: %v %s %v", ok, sql, args)
	}
	if _, _, basis, ok := attrValueProbeSQL("server.address", "gw.example.com", scope, false); ok || basis != ProbeNone {
		t.Fatal("indeks yokken dizi probu olmamalı (örneklem kanıtı kalır)")
	}
	sql, args, basis, ok = attrValueProbeSQL("server.address", "gw.example.com", scope, true)
	if !ok || basis != ProbeKVH || !strings.Contains(sql, "has(attr_kvh, cityHash64(concat(?") || !strings.Contains(sql, "attr_values[indexOf(attr_keys, ?)] = ?") || len(args) != 5 {
		t.Fatalf("kvh probu: %v %s %v", ok, sql, args)
	}
	sql, _, _, _ = attrValueProbeSQL("resource.k8s.cluster.name", "x", scope, true)
	if !strings.Contains(sql, "res_kvh") || !strings.Contains(sql, "res_values[indexOf(res_keys, ?)]") {
		t.Errorf("resource dizisi: %s", sql)
	}
	if _, _, _, ok := attrValueProbeSQL("http.route", "/pay", AttrScope{}, true); ok {
		t.Error("kapsamsız prob yok")
	}
}
