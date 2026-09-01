package influx

// enrich_test.go — v0.10.229 (Influx D4) Enricher sözleşmesi:
//   - SORGU 2 → geçersiz id sayılır, exemplar fingerprint poller ile aynı,
//     span özeti + pod sayımı + log imzası + hipotez (anchor problem)
//   - önceki hipotezle birleşim (yeni id önce, ≤50; sayımlar toplanır)
//   - enrich sorgusu yoksa yalnız metrik kanıtı + not
//   - log deposu yoksa ilan; SORGU 2 hatası ilan, hipotez YİNE yazılır
//   - enrichVars: tag adları + op/err kısaltmaları + RFC3339 pencere

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/otlp"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

type fakeEnrichStore struct {
	exemplars []*chstore.ExemplarRow
	exErr     error
	sums      []chstore.TraceSpanSummary
	sumIDs    []string
	prev      *chstore.RootCauseHypothesis
	hyp       *chstore.RootCauseHypothesis
}

func (f *fakeEnrichStore) InsertExemplars(_ context.Context, rows []*chstore.ExemplarRow) error {
	if f.exErr != nil {
		return f.exErr
	}
	f.exemplars = append(f.exemplars, rows...)
	return nil
}
func (f *fakeEnrichStore) SpanSummariesForTraces(_ context.Context, ids []string, _, _ time.Time) ([]chstore.TraceSpanSummary, error) {
	f.sumIDs = ids
	return f.sums, nil
}
func (f *fakeEnrichStore) GetHypothesis(context.Context, string, string) (*chstore.RootCauseHypothesis, error) {
	return f.prev, nil
}
func (f *fakeEnrichStore) UpsertHypothesis(_ context.Context, h chstore.RootCauseHypothesis) error {
	f.hyp = &h
	return nil
}

type fakeLogs struct {
	logs []*logstore.LogRecord
	err  error
	f    logstore.Filter
}

func (l *fakeLogs) Search(_ context.Context, f logstore.Filter) (*logstore.Page, error) {
	l.f = f
	if l.err != nil {
		return nil, l.err
	}
	return &logstore.Page{Total: len(l.logs), Logs: l.logs}, nil
}

const validA = "0af7651916cd43dd8448eb211c80319c"
const validB = "1bf7651916cd43dd8448eb211c80319d"

func enrichQuery() QueryConfig {
	return QueryConfig{
		Name:       "tfail_adet",
		GroupBy:    []string{"OPERATIONCODE", "ERRORCODE"},
		AttrMap:    map[string]string{"OPERATIONCODE": "operation_code"},
		EnrichFlux: `from(bucket:"GGFailTraceBckt") |> range(start: {{from}}, stop: {{to}}) |> filter(fn: (r) => r.OPERATIONCODE == "{{op}}" and r.ERRORCODE == "{{err}}") ENRICH-MARK`,
	}
}

func newTestEnricher(store *fakeEnrichStore, logs logSearcher, api QueryAPI, now time.Time) *Enricher {
	e := &Enricher{store: store, logs: logs, now: func() time.Time { return now }}
	if logs == nil {
		e.logs = nil
	}
	e.queryAPIFor = func(SourceConfig) (QueryAPI, error) { return api, nil }
	return e
}

func baseReq(now time.Time) EnrichRequest {
	return EnrichRequest{
		ProblemID: "p1", Subject: "ext:ggfail/OP1/E1",
		Source: SourceConfig{ID: "i-1", Name: "ggfail"}, Query: enrichQuery(),
		Values: []string{"OP1", "E1"}, Current: 60, Median: 5, MAD: 1, Z: 37,
		From: now.Add(-10 * time.Minute), To: now,
	}
}

