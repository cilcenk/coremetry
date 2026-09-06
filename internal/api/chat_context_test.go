package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.478 (Faz 4, F4-1) — sunucu sohbet bağlamı saf parçaları. Adlar SENTETİK.

func TestContextPatchFromRoute(t *testing.T) {
	c, changed := contextPatchFromRoute(ChatContext{}, guidedRoute{Intent: guidedServiceHealth, Service: "checkout-service"}, 1800, false)
	if !changed || c.Service != "checkout-service" || c.RangeS != 1800 || c.RangeExplicit || c.LastIntent != "service_health" {
		t.Fatalf("servis rotası: %+v", c)
	}
	c, _ = contextPatchFromRoute(c, guidedRoute{Intent: guidedNamespaceServices, FindQuery: "shop"}, 3600, true)
	if c.Namespace != "shop" || c.Service != "checkout-service" || c.RangeS != 3600 || !c.RangeExplicit {
		t.Fatalf("namespace + açık pencere: %+v", c)
	}
	c, _ = contextPatchFromRoute(c, guidedRoute{Intent: guidedTraceSearch, Service: "checkout-service", SearchText: "gw.example.com", SearchKeys: []string{"server.address"}}, 3600, false)
	if c.SearchText != "gw.example.com" || len(c.Filters) != 1 || c.Filters[0].Key != "server.address" || c.ErrorsOnly {
		t.Fatalf("arama yaması: %+v", c)
	}
	c, _ = contextPatchFromRoute(c, guidedRoute{Intent: guidedFamilyTraces, Family: []string{"a", "b"}, TraceErrorsOnly: true}, 3600, false)
	if !c.ErrorsOnly || c.Service != "checkout-service" {
		t.Fatalf("aile hatalı: %+v", c)
	}
	if _, changed := contextPatchFromRoute(c, guidedRoute{Intent: guidedFamilyTraces, Family: []string{"a", "b"}, TraceErrorsOnly: true}, 3600, false); changed {
		t.Error("aynı yama değişiklik saymamalı")
	}
}

func TestApplyAndClearContextPatch(t *testing.T) {
	c, err := applyContextPatch(ChatContext{}, map[string]any{"service": " checkout ", "range_s": float64(3600), "errors_only": true, "filters": []any{map[string]any{"k": "http.route", "op": "=", "v": []any{"/pay"}}}})
	if err != nil || c.Service != "checkout" || c.RangeS != 3600 || !c.RangeExplicit || !c.ErrorsOnly || len(c.Filters) != 1 {
		t.Fatalf("yama: %+v %v", c, err)
	}
	for _, bad := range []map[string]any{{"foo": "x"}, {"range_s": float64(5)}, {"errors_only": "yes"}, {"filters": []any{map[string]any{"k": "x", "op": "fuzzy", "v": []any{"y"}}}}} {
		if _, err := applyContextPatch(c, bad); err == nil {
			t.Errorf("%v hata beklenir", bad)
		}
	}
	c2, err := clearContextFields(c, []string{"service", "range_s"})
	if err != nil || c2.Service != "" || c2.RangeS != 0 || c2.RangeExplicit || !c2.ErrorsOnly {
		t.Fatalf("kısmi silme: %+v %v", c2, err)
	}
	if all, _ := clearContextFields(c, nil); !all.Empty() {
		t.Error("alan verilmezse hepsi silinir")
	}
	if _, err := clearContextFields(c, []string{"nope"}); err == nil {
		t.Error("bilinmeyen alan hata")
	}
}

func TestContextPreambleChipKey(t *testing.T) {
	if chatContextPreambleTR(ChatContext{}) != "" || chatContextChipTR(ChatContext{}) != "" {
		t.Fatal("boş bağlam boş dize")
	}
	c := ChatContext{Namespace: "shop", Service: "checkout", RangeS: 3600, ErrorsOnly: true, SearchText: "gw", Filters: []chstore.FilterExpr{{Key: "server.address", Op: "=", Values: []string{"gw"}}}}
	p := chatContextPreambleTR(c)
	for _, want := range []string{"AKTİF SOHBET BAĞLAMI", "namespace: shop", "servis: checkout", "range_s=3600", "yalnız hatalı", "server.address", "set_context"} {
		if !strings.Contains(p, want) {
			t.Errorf("önsöz %q yok", want)
		}
	}
	if chip := chatContextChipTR(c); !strings.Contains(chip, "shop · checkout") || !strings.Contains(chip, "yalnız hatalı") {
		t.Errorf("çip: %s", chip)
	}
	if chatContextKey("u1", "") != "copilot:ctx:u1:_" || chatContextKey("u1", "c9") != "copilot:ctx:u1:c9" {
		t.Error("anahtar")
	}
	v := contextToolView(c)
	if v["namespace"] != "shop" || v["errors_only"] != true || v["range_s"] != int64(3600) {
		t.Errorf("tool görünümü: %v", v)
	}
}
