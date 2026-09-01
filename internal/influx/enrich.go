package influx

// enrich.go — açık dış problem başına kanıt toplama (v0.10.229, audit
// docs/audit/influx-integration.md §4 + dilim D4).
//
// Akış (anomaly.ExternalScanner'ın OnEvidence kancasından, açılışta hemen
// + sürerken 5 dk'da bir):
//   1. SORGU 2 (QueryConfig.EnrichFlux; {{from}}/{{to}}/{{op}}/{{err}} +
//      her groupBy tag'ı adıyla) → ≤50 en yeni satır (TRACEID, INSTANCEID,
//      FUNCTIONCODE, KANALKOD). Trace id 32 küçük-harf hex doğrulanır;
//      geçmeyen düşer ve SAYILIR ("3/50 id geçersiz" — audit R12).
//   2. exemplars ← satır başına ExemplarRow (fingerprint = poller'ın
//      yazdığı seriyle AYNI: otlp.SeriesFingerprint) → Explore'da
//      seri→trace pivotu bedava.
//   3. CH span özetleri — trace_id IN (...) (chstore.SpanSummariesForTraces);
//      CH'de trace ARANMAZ, liste Influx'tan gelir.
//   4. ES/CH log imzaları — logstore.Search(TraceIDs, WARN+) →
//      NormalizeSignature grubu, ilk 15; örnek mesaj VERBATIM.
//   5. Pod'lar — INSTANCEID sayımı (Problem.Pod tek string; liste kanıta).
//   6. UpsertHypothesis (anchor_kind=problem) — önceki satırla BİRLEŞİM:
//      trace listesi (yeni önce, ≤50), pod/imza sayımları toplanır.
// Her adım fail-open: hata Notes'a yazılır, kalan kanıt yine kaydedilir.
// Sessiz kayıp yok (feedback: "bilinmeyen alan sessizce düşmez").

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/otlp"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

const (
	enrichMaxRows       = 50
	enrichMaxSignatures = 15
	enrichLogLimit      = 500
	enrichLogSeverity   = 13 // OTel WARN
	enrichPad           = 5 * time.Minute
	enrichTimeout       = 20 * time.Second
)

var traceIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// EnrichRequest — anomaly.ExternalEvent'in influx tarafı (main.go çevirir).
type EnrichRequest struct {
	ProblemID string
	Subject   string // Problem.Service (`ext:<kaynak>/<grup>`) → hipotez service alanı
	Source    SourceConfig
	Query     QueryConfig
	Values    []string // grup değerleri, Query.GroupBy sırasıyla
	Current   float64
	Median    float64
	MAD       float64
	Z         float64
	From, To  time.Time
}

// EnrichReport — log + test sayımı.
type EnrichReport struct {
	Rows, ValidIDs, InvalidIDs, Exemplars, Traces, Pods, LogSignatures int
	Notes                                                              []string
}

type enrichStore interface {
	InsertExemplars(ctx context.Context, rows []*chstore.ExemplarRow) error
	SpanSummariesForTraces(ctx context.Context, ids []string, from, to time.Time) ([]chstore.TraceSpanSummary, error)
	GetHypothesis(ctx context.Context, anchorKind, anchorID string) (*chstore.RootCauseHypothesis, error)
	UpsertHypothesis(ctx context.Context, h chstore.RootCauseHypothesis) error
}

// logSearcher — logstore.Store'un burada kullanılan tek metodu (test
// fake'i küçük kalsın; feedback: mock ormanı = yanlış seam).
type logSearcher interface {
	Search(ctx context.Context, f logstore.Filter) (*logstore.Page, error)
}

// Enricher — kaynak istemcisi + CH + log deposu.
type Enricher struct {
	store       enrichStore
	logs        logSearcher // nil → log kanıtı yok, ilan edilir
	queryAPIFor func(SourceConfig) (QueryAPI, error)
	now         func() time.Time
}

func NewEnricher(svc *Service, store enrichStore, logs logSearcher) *Enricher {
	return &Enricher{store: store, logs: logs, queryAPIFor: svc.QueryAPIFor, now: time.Now}
}

type enrichRow struct {
	TraceID, Pod, FunctionCode, ChannelCode string
	Time                                    time.Time
}

