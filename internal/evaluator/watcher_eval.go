package evaluator

// Imported ES Watcher evaluation (v0.9.x — Faz-1 of "birebir aynı
// Watcher JSON'ı Coremetry çalışabilsin"). Rules carrying a verbatim
// watcher definition (AlertRule.WatcherJSON != "") re-parse it every
// run — the stored raw JSON is the source of truth, exactly as the
// operator PUT it to _watcher/watch — and count matches via
// logstore.RawSearch (indices + body pass through untouched; the ES
// backend injects the cost guards). Open/resolve rides the SAME
// sustain/cooldown state machine as the saved-search log_query path
// (settleCountAlert), so notifications, incident-attach, escalation
// and the stale sweeps all apply for free.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/watcher"
)

// watcherDueTolerance absorbs ticker jitter: the 1m tick fires a few
// ms shy of the exact multiple, and without slack a 5m-interval watch
// would slip to 6m forever (elapsed 299.99s < 300s → skipped tick).
const watcherDueTolerance = 5 * time.Second

// watcherDue reports whether a watcher rule should run this tick.
// Zero lastRun (never ran / leader change reset) and zero interval
// (no parseable schedule) are always due. Pure — table-tested.
func watcherDue(lastRun time.Time, interval time.Duration, now time.Time) bool {
	if lastRun.IsZero() || interval <= 0 {
		return true
	}
	return now.Sub(lastRun) >= interval-watcherDueTolerance
}

// watcherTotalCap is the track_total_hits cap handed to RawSearch:
// threshold*2 so a "gte 15000" watch counts PAST the ES 10k default
// count saturation instead of flat-lining at 10000 (the saturation
// bug class). Floor 10 (always-condition watches only need "did
// anything match"). Pure.
//
// Review F11 (v0.9.x): the cap must NEVER land at or below the
// threshold — a count saturating below the threshold makes a gt/gte
// watch permanently dead and an lt/lte watch fire falsely (the old
// flat 2^30 ceiling did exactly that for thresholds above 2^30, and
// ES discards the saturation `relation` on our decode path). Numeric
// track_total_hits is a Java int in ES (≤ 2^31-1); when even
// threshold+1 exceeds that, the -1 sentinel tells RawSearch to send
// track_total_hits:true — an exact (long) count that any threshold
// compares correctly against.
func watcherTotalCap(threshold float64) int {
	const floor = 10
	const esMaxTotalHits = 1<<31 - 1 // largest numeric track_total_hits ES accepts
	if threshold <= 0 {
		return floor
	}
	if threshold+1 >= float64(esMaxTotalHits) {
		return -1 // exact-count sentinel
	}
	c := threshold * 2
	if c > float64(esMaxTotalHits) {
		c = float64(esMaxTotalHits) // still ≥ threshold+1 in this branch
	}
	if int(c) < floor {
		return floor
	}
	return int(c)
}

// watcherTickAction is what one evaluator tick does for a watcher
// rule: run the search (due on the watch's own interval), keep the
// open problem's updated_at fresh (not due, problem open), or nothing.
type watcherTickAction int

const (
	watcherTickIdle watcherTickAction = iota
	watcherTickRun
	watcherTickKeepAlive
)

// watcherTickPlan decides the tick action. Pure — table-tested.
//
// Review F0/F9 (v0.9.x): sweepStaleProblems auto-resolves any open
// problem whose updated_at is older than 3× the 1m evaluator tick
// (~3m), with no metric exemption. A watcher paced slower than that
// (including the 300s default) only refreshed its problem on due
// runs, so a continuously-breaching 5m watch flapped forever:
// open+notify → swept "source silent" at ~3-4m (cooldown stamp wiped
// by clearResolved) → re-open+notify at the next due run. The
// keep-alive action refreshes updated_at on every NOT-due tick while
// a problem is open (cheap FindOpenProblem + Upsert, only when one
// exists), so the sweep never sees a live watcher problem as stale —
// the sweep itself stays untouched for every other metric. This also
// covers the due-run-errored case (persistent 403/timeout): the next
// tick's keep-alive bounds updated_at age at ~2 ticks < the 3-tick
// cutoff, and the problem honestly stays open while the source is
// unmeasurable (the query error is visible in /admin/elastic).
func watcherTickPlan(lastRun time.Time, interval time.Duration, now time.Time, hasOpenProblem bool) watcherTickAction {
	return watcherTickPlanDue(watcherDue(lastRun, interval, now), hasOpenProblem)
}

