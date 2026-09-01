package anomaly

// external_test.go — v0.10.228 (Influx D3) dış seri anomalisi sözleşmesi:
//   - spike → kind=external Problem; özne `ext:<kaynak>/<grup>`, gerekçe
//     etiketli; sorgu filtresi metrik/kaynak/groupBy/60 s adımı taşır
//   - sakin seri + açık problem → touch (bayat süpürmeye karşı)
//   - geri dönüş → resolve
//   - genç kaynak: sabit pencere sıfırla DOLDURULMAZ, tarama atlanır
//   - kayan pencere simülasyonu: tek spike = tek açılış + tek kapanış,
//     spike baseline'a kayarken yeniden AÇILMAZ (v0.10.199 dersi)
//   - padMinuteSlots: eksik dakika 0, aralık dışı yok sayılır

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

type fakeExtStore struct {
	series  []chstore.SpanMetricSeries
	open    []chstore.Problem
	upserts []chstore.Problem
	queries []chstore.MetricQueryFilter
	cfg     chstore.AnomalySensitivityConfig
	// v0.10.231 (D6) — mevsimsel
	promo        chstore.AnomalyPromotionConfig
	horizon      int
	seasonal     map[string][]float64
	seasonalDays map[string]map[int64]struct{}
	seasonalReqs []chstore.ExternalSeasonalReq
}

func (f *fakeExtStore) GetAnomalyPromotion(context.Context) chstore.AnomalyPromotionConfig { return f.promo }
func (f *fakeExtStore) MetricsHorizonDays(context.Context) int                              { return f.horizon }
func (f *fakeExtStore) ExternalSeasonal(_ context.Context, req chstore.ExternalSeasonalReq) (map[string][]float64, map[string]map[int64]struct{}, error) {
	f.seasonalReqs = append(f.seasonalReqs, req)
	return f.seasonal, f.seasonalDays, nil
}

func (f *fakeExtStore) QueryMetric(_ context.Context, q chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	f.queries = append(f.queries, q)
	return f.series, nil
}
func (f *fakeExtStore) OpenProblemsSnapshot(context.Context) (*chstore.OpenProblems, error) {
	return chstore.NewOpenProblems(f.open), nil
}
func (f *fakeExtStore) UpsertProblem(_ context.Context, p chstore.Problem) error {
	f.upserts = append(f.upserts, p)
	// Açık listeyi CH gibi güncelle: resolved → düşer, open → yer değiştirir.
	kept := f.open[:0]
	for _, o := range f.open {
		if o.ID != p.ID {
			kept = append(kept, o)
		}
	}
	f.open = kept
	if p.Status == "open" || p.Status == "acknowledged" {
		f.open = append(f.open, p)
	}
	return nil
}
func (f *fakeExtStore) AnomalySensitivity() chstore.AnomalySensitivityConfig { return f.cfg }

// extSeries — end'de biten, dakikada bir noktalı seri.
func extSeries(vals []float64, end time.Time, key ...string) chstore.SpanMetricSeries {
	pts := make([]chstore.SpanMetricPoint, 0, len(vals))
	for i, v := range vals {
		t := end.Add(-time.Duration(len(vals)-1-i) * time.Minute)
		pts = append(pts, chstore.SpanMetricPoint{Time: t.UnixNano(), Value: v})
	}
	return chstore.SpanMetricSeries{GroupKey: key, Points: pts}
}

// baselineVals — 4/5/6 dönüşümlü sakin taban (medyan 5, MAD 1).
func baselineVals(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(4 + i%3)
	}
	return out
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

var extTarget = ExternalTarget{SourceID: "i-1", SourceName: "ggfail", Query: "tfail_adet", GroupBy: []string{"OPERATIONCODE", "ERRORCODE"}}

func newExtScanner(f *fakeExtStore, now time.Time) *ExternalScanner {
	s := NewExternalScanner(f, nil)
	s.now = func() time.Time { return now }
	return s
}