// Enrich — bkz. dosya başlığı. Yalnız hipotez yazımı hata döndürür.
func (e *Enricher) Enrich(ctx context.Context, req EnrichRequest) (EnrichReport, error) {
	var rep EnrichReport
	if req.ProblemID == "" || req.Source.Name == "" || req.Query.Name == "" {
		return rep, fmt.Errorf("enrich: problem id, source name and query name required")
	}
	ctx, cancel := context.WithTimeout(ctx, enrichTimeout)
	defer cancel()
	now := e.now()
	metric := MetricPrefix + req.Query.Name
	attrLabels := req.Query.attrLabels(req.Values)
	ext := &chstore.ExternalMetricEvidence{
		Source: req.Source.Name, Query: req.Query.Name, Labels: attrLabels,
		Current: req.Current, Median: req.Median, MAD: req.MAD, Z: req.Z,
		WindowFromNs: req.From.UnixNano(), WindowToNs: req.To.UnixNano(), UpdatedNs: now.UnixNano(),
	}
	note := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		ext.Notes = append(ext.Notes, msg)
		rep.Notes = append(rep.Notes, msg)
	}
	deep := chstore.DeepEvidence{External: ext}

	var rows []enrichRow
	if strings.TrimSpace(req.Query.EnrichFlux) == "" {
		note("enrich sorgusu (SORGU 2) tanımlı değil — trace/pod/log kanıtı toplanmadı")
	} else if flux, err := FillTemplate(req.Query.EnrichFlux, enrichVars(req.Query.GroupBy, req.Values, req.From, req.To)); err != nil {
		note("enrich şablonu: %v", err)
	} else if api, err := e.queryAPIFor(req.Source); err != nil {
		note("kaynak istemcisi: %v", err)
	} else if recs, err := api.Query(ctx, flux); err != nil {
		note("SORGU 2 hatası: %v", err)
	} else {
		rep.Rows = len(recs)
		rows, rep.InvalidIDs = parseEnrichRows(recs, now)
		if rep.InvalidIDs > 0 {
			note("%d/%d satırın trace id'si geçersiz (32 hex değil), düşürüldü", rep.InvalidIDs, rep.Rows)
		}
	}
	ext.Rows, ext.InvalidIDs = rep.Rows, rep.InvalidIDs
	rep.ValidIDs = len(rows)
	ids := uniqueTraceIDs(rows)

	if len(rows) > 0 {
		fp := otlp.SeriesFingerprint(metric, attrKVs(attrLabels), req.Source.Name, "")
		exs := make([]*chstore.ExemplarRow, 0, len(rows))
		for _, r := range rows {
			fa := map[string]string{}
			if r.Pod != "" {
				fa["k8s.pod.name"] = r.Pod
			}
			if r.FunctionCode != "" {
				fa["FUNCTION_CODE"] = r.FunctionCode
			}
			if r.ChannelCode != "" {
				fa["CHANNEL_CODE"] = r.ChannelCode
			}
			exs = append(exs, &chstore.ExemplarRow{Fingerprint: fp, Metric: metric, Service: req.Source.Name,
				Time: r.Time, Value: 1, TraceID: r.TraceID, FilteredAttrs: fa})
		}
		if err := e.store.InsertExemplars(ctx, exs); err != nil {
			note("exemplar yazımı: %v", err)
		} else {
			rep.Exemplars = len(exs)
		}
	}
	if len(ids) > 0 {
		sums, err := e.store.SpanSummariesForTraces(ctx, ids, req.From.Add(-enrichPad), req.To.Add(enrichPad))
		if err != nil {
			note("span özeti: %v", err)
		} else {
			ext.SpanSummary = sums
			rep.Traces = len(sums)
		}
		if e.logs == nil {
			note("log deposu yok — log imzası toplanmadı")
		} else if page, err := e.logs.Search(ctx, logstore.Filter{TraceIDs: ids, From: req.From.Add(-enrichPad), To: req.To.Add(enrichPad),
			SeverityMin: enrichLogSeverity, Limit: enrichLogLimit}); err != nil {
			note("log araması: %v", err)
		} else if page != nil {
			deep.LogSignatures = groupLogSignatures(page.Logs)
			rep.LogSignatures = len(deep.LogSignatures)
		}
	}
	deep.TraceIDs = ids
	deep.AffectedPods = podHits(rows)
	rep.Pods = len(deep.AffectedPods)

	if prev, err := e.store.GetHypothesis(ctx, "problem", req.ProblemID); err != nil {
		note("önceki kanıt okunamadı: %v", err)
	} else if prev != nil && prev.Deep != nil {
		mergeExternalEvidence(&deep, prev.Deep)
	}
	h := chstore.RootCauseHypothesis{
		AnchorKind: "problem", AnchorID: req.ProblemID, Service: req.Subject,
		ComputedAt: now.UnixNano(), Confidence: evidenceConfidence(deep),
		Candidates: []chstore.ScoredCause{}, Deep: &deep, ExemplarTraceID: exemplarTrace(deep),
	}
	return rep, e.store.UpsertHypothesis(ctx, h)
}