func TestEnrich_HappyPath(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	api := &fakeQueryAPI{recs: map[string][]Record{"ENRICH-MARK": recs(
		map[string]string{"_time": "2026-09-02T09:58:00Z", "TRACEID": strings.ToUpper(validA), "INSTANCEID": "pod-a", "FUNCTIONCODE": "F1", "KANALKOD": "MOBIL"},
		map[string]string{"_time": "2026-09-02T09:59:00Z", "TRACEID": validB, "INSTANCEID": "pod-b"},
		map[string]string{"_time": "2026-09-02T09:59:30Z", "TRACEID": validB, "INSTANCEID": "pod-b"},
		map[string]string{"_time": "2026-09-02T09:57:00Z", "TRACEID": "not-a-trace", "INSTANCEID": "pod-c"},
	)}}
	store := &fakeEnrichStore{sums: []chstore.TraceSpanSummary{{TraceID: validB, ErrorSpans: 1}, {TraceID: validA}}}
	logs := &fakeLogs{logs: []*logstore.LogRecord{
		{Body: "order 3f2b1c4e-9d8a-4b7c-8e6f-1a2b3c4d5e6f failed", Severity: 17, SeverityText: "ERROR", TraceID: validA},
		{Body: "order 9a8b7c6d-1e2f-4a3b-8c9d-0e1f2a3b4c5d failed", Severity: 13, SeverityText: "WARN", TraceID: validB},
		{Body: "cache miss", Severity: 13, SeverityText: "WARN", TraceID: validB},
	}}
	e := newTestEnricher(store, logs, api, now)
	rep, err := e.Enrich(context.Background(), baseReq(now))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows != 4 || rep.InvalidIDs != 1 || rep.ValidIDs != 3 || rep.Exemplars != 3 || rep.Traces != 2 || rep.Pods != 2 || rep.LogSignatures != 2 {
		t.Fatalf("report: %+v", rep)
	}
	// SORGU 2 şablonu: op/err + pencere
	if !strings.Contains(api.lastFlux, `r.OPERATIONCODE == "OP1" and r.ERRORCODE == "E1"`) || !strings.Contains(api.lastFlux, "range(start: 2026-09-02T09:50:00Z, stop: 2026-09-02T10:00:00Z)") {
		t.Fatalf("template: %s", api.lastFlux)
	}
	// exemplar: normalize edilmiş küçük-harf id, poller ile AYNI fingerprint
	ex := store.exemplars[0]
	if ex.TraceID != validB || ex.Metric != "ext:tfail_adet" || ex.Service != "ggfail" || ex.Value != 1 {
		t.Fatalf("exemplar newest-first/normalized: %+v", ex)
	}
	wantFP := otlp.SeriesFingerprint("ext:tfail_adet", []*commonpb.KeyValue{strKV("operation_code", "OP1"), strKV("ERRORCODE", "E1")}, "ggfail", "")
	if ex.Fingerprint != wantFP {
		t.Fatalf("fingerprint must match the poller's series (attrMap applied)")
	}
	var withAttrs *chstore.ExemplarRow
	for _, x := range store.exemplars {
		if x.TraceID == validA {
			withAttrs = x
		}
	}
	if withAttrs == nil || withAttrs.FilteredAttrs["k8s.pod.name"] != "pod-a" || withAttrs.FilteredAttrs["FUNCTION_CODE"] != "F1" || withAttrs.FilteredAttrs["CHANNEL_CODE"] != "MOBIL" {
		t.Fatalf("filtered attrs: %+v", withAttrs)
	}
	// span özeti: ayrık id'ler, ±5 dk pad; log araması WARN+ ve id listesi
	if len(store.sumIDs) != 2 || logs.f.SeverityMin != 13 || len(logs.f.TraceIDs) != 2 || logs.f.Limit != 500 {
		t.Fatalf("span/log lookups: ids=%v filter=%+v", store.sumIDs, logs.f)
	}
	h := store.hyp
	if h == nil || h.AnchorKind != "problem" || h.AnchorID != "p1" || h.Service != "ext:ggfail/OP1/E1" || h.Deep == nil {
		t.Fatalf("hypothesis: %+v", h)
	}
	d := h.Deep
	if d.External == nil || d.External.Current != 60 || d.External.Rows != 4 || d.External.InvalidIDs != 1 || d.External.Labels["operation_code"] != "OP1" {
		t.Fatalf("external evidence: %+v", d.External)
	}
	if len(d.TraceIDs) != 2 || d.TraceIDs[0] != validB || d.TraceIDs[1] != validA {
		t.Fatalf("trace ids newest first, unique: %v", d.TraceIDs)
	}
	if len(d.AffectedPods) != 2 || d.AffectedPods[0].Pod != "pod-b" || d.AffectedPods[0].Count != 2 {
		t.Fatalf("pods: %+v", d.AffectedPods)
	}
	if d.LogSignatures[0].Template != "order <x> failed" || d.LogSignatures[0].Count != 2 || d.LogSignatures[0].Severity != "ERROR" || d.LogSignatures[0].TraceCount != 2 {
		t.Fatalf("log signature grouping: %+v", d.LogSignatures[0])
	}
	if d.LogSignatures[0].Sample != "order 3f2b1c4e-9d8a-4b7c-8e6f-1a2b3c4d5e6f failed" {
		t.Fatalf("sample is verbatim: %q", d.LogSignatures[0].Sample)
	}
	if h.Confidence != 1 || h.ExemplarTraceID != validB {
		t.Fatalf("confidence 4/4, exemplar = first error trace: %+v", h)
	}
	if len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "1/4") {
		t.Fatalf("invalid-id honesty note: %v", rep.Notes)
	}
}