// watcherTickPlanDue is the due-agnostic core (Faz-2): calendar-cron
// watches compute `due` via watcherCronDue instead of the interval
// arithmetic, but the run / keep-alive / idle contract — and the
// F0/F9 stale-sweep protection it encodes — is IDENTICAL.
func watcherTickPlanDue(due, hasOpenProblem bool) watcherTickAction {
	if due {
		return watcherTickRun
	}
	if hasOpenProblem {
		return watcherTickKeepAlive
	}
	return watcherTickIdle
}

// watcherCronDue reports whether a calendar-cron watch should run
// this tick: a scheduled fire time exists in (lastRun, now]. Zero
// lastRun (never ran / leader change reset) is NOT due (v0.9.202
// review-fix): boot'ta "hemen koş" davranışı, HER pod restart /
// leader değişiminde TÜM takvim-cron watch'larını anında ateşliyordu —
// gece 08:00 watch'ı gündüz restart'ta koşup yanlış pencere sayardı.
// ES restart paritesi: SONRAKI planlı ateşleme beklenir; çağıran zero
// lastRun'da damgayı basar (bkz. evaluateWatcher cron dalı), böylece
// next-fire boot anından itibaren hesaplanır. Pure — table-tested.
func watcherCronDue(lastRun time.Time, exprs []string, now time.Time) bool {
	if lastRun.IsZero() {
		return false
	}
	for _, e := range exprs {
		next, err := watcher.NextFire(e, lastRun)
		if err != nil {
			continue
		}
		if !next.After(now) {
			return true
		}
	}
	return false
}