// attrLabels — metric attr adı → grup değeri (poller'ın yazdığı adlarla).
func (qc QueryConfig) attrLabels(values []string) map[string]string {
	out := map[string]string{}
	for i, tag := range qc.GroupBy {
		if i < len(values) {
			out[qc.metricAttrKey(tag)] = values[i]
		}
	}
	return out
}

// enrichVars — SORGU 2 şablon değişkenleri: from/to (RFC3339 UTC), her
// groupBy tag'ı kendi adıyla, artı spec kısaltmaları: {{op}} = adında
// "operation" geçen ilk tag, {{err}} = adında "error" geçen ilk tag. SAF.
func enrichVars(groupBy, values []string, from, to time.Time) map[string]string {
	vars := map[string]string{
		"from": from.UTC().Format(time.RFC3339),
		"to":   to.UTC().Format(time.RFC3339),
	}
	for i, tag := range groupBy {
		if i >= len(values) {
			break
		}
		vars[tag] = values[i]
		lk := strings.ToLower(tag)
		if _, has := vars["op"]; !has && strings.Contains(lk, "operation") {
			vars["op"] = values[i]
		}
		if _, has := vars["err"]; !has && strings.Contains(lk, "error") {
			vars["err"] = values[i]
		}
	}
	return vars
}

// parseEnrichRows — SORGU 2 satırları; sütun adları büyük/küçük harf ve
// birkaç yazım toleranslı. Trace id normalize (küçük harf) + 32 hex kapısı;
// id'siz/geçersiz satır düşer ve sayılır. En yeni önce, ≤enrichMaxRows.
func parseEnrichRows(recs []Record, now time.Time) (rows []enrichRow, invalid int) {
	for _, r := range recs {
		id := strings.ToLower(strings.TrimSpace(valueOf(r, "traceid", "trace_id", "trace.id")))
		if !traceIDRe.MatchString(id) {
			invalid++
			continue
		}
		ts := recordTime(r)
		if ts.IsZero() {
			ts = now
		}
		rows = append(rows, enrichRow{
			TraceID:      id,
			Pod:          strings.TrimSpace(valueOf(r, "instanceid", "instance_id", "pod", "k8s.pod.name")),
			FunctionCode: strings.TrimSpace(valueOf(r, "functioncode", "function_code")),
			ChannelCode:  strings.TrimSpace(valueOf(r, "kanalkod", "channel_code", "channelcode")),
			Time:         ts,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time.After(rows[j].Time) })
	if len(rows) > enrichMaxRows {
		rows = rows[:enrichMaxRows]
	}
	return rows, invalid
}

func valueOf(r Record, names ...string) string {
	for k, v := range r.Values {
		lk := strings.ToLower(k)
		for _, n := range names {
			if lk == n {
				return v
			}
		}
	}
	return ""
}

func uniqueTraceIDs(rows []enrichRow) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if !seen[r.TraceID] {
			seen[r.TraceID] = true
			out = append(out, r.TraceID)
		}
	}
	return out
}

func attrKVs(labels map[string]string) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(labels))
	for k, v := range labels {
		out = append(out, strKV(k, v))
	}
	return out
}

func podHits(rows []enrichRow) []chstore.PodHit {
	byPod := map[string]*chstore.PodHit{}
	for _, r := range rows {
		if r.Pod == "" {
			continue
		}
		h, ok := byPod[r.Pod]
		if !ok {
			h = &chstore.PodHit{Pod: r.Pod}
			byPod[r.Pod] = h
		}
		h.Count++
		if ns := r.Time.UnixNano(); ns > h.LastSeenNs {
			h.LastSeenNs = ns
		}
	}
	out := make([]chstore.PodHit, 0, len(byPod))
	for _, h := range byPod {
		out = append(out, *h)
	}
	sortPodHits(out)
	return out
}

func sortPodHits(hits []chstore.PodHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Count != hits[j].Count {
			return hits[i].Count > hits[j].Count
		}
		return hits[i].Pod < hits[j].Pod
	})
}