func TestEnrich_MergesWithPrevious(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	api := &fakeQueryAPI{recs: map[string][]Record{"ENRICH-MARK": recs(
		map[string]string{"_time": "2026-09-02T09:59:00Z", "TRACEID": validB, "INSTANCEID": "pod-b"},
	)}}
	old := "9999651916cd43dd8448eb211c80319c"
	store := &fakeEnrichStore{prev: &chstore.RootCauseHypothesis{Deep: &chstore.DeepEvidence{
		TraceIDs:     []string{old, validB},
		AffectedPods: []chstore.PodHit{{Pod: "pod-b", Count: 3, LastSeenNs: 1}, {Pod: "pod-z", Count: 1}},
		LogSignatures: []chstore.LogSignature{{Hash: "h1", Template: "t", Count: 4}},
		External: &chstore.ExternalMetricEvidence{SpanSummary: []chstore.TraceSpanSummary{{TraceID: old}}},
	}}}
	e := newTestEnricher(store, nil, api, now)
	if _, err := e.Enrich(context.Background(), baseReq(now)); err != nil {
		t.Fatal(err)
	}
	d := store.hyp.Deep
	if len(d.TraceIDs) != 2 || d.TraceIDs[0] != validB || d.TraceIDs[1] != old {
		t.Fatalf("union new-first: %v", d.TraceIDs)
	}
	if d.AffectedPods[0].Pod != "pod-b" || d.AffectedPods[0].Count != 4 || len(d.AffectedPods) != 2 {
		t.Fatalf("pod counts summed: %+v", d.AffectedPods)
	}
	if len(d.LogSignatures) != 1 || d.LogSignatures[0].Count != 4 {
		t.Fatalf("previous signatures kept when logs unavailable: %+v", d.LogSignatures)
	}
	if len(d.External.SpanSummary) != 1 || d.External.SpanSummary[0].TraceID != old {
		t.Fatalf("empty span summary this round keeps the previous one: %+v", d.External.SpanSummary)
	}
	if !hasNote(store.hyp, "log deposu yok") {
		t.Fatalf("missing logstore must be declared: %v", store.hyp.Deep.External.Notes)
	}
}

func TestEnrich_NoEnrichQueryStillWritesMetricEvidence(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := &fakeEnrichStore{}
	e := newTestEnricher(store, &fakeLogs{}, &fakeQueryAPI{}, now)
	req := baseReq(now)
	req.Query.EnrichFlux = ""
	rep, err := e.Enrich(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows != 0 || store.hyp == nil || store.hyp.Deep.External == nil || store.hyp.Confidence != 0.25 {
		t.Fatalf("metric-only evidence: rep=%+v hyp=%+v", rep, store.hyp)
	}
	if !hasNote(store.hyp, "SORGU 2") {
		t.Fatalf("missing enrich query declared: %v", store.hyp.Deep.External.Notes)
	}
}

func TestEnrich_QueryErrorIsDeclaredNotFatal(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := &fakeEnrichStore{}
	e := newTestEnricher(store, &fakeLogs{}, &fakeQueryAPI{err: errors.New("influx 503")}, now)
	if _, err := e.Enrich(context.Background(), baseReq(now)); err != nil {
		t.Fatalf("query failure must not fail the enrich: %v", err)
	}
	if store.hyp == nil || !hasNote(store.hyp, "influx 503") {
		t.Fatalf("query error declared in notes: %+v", store.hyp)
	}
}

func TestEnrichVars(t *testing.T) {
	from := time.Date(2026, 9, 2, 9, 50, 0, 0, time.UTC)
	to := from.Add(10 * time.Minute)
	v := enrichVars([]string{"KANALKOD", "OPERATIONCODE", "ERRORCODE"}, []string{"MOBIL", "OP1", "E1"}, from, to)
	if v["op"] != "OP1" || v["err"] != "E1" || v["KANALKOD"] != "MOBIL" || v["OPERATIONCODE"] != "OP1" {
		t.Fatalf("vars: %v", v)
	}
	if v["from"] != "2026-09-02T09:50:00Z" || v["to"] != "2026-09-02T10:00:00Z" {
		t.Fatalf("window RFC3339 UTC: %v", v)
	}
	if _, has := enrichVars([]string{"HOST"}, []string{"h1"}, from, to)["op"]; has {
		t.Fatal("no operation-like tag → no op alias")
	}
}

func TestParseEnrichRows_CapAndOrder(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	var rs []map[string]string
	for i := 0; i < 60; i++ {
		id := strings.Repeat("a", 30) + string(rune('0'+i%10)) + string(rune('0'+(i/10)%10))
		rs = append(rs, map[string]string{"_time": now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339), "TRACEID": id})
	}
	rows, invalid := parseEnrichRows(recs(rs...), now)
	if invalid != 0 || len(rows) != enrichMaxRows || !rows[0].Time.Equal(now) {
		t.Fatalf("newest 50: n=%d invalid=%d first=%v", len(rows), invalid, rows[0].Time)
	}
	rows, invalid = parseEnrichRows(recs(map[string]string{"TRACEID": ""}, map[string]string{"foo": "bar"}), now)
	if len(rows) != 0 || invalid != 2 {
		t.Fatalf("missing id counts as invalid: %d/%d", len(rows), invalid)
	}
}

func hasNote(h *chstore.RootCauseHypothesis, s string) bool {
	if h == nil || h.Deep == nil || h.Deep.External == nil {
		return false
	}
	for _, n := range h.Deep.External.Notes {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}
