package chstore

// spanmetric_filter_root_test.go — v0.10.655 (operatör, prod: "filtreli
// sorguda 34 trace çıkıyor ama histogram milyon gösteriyor").
//
// /traces üst histogramı /api/spans/metric-batch'e biner; bu yüzey gruplu
// (OR / iç içe) filtreyi bilmiyordu, sayfa da gruplu kipte düz filtreleri
// bilerek göndermiyordu → grafik servisin tüm evrenini, tablo grubu
// sayıyordu. Batch filtresi artık tek-agg yolundaki gibi FilterRoot taşır.
//
// SÖZLEŞME:
//   - FilterRoot varsa WHERE'e grup yüklemi girer (OR / iç içe dahil) ve
//     düz Filters ile AND'lenir (env/cluster/kind bağlam çipleri düz gelir).
//   - FilterRoot varsa dar rollup fast-path'i DEVRE DIŞI (grup MV
//     boyutlarına oturmaz; ham yol) — aksi hâlde grup sessizce yutulurdu.
// Mutasyon (ölçüldü): where'de ApplyFilterGroup çağrısını kaldırmak 1. testi,
// narrowRollupEligible'daki FilterRoot muhafızını kaldırmak 3. testi düşürür.

import (
	"strings"
	"testing"
	"time"
)

func rootGroupFilter() SpanMetricBatchFilter {
	return SpanMetricBatchFilter{
		From: time.Unix(1_700_000_000, 0), To: time.Unix(1_700_003_600, 0),
		Filters: []FilterExpr{{Key: "deployment.environment", Op: "=", Values: []string{"prod"}}},
		FilterRoot: &FilterGroup{Join: "AND",
			Filters: []FilterExpr{{Key: "db.system", Op: "=", Values: []string{"oracle"}}},
			Groups: []FilterGroup{{Join: "OR", Filters: []FilterExpr{
				{Key: "peer.service", Op: "=", Values: []string{"ora-1"}},
				{Key: "server.address", Op: "=", Values: []string{"ora-1"}},
			}}},
		},
		Aggs: []SpanMetricAggSpec{{Name: "count", Aggregation: "count"}},
	}
}

func TestSpanMetricBatchWhereAppliesFilterRoot(t *testing.T) {
	wc := spanMetricBatchWhere(rootGroupFilter(), 0, 0)
	sql := wc.sql()
	if !strings.Contains(sql, " OR ") {
		t.Fatalf("OR grubu WHERE'e girmedi:\n%s", sql)
	}
	// Terfi kolonları (db_system, peer_service) ve dizi yolu (server.address
	// terfi etmemiş → attr_values[indexOf(attr_keys, ?)], anahtar bind arg).
	for _, want := range []string{"db_system = ?", "peer_service = ?", "attr_values[indexOf(attr_keys, ?)] = ?"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("grup yaprağı %q WHERE'de yok:\n%s", want, sql)
		}
	}
	// Düz bağlam çipi (env) grupla AND'lenir, kaybolmaz.
	if !strings.Contains(sql, "deploy_env = ?") {
		t.Fatalf("düz env çipi grupla birlikte uygulanmalı:\n%s", sql)
	}
}

func TestSpanMetricBatchWhereNilRootUnchanged(t *testing.T) {
	f := rootGroupFilter()
	f.FilterRoot = nil
	wc := spanMetricBatchWhere(f, 0, 0)
	sql := wc.sql()
	if strings.Contains(sql, " OR ") || strings.Contains(sql, "peer_service") {
		t.Fatalf("root yokken grup yüklemi girmemeli:\n%s", sql)
	}
}

func TestNarrowRollupIneligibleWithFilterRoot(t *testing.T) {
	f := SpanMetricBatchFilter{
		From: time.Unix(1_700_000_000, 0), To: time.Unix(1_700_003_600, 0),
		Filters: []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"shop-payment"}}},
		Aggs:    []SpanMetricAggSpec{{Name: "count", Aggregation: "count"}},
	}
	if _, ok := narrowRollupEligible(f); !ok {
		t.Fatal("kontrol: yalnız service.name = X dar rollup'a uygun olmalı")
	}
	f.FilterRoot = &FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "db.system", Op: "=", Values: []string{"oracle"}},
		{Key: "db.system", Op: "=", Values: []string{"postgresql"}},
	}}
	if _, ok := narrowRollupEligible(f); ok {
		t.Fatal("FilterRoot varken dar rollup fast-path'i kapalı olmalı — grup MV'de yutulurdu")
	}
}