// evaluateWatcher runs one imported watcher rule: parse the stored
// definition, pace on ITS schedule interval (not the evaluator tick),
// count via the guarded raw search, then settle open/resolve through
// the shared count-alert machine. Mirrors evaluateLogQuery's posture:
// evaluated ONCE per rule (never fanned out per service — HA audit H9
// lesson), 30s per-evaluation deadline so an ES brownout can't stall
// the whole alerting tick chain.
func (e *Evaluator) evaluateWatcher(ctx context.Context, r chstore.AlertRule) {
	if e.logs == nil {
		log.Printf("[evaluator] watcher %s: logs backend not wired", r.ID)
		return
	}
	w, err := watcher.Parse([]byte(r.WatcherJSON))
	if err != nil {
		log.Printf("[evaluator] watcher %s: parse stored definition: %v", r.ID, err)
		return
	}
	if w.Input == nil || w.Input.Search == nil || len(bytes.TrimSpace(w.Input.Search.Request.Body)) == 0 {
		// Import marks such watches disabled (no search / no
		// executable body — review F10); reaching here means the
		// operator force-enabled one. Skip loudly rather than firing a
		// meaningless match-all count over the injected 24h window.
		log.Printf("[evaluator] watcher %s: no executable input.search.request.body in definition — skipping", r.ID)
		return
	}

	// Per-evaluation deadline, same shape as evaluateLogQuery (v0.8.3):
	// bounds a hung ES call — and the keep-alive's problem round-trip —
	// against the process-lifetime ctx.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Pacing (the re-parse wins if the stored JSON and the rule row
	// drifted — the JSON is the source of truth). Faz-2: a
	// calendar-cron schedule ("0 0 8 * * ?") is due when a scheduled
	// fire time passed since the last run; everything else keeps the
	// Faz-1 interval pacing (interval / fixed-rate cron→interval /
	// WindowSec / 300s default) UNCHANGED.
	now := time.Now()
	e.watcherMu.Lock()
	lastRun := e.watcherLastRun[r.ID]
	e.watcherMu.Unlock()

	var due bool
	var cadence string
	if crons := w.CalendarCron(); len(crons) > 0 {
		// v0.9.202 review-fix — boot/leader-değişimi: zero lastRun'da
		// HEMEN koşma (boot fırtınası: tüm takvim watch'ları restart
		// anında ateşlenirdi — gece 08:00 watch'ı gündüz koşardı).
		// Damgayı bas, SONRAKI planlı ateşlemeyi bekle (ES paritesi).
		if lastRun.IsZero() {
			e.watcherMu.Lock()
			e.watcherLastRun[r.ID] = now
			e.watcherMu.Unlock()
			lastRun = now
		}
		due = watcherCronDue(lastRun, crons, now)
		cadence = fmt.Sprintf("cron %s", strings.Join(crons, " | "))
	} else {
		intervalSec := w.IntervalSec()
		if intervalSec == 0 {
			intervalSec = r.WindowSec
		}
		if intervalSec == 0 {
			intervalSec = 300
		}
		interval := time.Duration(intervalSec) * time.Second
		due = watcherDue(lastRun, interval, now)
		cadence = fmt.Sprintf("every %s", interval)
	}
	if !due {
		// Not due this tick: keep an open problem's updated_at fresh so
		// the 3×tick stale sweep can't resolve it "source silent"
		// between runs (review F0/F9 — see watcherTickPlanDue). The
		// extra store round-trip only happens while a problem is open.
		open, err := e.store.FindOpenProblem(ctx, r.ID, "")
		hasOpen := err == nil && open != nil && open.ID != ""
		if watcherTickPlanDue(false, hasOpen) == watcherTickKeepAlive {
			if err := e.store.UpsertProblem(ctx, *open); err != nil {
				log.Printf("[evaluator] watcher %s keep-alive: %v", r.ID, err)
			}
		}
		return
	}
	e.watcherMu.Lock()
	// Stamped at ATTEMPT, not success — a broken search must not
	// retry on every 1m tick, it retries on the watch's cadence.
	e.watcherLastRun[r.ID] = now
	e.watcherMu.Unlock()

	indices, body := w.Input.Search.Request.Indices, w.Input.Search.Request.Body

	// Condition dispatch (Faz-2): agg-path conditions read the
	// aggregations payload via RawSearchPayload; every other shape —
	// hits.total compare, always, or a force-enabled unsupported
	// condition — stays on the Faz-1 RawSearch count path, behaviour
	// unchanged.
	spec, specOK := w.ConditionSpec()
	switch {
	case specOK && spec.Kind == watcher.CondAggValue:
		payload, _, err := e.logs.RawSearchPayload(ctx, indices, body, watcherTotalCap(r.Threshold))
		if err != nil {
			log.Printf("[evaluator] watcher measure %s: %v", r.ID, err)
			return
		}
		value, err := extractPayloadValue(payload, spec.Path)
		if err != nil {
			log.Printf("[evaluator] watcher %s: %v — check the watch's aggs match the condition path", r.ID, err)
			return
		}
		desc := fmt.Sprintf("watcher: %s — %s = %g (threshold %s %g, evaluated %s)",
			r.Name, spec.Path, value, r.Comparator, r.Threshold, cadence)
		// Agg-type watches embed no hit samples (nil enricher): the
		// condition fires on an aggregate, so individual documents can
		// be irrelevant to WHY it fired (and the query may be shaped
		// purely for bucketing). Faz-2 keeps examples hits-only.
		e.settleCountAlert(ctx, r, now, value, "watcher", desc, nil)
	case specOK && spec.Kind == watcher.CondArrayCompare:
		payload, _, err := e.logs.RawSearchPayload(ctx, indices, body, watcherTotalCap(r.Threshold))
		if err != nil {
			log.Printf("[evaluator] watcher measure %s: %v", r.ID, err)
			return
		}
		res, err := extractArrayCompareMax(payload, spec.ArrayPath, spec.ItemPath, r.Comparator, r.Threshold)
		if err != nil {
			log.Printf("[evaluator] watcher %s: %v — check the watch's aggs match the array_compare path", r.ID, err)
			return
		}
		value := arrayCompareSettleValue(res, r.Comparator, r.Threshold)
		item := spec.ItemPath
		if item == "" {
			item = "value"
		}
		desc := fmt.Sprintf("watcher: %s — array_compare %s max %s = %g (threshold %s %g, any matching element fires, evaluated %s)",
			r.Name, spec.ArrayPath, item, value, r.Comparator, r.Threshold, cadence)
		// Same rationale as CondAggValue: no hit samples on agg shapes.
		e.settleCountAlert(ctx, r, now, value, "watcher", desc, nil)
	default:
		total, err := e.logs.RawSearch(ctx, indices, body, watcherTotalCap(r.Threshold))
		if err != nil {
			log.Printf("[evaluator] watcher measure %s: %v", r.ID, err)
			return
		}
		desc := fmt.Sprintf("watcher: %s — search matched %d (threshold %s %.0f, evaluated %s)",
			r.Name, total, r.Comparator, r.Threshold, cadence)
		e.settleCountAlert(ctx, r, now, float64(total), "watcher", desc,
			e.watcherSampleEnricher(r.ID, indices, body))
	}
}