func TestExternalScan_OpensProblemOnSpike(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	vals := append(baselineVals(30), repeat(60, cfg.DwellBuckets)...)
	f := &fakeExtStore{cfg: cfg, series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Series != 1 || rep.Opened != 1 || len(f.upserts) != 1 {
		t.Fatalf("one series, one open: %+v upserts=%d", rep, len(f.upserts))
	}
	p := f.upserts[0]
	if p.Kind != chstore.ProblemKindExternal || p.Status != "open" || p.ID == "" {
		t.Fatalf("kind=external open problem, got %+v", p)
	}
	if p.Service != "ext:ggfail/OP1/E1" || p.Metric != "ext:tfail_adet" || p.RuleID != "anomaly:ext:ggfail/OP1/E1:ext:tfail_adet" {
		t.Fatalf("subject/metric/rule: %q %q %q", p.Service, p.Metric, p.RuleID)
	}
	if !strings.Contains(p.Description, "OPERATIONCODE=OP1") || !strings.Contains(p.Description, "ERRORCODE=E1") {
		t.Fatalf("reason string carries labels: %q", p.Description)
	}
	if p.Severity == "" || p.Comparator == "" || p.Value != 60 {
		t.Fatalf("severity/comparator/value: %+v", p)
	}
	// Baseline medyanı 5 (4/5/6 taban) — 0 çıkarsa mevsimsel boş dizi
	// seçilmiş demektir (chooseBaseline'a 0 geçme sınıfı).
	if p.Threshold != 5 {
		t.Fatalf("baseline median must come from the live series (5), got %v", p.Threshold)
	}
	q := f.queries[0]
	if q.Name != "ext:tfail_adet" || q.Service != "ggfail" || q.StepSeconds != 60 || len(q.GroupBy) != 2 || q.GroupBy[0] != "OPERATIONCODE" {
		t.Fatalf("metric query filter: %+v", q)
	}
}

func TestExternalScan_TouchesOpenProblemWhileQuiet(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	open := chstore.Problem{ID: "p-open", RuleID: "anomaly:ext:ggfail/OP1/E1:ext:tfail_adet", Service: "ext:ggfail/OP1/E1",
		Kind: chstore.ProblemKindExternal, Metric: "ext:tfail_adet", Status: "open", Severity: "critical", Value: 60}
	// Hâlâ yüksek ama dwell penceresinin tamamı kritik z üstünde DEĞİL:
	// karar "none" — problem açık kalır ve dokunulur.
	vals := append(baselineVals(30), 60, 5, 5)
	f := &fakeExtStore{cfg: cfg, open: []chstore.Problem{open},
		series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Touched+rep.Resolved != 1 || rep.Opened != 0 || len(f.upserts) != 1 {
		t.Fatalf("open problem must be touched or resolved, never re-opened: %+v", rep)
	}
	if f.upserts[0].ID != "p-open" {
		t.Fatalf("touch re-upserts the SAME row (updated_at), got id %q", f.upserts[0].ID)
	}
}

func TestExternalScan_ResolvesOnRecovery(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	open := chstore.Problem{ID: "p-open", RuleID: "anomaly:ext:ggfail/OP1/E1:ext:tfail_adet", Service: "ext:ggfail/OP1/E1",
		Kind: chstore.ProblemKindExternal, Metric: "ext:tfail_adet", Status: "open", Severity: "critical", Value: 60}
	vals := append(baselineVals(30), repeat(5, cfg.DwellBuckets+2)...)
	f := &fakeExtStore{cfg: cfg, open: []chstore.Problem{open},
		series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Resolved != 1 || len(f.upserts) != 1 || f.upserts[0].Status != "resolved" || f.upserts[0].ResolvedAt == nil {
		t.Fatalf("recovery resolves the open problem: %+v upserts=%+v", rep, f.upserts)
	}
	if len(f.open) != 0 {
		t.Fatalf("resolved row leaves the open set")
	}
}

func TestExternalScan_YoungSourceIsNotPaddedIntoAnAnomaly(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	// Yalnız 5 dakikalık gözlem; sabit 240'lık pencere olsaydı 235 sıfır
	// + medyan 0 → 40 "anomali" diye açılırdı.
	f := &fakeExtStore{cfg: cfg, series: []chstore.SpanMetricSeries{extSeries([]float64{40, 41, 40, 42, 40}, now, "OP1", "E1")}}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened != 0 || rep.Skipped != 1 || len(f.upserts) != 0 {
		t.Fatalf("young source is skipped, never opened: %+v", rep)
	}
}

func TestExternalScan_ThresholdOverrides(t *testing.T) {
	base := chstore.DefaultAnomalySensitivity()
	cfg := externalSensitivity(base, "ext:x", ExternalThresholds{CriticalZ: 9, Dwell: 5, MinAbsDelta: 20, MinMAD: 3})
	if cfg.CriticalZ != 9 || cfg.DwellBuckets != 5 {
		t.Fatalf("criticalZ/dwell override: %+v", cfg)
	}
	ms := cfg.For("ext:x")
	if ms.MinAbsDelta != 20 || ms.MinMAD != 3 {
		t.Fatalf("per-metric override: %+v", ms)
	}
	def := externalSensitivity(base, "ext:y", ExternalThresholds{})
	if d := def.For("ext:y"); d.MinAbsDelta != externalDefaultMinAbsDelta || d.MinMAD != externalDefaultMinMAD {
		t.Fatalf("defaults when thresholds empty: %+v", d)
	}
	if def.CriticalZ != base.CriticalZ || def.DwellBuckets != base.DwellBuckets {
		t.Fatalf("empty thresholds keep the global blob: %+v", def)
	}
	if _, shared := base.Metrics["ext:x"]; shared {
		t.Fatal("base Metrics map must not be mutated (shared atomic snapshot)")
	}
}

// Kayan pencere simülasyonu (memory feedback-sliding-window-fabricates-events):
// bir spike → tam bir açılış + tam bir kapanış; spike baseline'ın içinden
// geçerken tekrar açılmaz.
func TestExternalScan_SlidingWindowOpensOnceResolvesOnce(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	t0 := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	master := append(baselineVals(60), repeat(60, cfg.DwellBuckets)...)
	master = append(master, baselineVals(90)...)
	f := &fakeExtStore{cfg: cfg}
	opened, resolved := 0, 0
	for end := 40; end <= len(master); end++ {
		now := t0.Add(time.Duration(end) * time.Minute)
		f.series = []chstore.SpanMetricSeries{extSeries(master[:end], now, "OP1", "E1")}
		rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
		if err != nil {
			t.Fatal(err)
		}
		opened += rep.Opened
		resolved += rep.Resolved
	}
	if opened != 1 || resolved != 1 {
		t.Fatalf("one spike → opened=%d resolved=%d (want 1/1)", opened, resolved)
	}
	if len(f.open) != 0 {
		t.Fatalf("nothing stays open after recovery: %+v", f.open)
	}
}

func TestPadMinuteSlots(t *testing.T) {
	end := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	start := end.Add(-4 * time.Minute)
	pts := []chstore.SpanMetricPoint{
		{Time: end.Add(-9 * time.Minute).UnixNano(), Value: 99}, // aralık dışı
		{Time: end.Add(-3 * time.Minute).UnixNano(), Value: 7},
		{Time: end.Add(-1 * time.Minute).UnixNano(), Value: 9},
		{Time: end.Add(-1 * time.Minute).UnixNano(), Value: 4}, // aynı dakika: büyüğü kalır
		{Time: end.UnixNano(), Value: 11},
	}
	got := padMinuteSlots(pts, start, end)
	want := []float64{0, 7, 0, 9, 11}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slot %d: got %v want %v", i, got, want)
		}
	}
	if padMinuteSlots(nil, end, start) != nil {
		t.Fatal("end before start → nil")
	}
}

func TestObservedSpan_ClampsToSlots(t *testing.T) {
	end := time.Date(2026, 9, 2, 10, 0, 30, 0, time.UTC)
	s := []chstore.SpanMetricSeries{
		{Points: []chstore.SpanMetricPoint{{Time: end.Add(-500 * time.Minute).UnixNano(), Value: 1}}},
		{Points: []chstore.SpanMetricPoint{{Time: end.UnixNano(), Value: 1}}},
	}
	start, got, ok := observedSpan(s)
	if !ok || !got.Equal(end.Truncate(time.Minute)) {
		t.Fatalf("end = latest observed minute: %v ok=%v", got, ok)
	}
	if n := int(got.Sub(start)/time.Minute) + 1; n != externalSlots {
		t.Fatalf("span clamped to %d slots, got %d", externalSlots, n)
	}
	if _, _, ok := observedSpan(nil); ok {
		t.Fatal("no points → ok=false")
	}
}

func TestExternalSubject(t *testing.T) {
	if got := ExternalSubject("ggfail", []string{"OP1", "E1"}); got != "ext:ggfail/OP1/E1" {
		t.Fatalf("got %q", got)
	}
	if got := ExternalSubject("ggfail", nil); got != "ext:ggfail" {
		t.Fatalf("got %q", got)
	}
}

// v0.10.229 (D4) — kanıt kancası: açılışta HEMEN, sürerken 5 dk'da bir,
// çözülünce kayıt silinir (yeniden açılış yine hemen). Pencere
// [startedAt−2m, now].
func TestExternalScan_EvidenceHookCadence(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	t0 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	var events []ExternalEvent
	target := extTarget
	target.OnEvidence = func(_ context.Context, ev ExternalEvent) { events = append(events, ev) }
	f := &fakeExtStore{cfg: cfg}
	sc := NewExternalScanner(f, nil)
	now := t0
	sc.now = func() time.Time { return now }
	spike := append(baselineVals(30), repeat(60, cfg.DwellBuckets)...)
	f.series = []chstore.SpanMetricSeries{extSeries(spike, now, "OP1", "E1")}
	if _, err := sc.Scan(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Problem.Status != "open" || events[0].Values[0] != "OP1" || events[0].Current != 60 {
		t.Fatalf("open → one evidence event: %+v", events)
	}
	if !events[0].From.Equal(t0.Add(-externalEnrichLead)) || !events[0].To.Equal(t0) {
		t.Fatalf("window [startedAt-2m, now]: %v..%v", events[0].From, events[0].To)
	}
	// 1 dk sonra hâlâ yüksek (refresh) → kanca yok (5 dk dolmadı)
	now = t0.Add(time.Minute)
	f.series = []chstore.SpanMetricSeries{extSeries(append(spike, 60), now, "OP1", "E1")}
	sc.Scan(context.Background(), target)
	if len(events) != 1 {
		t.Fatalf("within 5 min no re-enrich, got %d", len(events))
	}
	now = t0.Add(6 * time.Minute)
	f.series = []chstore.SpanMetricSeries{extSeries(append(spike, repeat(60, 6)...), now, "OP1", "E1")}
	sc.Scan(context.Background(), target)
	if len(events) != 2 || events[1].Problem.ID != events[0].Problem.ID {
		t.Fatalf("after 5 min re-enrich the SAME problem, got %d", len(events))
	}
	// İyileşme → resolve, kayıt silinir; yeni spike hemen kanca
	now = t0.Add(20 * time.Minute)
	rec := append(append(spike, repeat(60, 6)...), repeat(5, 14)...)
	f.series = []chstore.SpanMetricSeries{extSeries(rec, now, "OP1", "E1")}
	sc.Scan(context.Background(), target)
	if len(f.open) != 0 {
		t.Fatalf("resolved: %+v", f.open)
	}
	now = t0.Add(60 * time.Minute)
	again := append(append(rec, baselineVals(37)...), repeat(60, cfg.DwellBuckets)...)
	f.series = []chstore.SpanMetricSeries{extSeries(again, now, "OP1", "E1")}
	sc.Scan(context.Background(), target)
	if len(events) != 3 || events[2].Problem.ID == events[0].Problem.ID {
		t.Fatalf("re-open enriches immediately with a NEW problem, got %d", len(events))
	}
}

// v0.10.231 (D6) — mevsimsel baseline: aynı-dilim geçmişi "bu saatte 60
// normal" diyorsa ardışık 4 saatin 5'i anomali AÇMAZ; retention ufku
// gün-çeşitliliği eşiğinin altındaysa mevsimsel okuma HİÇ yapılmaz;
// gün-çeşitliliği (≥3 gün) sağlanmayan anahtar mevsimselden düşer.
func TestExternalScan_SeasonalBaselinePreventsFalseOpen(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	vals := append(baselineVals(30), repeat(60, cfg.DwellBuckets)...)
	key := "OP1" + chstore.ExternalSeasonalKeySep + "E1"
	season := repeat(60, 20)
	days := map[string]map[int64]struct{}{key: {1: {}, 2: {}, 3: {}}}
	f := &fakeExtStore{cfg: cfg, horizon: 14, seasonal: map[string][]float64{key: season}, seasonalDays: days,
		series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	rep, err := newExtScanner(f, now).Scan(context.Background(), extTarget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Opened != 0 || rep.Seasonal != 1 || len(f.upserts) != 0 {
		t.Fatalf("seasonal baseline (60 at this hour) must not open: %+v", rep)
	}
	req := f.seasonalReqs[0]
	if req.Metric != "ext:tfail_adet" || req.Service != "ggfail" || req.Class != "weekday" || req.TargetSod != 36000 || req.RadiusSec != 900 || len(req.GroupBy) != 2 {
		t.Fatalf("seasonal request: %+v", req)
	}
	if !req.Cutoff.Equal(now.Add(-14 * 24 * time.Hour)) || !req.Upper.Before(now) {
		t.Fatalf("window: %v..%v", req.Cutoff, req.Upper)
	}

	// Ufuk kısa → mevsimsel okunmaz, ardışık baseline → açılır.
	g := &fakeExtStore{cfg: cfg, horizon: 2, seasonal: map[string][]float64{key: season}, seasonalDays: days,
		series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	rep, _ = newExtScanner(g, now).Scan(context.Background(), extTarget)
	if len(g.seasonalReqs) != 0 || rep.Opened != 1 || rep.Seasonal != 0 {
		t.Fatalf("horizon below the day-diversity floor must skip the seasonal read: reqs=%d rep=%+v", len(g.seasonalReqs), rep)
	}

	// Ufuk 5 gün → gün sayısı ufka iner (14 değil).
	h := &fakeExtStore{cfg: cfg, horizon: 5, seasonal: map[string][]float64{}, seasonalDays: map[string]map[int64]struct{}{},
		series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	newExtScanner(h, now).Scan(context.Background(), extTarget)
	if len(h.seasonalReqs) != 1 || !h.seasonalReqs[0].Cutoff.Equal(now.Add(-5*24*time.Hour)) {
		t.Fatalf("days clamp to horizon: %+v", h.seasonalReqs)
	}

	// Gün-çeşitliliği yok (tek gün) → mevsimsel düşer → ardışık → açılır.
	k := &fakeExtStore{cfg: cfg, horizon: 14, seasonal: map[string][]float64{key: season},
		seasonalDays: map[string]map[int64]struct{}{key: {1: {}}},
		series: []chstore.SpanMetricSeries{extSeries(vals, now, "OP1", "E1")}}
	rep, _ = newExtScanner(k, now).Scan(context.Background(), extTarget)
	if rep.Opened != 1 || rep.Seasonal != 0 {
		t.Fatalf("single-day seasonal history must be pruned: %+v", rep)
	}
}
