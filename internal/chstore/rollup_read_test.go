// rollup_read_test.go — v0.9.385 (rollup Aşama 2). rollupREDSQL'in
// disiplin pinleri: bound WHERE + LIMIT + max_execution_time (CLAUDE.md
// sorgu kuralının rollup karşılığı), whitelist dışı groupBy reddi, iki
// quantile modunun şekli, GroupBy'da bind-arg ikilemesi (iç top-N alt
// sorgusu aynı filtreleri SQL sırasında yeniden bağlar).
package chstore

import (
	"strings"
	"testing"
	"time"
)

func rtestPlan(mode string) RollupPlan {
	t := "rollup_spans_narrow_1m"
	if mode == "buckets" {
		t = "rollup_spans_wide_1m"
	}
	return RollupPlan{Table: t, StepSeconds: 60, QuantileMode: mode}
}

func TestRollupREDSQL(t *testing.T) {
	from := time.Unix(1000, 0)
	to := time.Unix(2000, 0)

	t.Run("tdigest disiplin + filtre bind'ları", func(t *testing.T) {
		sql, args, err := rollupREDSQL(rtestPlan("tdigest"),
			RollupSeriesFilter{Service: "payment", Kind: "server"}, from, to)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"ts >= ?", "ts < ?", "service_name = ?", "span_kind = ?",
			"quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state)",
			"LIMIT 250000", "max_execution_time = 15",
			"INTERVAL 60 SECOND",
			// v0.9.386 — CH sürücüsü Scan'de katı: toUnixTimestamp UInt32
			// döner, hedef int64 → "converting UInt32 to *int64 is
			// unsupported". toInt64 sarmalayıcısı sözleşmenin parçası.
			"toInt64(toUnixTimestamp(",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("SQL %q içermeli:\n%s", want, sql)
			}
		}
		if len(args) != 4 { // from, to, service, kind
			t.Errorf("args = %d, want 4", len(args))
		}
	})

	t.Run("buckets modu katmanlı ve sumForEachMerge'li", func(t *testing.T) {
		sql, _, err := rollupREDSQL(rtestPlan("buckets"),
			RollupSeriesFilter{Endpoint: "/api/transfer"}, from, to)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"sumForEachMerge(lat_buckets)", "arrayCumSum", "exp2(",
			"endpoint = ?", "[0.5, 0.95, 0.99]",
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("SQL %q içermeli", want)
			}
		}
		if strings.Contains(sql, "q_state") {
			t.Errorf("buckets modunda q_state olmamalı (geniş tabloda kolon yok)")
		}
	})

	t.Run("groupBy bind ikilemesi + top-N alt sorgusu", func(t *testing.T) {
		sql, args, err := rollupREDSQL(rtestPlan("tdigest"),
			RollupSeriesFilter{Service: "payment", GroupBy: "status_code", MaxGroups: 5}, from, to)
		if err != nil {
			t.Fatal(err)
		}
		// dış WHERE 3 bind (from,to,service) + iç alt sorgu aynı 3'ü tekrar = 6
		if len(args) != 6 {
			t.Errorf("args = %d, want 6 (iç alt sorgu bind ikilemesi)", len(args))
		}
		if !strings.Contains(sql, "ORDER BY sum(span_count) DESC LIMIT 6") {
			t.Errorf("top-N alt sorgusu maxGroups+1=6 LIMIT'li olmalı:\n%s", sql)
		}
	})

	t.Run("whitelist dışı groupBy reddedilir", func(t *testing.T) {
		if _, _, err := rollupREDSQL(rtestPlan("tdigest"),
			RollupSeriesFilter{GroupBy: "trace_id; DROP TABLE spans--"}, from, to); err == nil {
			t.Fatal("hostil groupBy hata vermeli")
		}
	})
}