// watcherSampleEnricher returns the transition-to-open description
// enricher for hits-type watches (Faz-2 — ES Watcher parity: the
// watcher's own actions interpolate ctx.payload.hits): a SECOND small
// query with the same body fetches up to 3 matching documents whose
// one-line summaries are appended to the Problem description, which
// every notification channel already carries. Errors are SOFT — a
// broken sample fetch logs and the fire proceeds without examples.
// Called ONLY at the open transition (settleCountAlert), never on
// refresh ticks.
func (e *Evaluator) watcherSampleEnricher(ruleID string, indices []string, body json.RawMessage) func(context.Context) string {
	return func(ctx context.Context) string {
		lines, err := e.logs.RawSearchSamples(ctx, indices, body, 3)
		if err != nil {
			log.Printf("[evaluator] watcher %s samples: %v (fire proceeds without examples)", ruleID, err)
			return ""
		}
		return watcherSampleBlock(lines)
	}
}

// watcherSampleBlock renders the sample lines appended to the Problem
// description at fire time. Pure — table-tested.
func watcherSampleBlock(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nÖrnekler:")
	for _, l := range lines {
		b.WriteString("\n- ")
		b.WriteString(l)
	}
	return b.String()
}

// ── Agg payload access (Faz-2) ──────────────────────────────────────

// decodePayload decodes the RawSearchPayload subset with UseNumber so
// numeric literals survive the walk byte-exact (the review-F4
// discipline applied to the READ side).
func decodePayload(payload json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("watcher payload: %w", err)
	}
	return root, nil
}

// walkPayload resolves a dotted path inside the decoded payload.
// Aggregation names may themselves contain dots, so map lookups
// prefer the LONGEST literal key matching a prefix of the remaining
// path; numeric segments index into arrays (the ES watcher
// "buckets.0.doc_count" convention).
func walkPayload(node any, path string) (any, error) {
	if path == "" {
		return node, nil
	}
	switch n := node.(type) {
	case map[string]any:
		segs := strings.Split(path, ".")
		for i := len(segs); i >= 1; i-- {
			key := strings.Join(segs[:i], ".")
			if v, ok := n[key]; ok {
				return walkPayload(v, strings.Join(segs[i:], "."))
			}
		}
		return nil, fmt.Errorf("segment %q not found", segs[0])
	case []any:
		seg, rest, _ := strings.Cut(path, ".")
		idx, err := strconv.Atoi(seg)
		if err != nil {
			return nil, fmt.Errorf("segment %q indexes an array but is not a number", seg)
		}
		if idx < 0 || idx >= len(n) {
			return nil, fmt.Errorf("index %d out of range (array has %d elements)", idx, len(n))
		}
		return walkPayload(n[idx], rest)
	default:
		return nil, fmt.Errorf("path remainder %q descends into a scalar", path)
	}
}

