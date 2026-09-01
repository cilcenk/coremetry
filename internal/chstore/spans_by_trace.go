package chstore

// spans_by_trace.go — verilen trace id listesi için span özetleri
// (v0.10.229, Influx D4 audit §4 adım 3).
//
// CH'de trace ARANMAZ: liste Influx'tan (SORGU 2) gelir, burada yalnız
// `trace_id IN (...)` ile okunur — bloom `idx_trace` + zaman sınırı +
// LIMIT + max_execution_time (CLAUDE.md CH bounds). Katlama SAF
// (foldTraceSummaries) ve tablo-testli.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	spansByTraceMaxIDs  = 50
	spansByTraceMaxRows = 5000
)

// TraceSpanSummary — trace başına tek satır kanıt.
type TraceSpanSummary struct {
	TraceID        string `json:"traceId"`
	StartNs        int64  `json:"startNs"`
	DurationNs     int64  `json:"durationNs"`
	Spans          int    `json:"spans"`
	ErrorSpans     int    `json:"errorSpans"`
	RootService    string `json:"rootService,omitempty"`
	RootOp         string `json:"rootOp,omitempty"`
	ErrorService   string `json:"errorService,omitempty"`
	ErrorOp        string `json:"errorOp,omitempty"`
	SlowestService string `json:"slowestService,omitempty"`
	SlowestOp      string `json:"slowestOp,omitempty"`
}

type traceSpanRow struct {
	TraceID, SpanID, ParentID, Service, Name, Status string
	Time                                             time.Time
	DurationNs                                       int64
}

// SpanSummariesForTraces — ids ≤50 (fazlası kırpılır), pencere
// [from, to]; çağıran ±5 dk pad'ler. Sonuç en yeni trace önce.
func (s *Store) SpanSummariesForTraces(ctx context.Context, ids []string, from, to time.Time) ([]TraceSpanSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > spansByTraceMaxIDs {
		ids = ids[:spansByTraceMaxIDs]
	}
	holders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, from, to)
	for i, id := range ids {
		holders[i] = "?"
		args = append(args, id)
	}
	rows, err := s.telemetryReadConn().Query(ctx, fmt.Sprintf(`
		SELECT trace_id, span_id, parent_id, service_name, name, status_code, time, duration
		FROM spans
		WHERE time >= ? AND time <= ?
		  AND trace_id IN (%s)
		ORDER BY trace_id, time
		LIMIT %d
		SETTINGS max_execution_time = 5`, strings.Join(holders, ","), spansByTraceMaxRows), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []traceSpanRow
	for rows.Next() {
		var r traceSpanRow
		if err := rows.Scan(&r.TraceID, &r.SpanID, &r.ParentID, &r.Service, &r.Name, &r.Status, &r.Time, &r.DurationNs); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return foldTraceSummaries(all), nil
}

// foldTraceSummaries — SAF: kök = parent'ı boş ya da trace içinde
// bulunmayan ilk span (zaman sırası); hata = ilk status 'error' span;
// en yavaş = en uzun süreli span; süre = kök varsa kökün süresi, yoksa
// min(time)…max(time+duration). En yeni trace önce.
func foldTraceSummaries(rows []traceSpanRow) []TraceSpanSummary {
	if len(rows) == 0 {
		return nil
	}
	byTrace := map[string][]traceSpanRow{}
	order := []string{}
	for _, r := range rows {
		if _, seen := byTrace[r.TraceID]; !seen {
			order = append(order, r.TraceID)
		}
		byTrace[r.TraceID] = append(byTrace[r.TraceID], r)
	}
	out := make([]TraceSpanSummary, 0, len(order))
	for _, id := range order {
		sp := byTrace[id]
		sort.SliceStable(sp, func(i, j int) bool { return sp[i].Time.Before(sp[j].Time) })
		ids := make(map[string]bool, len(sp))
		for _, r := range sp {
			ids[r.SpanID] = true
		}
		sum := TraceSpanSummary{TraceID: id, Spans: len(sp), StartNs: sp[0].Time.UnixNano()}
		var endNs int64
		var slowest int64 = -1
		for _, r := range sp {
			if e := r.Time.UnixNano() + r.DurationNs; e > endNs {
				endNs = e
			}
			if sum.RootService == "" && (r.ParentID == "" || !ids[r.ParentID]) {
				sum.RootService, sum.RootOp = r.Service, r.Name
				sum.DurationNs = r.DurationNs
			}
			if r.Status == "error" {
				sum.ErrorSpans++
				if sum.ErrorService == "" {
					sum.ErrorService, sum.ErrorOp = r.Service, r.Name
				}
			}
			if r.DurationNs > slowest {
				slowest = r.DurationNs
				sum.SlowestService, sum.SlowestOp = r.Service, r.Name
			}
		}
		if sum.DurationNs == 0 {
			sum.DurationNs = endNs - sum.StartNs
		}
		out = append(out, sum)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartNs > out[j].StartNs })
	return out
}
