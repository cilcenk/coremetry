package chstore

// trace_explain.go — v0.10.326: /api/traces?explain=1 (admin) teşhis kaydı.
// Operatör 2026-09-03: aynı servis+operasyon araması prod'da 15m/30m boş,
// 6h dolu; lokalde tekrar etmiyor, prod query_log'a erişim yok. Bu sınıf
// bir daha tahminle çözülmesin: liste isteği hangi yolu seçti (mv /
// error-first / probe / light / raw-list), hangi SQL hangi arg'larla
// koştu, kaç ms sürdü, kaç satır döndü, hata neydi — yanıtın içinde.
// nil-güvenli: Explain verilmediğinde sıfır maliyet (nil alıcı, erken dönüş).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type TraceExplainStep struct {
	Name string   `json:"name"`
	SQL  string   `json:"sql,omitempty"`
	Args []string `json:"args,omitempty"`
	Ms   float64  `json:"ms"`
	Rows int      `json:"rows"`
	Err  string   `json:"err,omitempty"`
}

type TraceExplain struct {
	Notes []string           `json:"notes"`
	Steps []TraceExplainStep `json:"steps"`
}

// note — yol kararı / bağlam satırı ("path=raw-list", "window=…").
func (x *TraceExplain) note(format string, a ...any) {
	if x == nil {
		return
	}
	x.Notes = append(x.Notes, fmt.Sprintf(format, a...))
}

// step — bir CH sorgusunun kaydı; SQL boşlukları tek boşluğa iner, arg'lar
// metne çevrilir (time.Time UTC RFC3339Nano — pencere sınırı okunsun).
func (x *TraceExplain) step(name, sql string, args []any, start time.Time, rows int, err error) {
	if x == nil {
		return
	}
	st := TraceExplainStep{Name: name, SQL: strings.Join(strings.Fields(sql), " "), Ms: float64(time.Since(start).Microseconds()) / 1000, Rows: rows}
	for _, a := range args {
		switch v := a.(type) {
		case time.Time:
			st.Args = append(st.Args, v.UTC().Format(time.RFC3339Nano))
		default:
			s := fmt.Sprint(v)
			if len(s) > 120 {
				s = s[:120] + "…"
			}
			st.Args = append(st.Args, s)
		}
	}
	if err != nil {
		st.Err = err.Error()
	}
	x.Steps = append(x.Steps, st)
}

// ── v0.10.329 — boş liste öz-teşhisi ─────────────────────────────────────
// Operatör (prod): filtreli/aramalı liste kısa pencerede boş, şerit dolu.
// Kanıt toplamayı ürüne gömüyoruz: liste boş dönerse aynı WHERE + arama
// yüklemiyle SPAN düzeyinde sayım yapılır. N > 0 ise veri var, liste
// sorgusu (GROUP BY/HAVING) onları kaybediyor; N = 0 ise veri/yüklem.

// emptyDiagWanted — sayım yalnız boş sonuçta ve bir daraltma varken
// (arama / filtre / hata): filtresiz boş liste zaten "pencerede iz yok"tur.
func emptyDiagWanted(f TraceFilter, rows int) bool {
	if rows != 0 || f.TraceID != "" || len(f.TraceIDs) > 0 {
		return false
	}
	return f.Search != "" || len(f.Filters) > 0 || (f.FilterRoot != nil && f.FilterRoot.hasPredicate()) || f.HasError
}

func EmptyDiagWanted(f TraceFilter, rows int) bool { return emptyDiagWanted(f, rows) }

// countMatchingSpansSQL — saf: liste WHERE'i + arama yüklemi span düzeyinde.
func countMatchingSpansSQL(whereSQL string) string {
	return `SELECT count() FROM spans ` + whereSQL + ` SETTINGS max_execution_time = 10`
}

// CountMatchingSpans — bkz. üst yorum. Hata teşhisin parçası: hata dönerse
// çağıran onu da kaydeder.
func (s *Store) CountMatchingSpans(ctx context.Context, f TraceFilter) (uint64, error) {
	lf := f
	lf.CandidateIDs = nil
	wc := buildGetTracesWhere(lf, s.clusterExpr())
	if pred, pargs := searchPredicate(f.Search); pred != "" {
		wc.add(pred, pargs...)
	}
	if f.HasError && !hasErrorSpanLocal(f) {
		wc.add("status_code = 'error'")
	}
	t0 := time.Now()
	sql := countMatchingSpansSQL(wc.sql())
	var n uint64
	err := s.telemetryReadConn().QueryRow(ctx, sql, wc.args...).Scan(&n)
	f.Explain.step("empty-diag-count", sql, wc.args, t0, int(n), err)
	return n, err
}

// v0.10.339 — terfi kolonu uyuşmazlık probu (promoted_attr.go §v0.10.339).
// Burada, çünkü telemetri SELECT'i okuma havuzundan gider ve o çağrı yüzeyi
// dosya bazında kapılı (conn_strategy_test.go); CountMatchingSpans ile aynı
// dosya, aynı havuz.
// PromotedMismatch — kolon yolu vs dizi yolu, host başına.
func (s *Store) PromotedMismatch(ctx context.Context, f TraceFilter) ([]PromotedHostCount, error) {
	byHost := map[string]*PromotedHostCount{}
	run := func(noPromoted bool, name string, set func(c *PromotedHostCount, n uint64)) error {
		lf := f
		lf.CandidateIDs = nil
		lf.NoPromoted = noPromoted
		wc := buildGetTracesWhere(lf, s.clusterExpr())
		if pred, pargs := searchPredicate(f.Search); pred != "" {
			wc.add(pred, pargs...)
		}
		if f.HasError && !hasErrorSpanLocal(f) {
			wc.add("status_code = 'error'")
		}
		sql := promotedMismatchSQL(wc.sql())
		t0 := time.Now()
		rows, err := s.telemetryReadConn().Query(ctx, sql, wc.args...)
		if err != nil {
			f.Explain.step(name, sql, wc.args, t0, 0, err)
			return err
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var h string
			var c uint64
			if err := rows.Scan(&h, &c); err != nil {
				return err
			}
			hc := byHost[h]
			if hc == nil {
				hc = &PromotedHostCount{Host: h}
				byHost[h] = hc
			}
			set(hc, c)
			n++
		}
		f.Explain.step(name, sql, wc.args, t0, n, rows.Err())
		return rows.Err()
	}
	if err := run(false, "promoted-col-count", func(c *PromotedHostCount, n uint64) { c.Col = n }); err != nil {
		return nil, err
	}
	if err := run(true, "promoted-arr-count", func(c *PromotedHostCount, n uint64) { c.Arr = n }); err != nil {
		return nil, err
	}
	out := make([]PromotedHostCount, 0, len(byHost))
	for _, hc := range byHost {
		out = append(out, *hc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}