// payloadNumber converts a walked terminal to float64.
func payloadNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	default:
		return 0, false
	}
}

// extractPayloadValue walks the dotted payload path (agg-value
// compare conditions, e.g. "aggregations.err_count.value") to a
// numeric value. Pure — table-tested with fixture agg responses.
func extractPayloadValue(payload json.RawMessage, path string) (float64, error) {
	root, err := decodePayload(payload)
	if err != nil {
		return 0, err
	}
	v, err := walkPayload(root, path)
	if err != nil {
		return 0, fmt.Errorf("payload path %q: %w", path, err)
	}
	n, ok := payloadNumber(v)
	if !ok {
		return 0, fmt.Errorf("payload path %q is not numeric (got %T)", path, v)
	}
	return n, nil
}

// arrayCompareResult is the outcome of one array_compare evaluation.
type arrayCompareResult struct {
	Matched    bool    // any element satisfied the comparison
	MaxMatched float64 // MAX of the matching values (valid when Matched)
	MaxAll     float64 // MAX of every numeric value seen (valid when HasItems)
	HasItems   bool    // at least one element resolved to a number
}

// extractArrayCompareMax evaluates the ES array_compare semantics in
// the Faz-2 subset: walk to the array, resolve itemPath inside each
// element ("" = the element itself; elements missing the path are
// skipped, matching ES's null-comparison behaviour), and fire when
// ANY element satisfies the comparison — the reported value is the
// MAX of the matches. Pure — table-tested.
func extractArrayCompareMax(payload json.RawMessage, arrayPath, itemPath, comparator string, threshold float64) (arrayCompareResult, error) {
	var res arrayCompareResult
	root, err := decodePayload(payload)
	if err != nil {
		return res, err
	}
	node, err := walkPayload(root, arrayPath)
	if err != nil {
		return res, fmt.Errorf("array path %q: %w", arrayPath, err)
	}
	arr, ok := node.([]any)
	if !ok {
		return res, fmt.Errorf("array path %q is not an array (got %T)", arrayPath, node)
	}
	for _, el := range arr {
		v := el
		if itemPath != "" {
			w, werr := walkPayload(el, itemPath)
			if werr != nil {
				continue // element lacks the path — skipped, not fatal
			}
			v = w
		}
		n, isNum := payloadNumber(v)
		if !isNum {
			continue
		}
		if !res.HasItems || n > res.MaxAll {
			res.MaxAll = n
		}
		res.HasItems = true
		if compare(n, comparator, threshold) {
			if !res.Matched || n > res.MaxMatched {
				res.MaxMatched = n
			}
			res.Matched = true
		}
	}
	return res, nil
}

// arrayCompareSettleValue maps the array_compare outcome onto the
// scalar handed to the shared count-alert machine, which RE-DERIVES
// breached via compare(value, comparator, threshold) — so the value
// must reproduce the verdict exactly:
//
//   - matched → MAX of the matching elements (satisfies the
//     comparator by construction; becomes the Problem value).
//   - not matched, items seen → the observed MAX. When no element
//     matched, the max can never accidentally satisfy the comparator
//     (for gt/gte the max IS an element; for lt/lte max<threshold
//     would imply every element matched) — proven in the table test.
//   - no items (empty buckets) → a synthetic non-breaching value:
//     threshold±1 on the failing side. A naive 0 would FALSE-FIRE
//     lt/lte watches on an empty aggregation.
func arrayCompareSettleValue(res arrayCompareResult, comparator string, threshold float64) float64 {
	if res.Matched {
		return res.MaxMatched
	}
	if res.HasItems {
		return res.MaxAll
	}
	// v0.9.202 review-fix — sentetik "ihlal-değil" değeri: threshold±1,
	// |threshold| büyükken float64'te threshold'un KENDİSİNE çöküyordu
	// (1e18+1 == 1e18) ve yanlış verdict temsil ediyordu. Nextafter bir
	// ULP komşusunu verir — her büyüklükte threshold'dan farklı ve doğru
	// yönde.
	switch comparator {
	case "<", "<=":
		return math.Nextafter(threshold, math.Inf(1))
	default:
		return math.Nextafter(threshold, math.Inf(-1))
	}
}

