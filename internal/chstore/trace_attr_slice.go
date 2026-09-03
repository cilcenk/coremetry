package chstore

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
)

// trace_attr_slice.go — v0.10.301 (docs/audit/trace-attribute-search.md
// Dilim 1c, G2): attribute filtresi olan aramada ADAY KÜMESİ indeksten
// gelir.
//
// Öncesinde aday kümesi attribute'tan HABERSİZ seçiliyordu: servis yoksa en
// yeni 5000 trace (recency dilimi), servis varsa PK dilimi; attribute
// filtreleri yalnız aşama 2'de o dilimin içinde uygulanıyordu. Pencerenin
// başındaki nadir bir değer BULUNAMIYORDU (UI "ranked within newest N"
// diyordu — dürüst ama yetersiz). Şimdi: filtrelerde bloom'un kullanabildiği
// en az bir yüklem varsa (dizi yolunda `=`/`IN` → attr_kvh/res_kvh;
// `EXISTS` → attr_keys/res_keys bloom'u) aşama 1 doğrudan `spans`
// üzerinden koşar — indeks granül budar, zaman sırasıyla `want` trace_id
// alınır (scanIDSlice: tekilleştirme + kesim), aşama 2 yalnız o id'lerle.
//
// Doğruluk: aday kümesi ARTIK yüklemle süzülmüş → boş sonuç "bulunamadı"
// demektir, recency dilimine DÜŞÜLMEZ. Yaygın değerde (bloom budamaz)
// tarama pencere boyunca sürer ama LIMIT + max_execution_time bütçelidir;
// kesilirse RankedWithin sayacı (yalnız sıralı sort'ta) bunu ilan eder.
// Kapı: AttrIndexAvailable() — kolon yoksa eski akış aynen.

var attrSliceUsed atomic.Uint64

// AttrSliceUsed — /api/health `attr_slice_used`.
func AttrSliceUsed() uint64 { return attrSliceUsed.Load() }

// flatAndLeaves — grup ağacı tamamen AND ise yapraklar; OR varsa (ok=false)
// indeks dilimi kurulmaz (OR'un bir kolu indekssiz olabilir → yanlış küme).
func flatAndLeaves(g *FilterGroup) ([]FilterExpr, bool) {
	if g == nil {
		return nil, true
	}
	if joinOp(g.Join) != "AND" {
		return nil, false
	}
	out := append([]FilterExpr(nil), g.Filters...)
	for i := range g.Groups {
		sub, ok := flatAndLeaves(&g.Groups[i])
		if !ok {
			return nil, false
		}
		out = append(out, sub...)
	}
	return out, true
}

// attrSlicePredicates — bloom-kullanabilir yüklemlerin AND'i. Diğer
// yaprakları (negasyon, regex, aralık, terfi kolonu) burada UYGULAMAZ —
// onlar aşama 2'de tam listeyle koşar; aday kümesi yalnız daralır,
// genişlemez (üst küme → doğru).
func attrSlicePredicates(f TraceFilter) (sql string, args []any, ok bool) {
	if !AttrIndexAvailable() {
		return "", nil, false
	}
	leaves := f.Filters
	if f.FilterRoot != nil {
		l, flat := flatAndLeaves(f.FilterRoot)
		if !flat {
			return "", nil, false
		}
		leaves = l
	}
	var parts []string
	for _, leaf := range leaves {
		s, a, err := leaf.SQL()
		if err != nil || s == "" {
			continue
		}
		op := strings.ToUpper(strings.TrimSpace(leaf.Op))
		indexed := strings.Contains(s, "_kvh, ") ||
			(op == "EXISTS" && (strings.HasPrefix(s, "has(attr_keys, ?)") || strings.HasPrefix(s, "has(res_keys, ?)")))
		if !indexed {
			continue
		}
		parts = append(parts, s)
		args = append(args, a...)
	}
	if len(parts) == 0 {
		return "", nil, false
	}
	return strings.Join(parts, " AND "), args, true
}

// traceAttrSliceSQL — aşama 1: spans üzerinden, indeksli yüklemlerle,
// zaman sıralı trace_id + 5-dk kova (scanIDSlice sözleşmesi). Servis
// varsa PK öneki de kullanılır. Zaman sınırı + LIMIT + max_execution_time
// (CLAUDE.md sert kısıt).
func (s *Store) traceAttrSliceSQL(f TraceFilter, preds string) string {
	dir := "DESC"
	if f.Order == "asc" {
		dir = "ASC"
	}
	where := "time >= ? AND time < ?"
	if f.Service != "" {
		where += " AND service_name = ?"
	}
	if f.Env != "" {
		where += " AND deploy_env = ?"
	}
	return `
		SELECT trace_id, toStartOfFiveMinute(time) AS time_bucket
		FROM spans
		WHERE ` + where + ` AND ` + preds + `
		ORDER BY time ` + dir + `
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         ` + s.shardSkipSetting()
}

// traceAttrSlice — scanIDSlice ile: tekil trace_id'ler, kesim kovası,
// tükenme (pencere bitti = tüm eşleşenler alındı).
func (s *Store) traceAttrSlice(
	ctx context.Context, f TraceFilter, want int, preds string, predArgs []any,
) (ids []any, cut time.Time, exhausted bool, err error) {
	if want <= 0 || preds == "" {
		return nil, time.Time{}, false, nil
	}
	budget := want * traceSliceOverprovision
	if budget > traceSliceMaxRows {
		budget = traceSliceMaxRows
	}
	lead := []any{f.From, f.To}
	if f.Service != "" {
		lead = append(lead, f.Service)
	}
	if f.Env != "" {
		lead = append(lead, f.Env)
	}
	lead = append(lead, predArgs...)
	attrSliceUsed.Add(1)
	return s.scanIDSlice(ctx, s.traceAttrSliceSQL(f, preds), lead, f.From, want, budget)
}