// groupLogSignatures — NormalizeSignature grubu: sayım, en yüksek şiddet,
// ilk mesaj VERBATIM örnek, ayrık trace sayısı. Sayıma göre ilk 15.
func groupLogSignatures(logs []*logstore.LogRecord) []chstore.LogSignature {
	type acc struct {
		sig    chstore.LogSignature
		sevNum uint8
		traces map[string]bool
	}
	groups := map[string]*acc{}
	order := []string{}
	for _, l := range logs {
		if l == nil {
			continue
		}
		tpl := logstore.NormalizeSignature(l.Body)
		if tpl == "" {
			continue
		}
		g, ok := groups[tpl]
		if !ok {
			g = &acc{sig: chstore.LogSignature{Hash: strconv.FormatUint(logstore.SignatureHash(tpl), 16),
				Template: tpl, Sample: l.Body, Severity: l.SeverityText}, sevNum: l.Severity, traces: map[string]bool{}}
			groups[tpl] = g
			order = append(order, tpl)
		}
		g.sig.Count++
		if l.Severity > g.sevNum {
			g.sevNum, g.sig.Severity = l.Severity, l.SeverityText
		}
		if l.TraceID != "" {
			g.traces[l.TraceID] = true
		}
	}
	out := make([]chstore.LogSignature, 0, len(order))
	for _, tpl := range order {
		g := groups[tpl]
		g.sig.TraceCount = len(g.traces)
		out = append(out, g.sig)
	}
	sortSignatures(out)
	if len(out) > enrichMaxSignatures {
		out = out[:enrichMaxSignatures]
	}
	return out
}

func sortSignatures(sigs []chstore.LogSignature) {
	sort.SliceStable(sigs, func(i, j int) bool { return sigs[i].Count > sigs[j].Count })
}

// mergeExternalEvidence — önceki hipotezle birleşim: trace id'ler yeni
// önce (≤50), pod ve imza sayımları toplanır, önceki span özeti bu turda
// boşsa korunur. Önceki External sayıları YENİSİYLE değişir (güncel karar).
func mergeExternalEvidence(cur, prev *chstore.DeepEvidence) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(cur.TraceIDs)+len(prev.TraceIDs))
	for _, id := range append(append([]string{}, cur.TraceIDs...), prev.TraceIDs...) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > enrichMaxRows {
		ids = ids[:enrichMaxRows]
	}
	cur.TraceIDs = ids

	pods := map[string]chstore.PodHit{}
	for _, h := range append(append([]chstore.PodHit{}, prev.AffectedPods...), cur.AffectedPods...) {
		m := pods[h.Pod]
		m.Pod = h.Pod
		m.Count += h.Count
		if h.LastSeenNs > m.LastSeenNs {
			m.LastSeenNs = h.LastSeenNs
		}
		pods[h.Pod] = m
	}
	merged := make([]chstore.PodHit, 0, len(pods))
	for _, h := range pods {
		merged = append(merged, h)
	}
	sortPodHits(merged)
	cur.AffectedPods = merged

	sigs := map[string]chstore.LogSignature{}
	sigOrder := []string{}
	for _, s := range append(append([]chstore.LogSignature{}, cur.LogSignatures...), prev.LogSignatures...) {
		m, ok := sigs[s.Hash]
		if !ok {
			sigs[s.Hash] = s
			sigOrder = append(sigOrder, s.Hash)
			continue
		}
		m.Count += s.Count
		if s.TraceCount > m.TraceCount {
			m.TraceCount = s.TraceCount
		}
		sigs[s.Hash] = m
	}
	ms := make([]chstore.LogSignature, 0, len(sigOrder))
	for _, h := range sigOrder {
		ms = append(ms, sigs[h])
	}
	sortSignatures(ms)
	if len(ms) > enrichMaxSignatures {
		ms = ms[:enrichMaxSignatures]
	}
	cur.LogSignatures = ms

	if cur.External != nil && prev.External != nil && len(cur.External.SpanSummary) == 0 {
		cur.External.SpanSummary = prev.External.SpanSummary
	}
}

// evidenceConfidence — mevcut kanıt TÜRÜ sayısı / 4 (metrik her zaman var).
func evidenceConfidence(d chstore.DeepEvidence) float64 {
	n := 1.0
	if len(d.TraceIDs) > 0 {
		n++
	}
	if len(d.AffectedPods) > 0 {
		n++
	}
	if len(d.LogSignatures) > 0 {
		n++
	}
	return n / 4
}

// exemplarTrace — ilk hatalı trace, yoksa ilk trace (RootCausePanel çizer).
func exemplarTrace(d chstore.DeepEvidence) string {
	if d.External != nil {
		for _, s := range d.External.SpanSummary {
			if s.ErrorSpans > 0 {
				return s.TraceID
			}
		}
	}
	if len(d.TraceIDs) > 0 {
		return d.TraceIDs[0]
	}
	return ""
}