// settleCountAlert is the count-threshold open/resolve state machine
// shared by the log_query (v0.5.242) and watcher (v0.9.x) paths:
// sustained-breach gate (ForSec), post-resolve cooldown (CooldownSec),
// then open / refresh / resolve against the (rule, "" service) key.
// Extracted VERBATIM from evaluateLogQuery — behaviour is pinned by
// that path's history (stamps ride the same Redis mirror, v0.8.354);
// only the Problem metric + description + log labels are
// parameterised. Service stays empty on the resulting Problem: the
// query/watch itself carries any service scoping.
//
// enrichOpen (Faz-2, nil = no-op — the log_query path and agg-type
// watches pass nil, behaviour unchanged): called ONLY on the
// transition-to-open, after every suppression gate (ForSec sustain,
// cooldown), so a per-fire side query (the watcher hit samples) runs
// exactly once per opened Problem — never on refresh ticks, never on
// suppressed breaches. Its return is appended to the description the
// notifications carry.
func (e *Evaluator) settleCountAlert(ctx context.Context, r chstore.AlertRule, now time.Time, value float64, metric, description string, enrichOpen func(context.Context) string) {
	breached := compare(value, r.Comparator, r.Threshold)

	key := breachKey{RuleID: r.ID, Service: ""}
	if breached && r.ForSec > 0 {
		first, existing := e.breachStart(ctx, key, now, r.ForSec)
		if !existing {
			return
		}
		if now.Sub(first) < time.Duration(r.ForSec)*time.Second {
			return
		}
	}
	if !breached {
		e.clearBreach(ctx, key)
	}

	open, err := e.store.FindOpenProblem(ctx, r.ID, "")
	hasOpen := err == nil && open != nil && open.ID != ""

	switch {
	case breached && !hasOpen:
		if r.CooldownSec > 0 {
			if rt, seen := e.resolvedAt(ctx, key); seen && now.Sub(rt) < time.Duration(r.CooldownSec)*time.Second {
				return
			}
		}
		// Transition-to-open confirmed (all gates passed): enrich the
		// description once — the watcher sample fetch lives here.
		if enrichOpen != nil {
			if extra := enrichOpen(ctx); extra != "" {
				description += extra
			}
		}
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      r.ID,
			RuleName:    r.Name,
			Severity:    r.Severity,
			Service:     "",
			Metric:      metric,
			Value:       value,
			Threshold:   r.Threshold,
			Status:      "open",
			Description: description,
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] open %s problem: %v", metric, err)
			return
		}
		log.Printf("[evaluator] PROBLEM OPENED (%s): %s = %.0f (threshold %s %.0f)",
			metric, r.Name, value, r.Comparator, r.Threshold)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] %s incident attach: %v", metric, err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}
	case breached && hasOpen:
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] refresh %s problem: %v", metric, err)
		}
	case !breached && hasOpen:
		resolvedAt := now.UnixNano()
		open.Status = "resolved"
		open.ResolvedAt = &resolvedAt
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] resolve %s problem: %v", metric, err)
		} else {
			e.stampResolved(ctx, key, now, r.CooldownSec)
			log.Printf("[evaluator] PROBLEM RESOLVED (%s): %s", metric, r.Name)
		}
	}
}
