// Package pipeline is Coremetry's ingest-time policy engine
// (v0.5.263). Operator-defined rules run BEFORE the sampler so
// dropped spans never touch the consumer's batch buffer, the
// sampler's tail buffer, or ClickHouse. Equivalent to
// Dynatrace OpenPipeline / Vector's transform pipeline for the
// drop case — implemented narrowly here so we don't grow a
// second-config-system surface inside Coremetry.
//
// Scope:
//   - Signal: spans, logs, and metrics (v0.8.282 completed the
//     logs + metrics application; the span-only MVP was v0.5.263).
//     AcceptSpan / AcceptLog / AcceptMetric each scope to their
//     own Signal so one shared catalog cleanly partitions rules.
//   - Rule kind: "drop", "enrich" (set resource attribute), and
//     "sample" (probabilistic keep). Sample on metrics is a sharp
//     tool — it estimates aggregates — but supported for symmetry.
//   - One Condition per rule (key = op + value). Multi-condition
//     AND is a follow-up; one predicate covers the dominant
//     "drop spans from service X" + "drop kind=internal" use
//     cases.
//
// HA story: rules live in system_settings (single JSON blob).
// Every replica loads at boot + on every PUT. No per-pod
// drift — same Redis-arbitrated config pattern as Tempo /
// Copilot.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// RuleKind enumerates the actions a matching rule performs.
type RuleKind string

const (
	KindDrop   RuleKind = "drop"   // drop the signal entirely
	KindEnrich RuleKind = "enrich" // add / override a resource attribute (v0.5.270)
	KindSample RuleKind = "sample" // probabilistic keep at Rate (v0.5.270)
)

// Signal scopes the rule to a single OTel signal type.
type Signal string

const (
	SignalSpans   Signal = "spans"
	SignalLogs    Signal = "logs"
	SignalMetrics Signal = "metrics"
)

// Op enumerates the comparison operators a Condition supports.
// Kept narrow on purpose — full FilterExpr-grade ops belong on
// the query side, not the ingest side where the budget per
// span is microseconds.
type Op string

const (
	OpEq         Op = "="
	OpNeq        Op = "!="
	OpContains   Op = "contains"
	OpStartsWith Op = "startsWith"
	OpEndsWith   Op = "endsWith"
)

// Condition is a single attribute predicate. Key supports the
// well-known span fields directly (service.name, name, kind,
// status_code) plus any attribute via the "attr." or "resource."
// prefix.
type Condition struct {
	Key   string `json:"key"`
	Op    Op     `json:"op"`
	Value string `json:"value"`
}

// Rule is one operator-defined pipeline policy.
type Rule struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    RuleKind  `json:"kind"`
	Signal  Signal    `json:"signal"`
	Enabled bool      `json:"enabled"`
	When    Condition `json:"when"`

	// SetAttributes — enrich rules only (v0.5.270). When the
	// rule matches, every key/value pair is written to the
	// span's RESOURCE attributes (overrides if the key
	// already exists). Empty map = no-op.
	SetAttributes map[string]string `json:"setAttributes,omitempty"`

	// Rate — sample rules only (v0.5.270). Keep probability
	// in [0, 1]; 1.0 = keep everything (no-op), 0.0 = drop
	// everything (use a drop rule instead). Random keep
	// decision is local to this rule — the global head
	// sampler still runs afterwards and may further sample
	// the matching span out.
	Rate float64 `json:"rate,omitempty"`
}

// Engine evaluates the active rule set against each incoming
// span. Methods are safe for concurrent use; the RWMutex
// protects the rules slice against the LoadRules / SaveRules
// admin path. AcceptSpan is the hot path — keep it allocation-
// free past the read lock.
type Engine struct {
	mu    sync.RWMutex
	rules []Rule
}

// store is the small interface the engine needs to persist
// rules. Defined locally so the package doesn't depend on
// chstore for type-coupling.
type store interface {
	GetPipelineRulesRaw(ctx context.Context) ([]byte, error)
	PutPipelineRulesRaw(ctx context.Context, raw []byte) error
}

// New returns an engine with no rules. Call LoadPersisted at
// boot to hydrate from system_settings.
func New() *Engine { return &Engine{} }

// LoadPersisted hydrates the in-memory rule set from
// system_settings. Missing blob = empty rule set (engine
// accepts everything). Failure is non-fatal — caller logs and
// proceeds.
func (e *Engine) LoadPersisted(ctx context.Context, st store) error {
	if e == nil || st == nil {
		return nil
	}
	raw, err := st.GetPipelineRulesRaw(ctx)
	if err != nil {
		return err
	}
	var rules []Rule
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return fmt.Errorf("pipeline rules decode: %w", err)
		}
	}
	e.mu.Lock()
	e.rules = normalise(rules)
	e.mu.Unlock()
	return nil
}

// StartConfigRefresh — v0.5.324. Background poll keeps the
// pipeline rules in sync with the shared persisted blob across
// pods. interval ≤ 0 → 30s.
func (e *Engine) StartConfigRefresh(ctx context.Context, st store, interval time.Duration) {
	if e == nil || st == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.LoadPersisted(ctx, st); err != nil {
				log.Printf("[pipeline] config refresh: %v", err)
			}
		}
	}
}

// Rules returns a snapshot copy of the current rule set, sorted
// by name. Safe to mutate the returned slice.
func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// Upsert creates or replaces a rule by ID + persists the new
// catalog. Returns the canonical Rule (with normalised fields)
// so the API handler can echo it back to the operator.
func (e *Engine) Upsert(ctx context.Context, st store, r Rule) (Rule, error) {
	if st == nil {
		return Rule{}, fmt.Errorf("pipeline store not configured")
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return Rule{}, fmt.Errorf("rule name required")
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("rule-%x", uniqHash(r.Name))
	}
	if r.Kind != KindDrop && r.Kind != KindEnrich && r.Kind != KindSample {
		return Rule{}, fmt.Errorf("unknown rule kind %q", r.Kind)
	}
	if r.Kind == KindSample {
		if r.Rate < 0 || r.Rate > 1 {
			return Rule{}, fmt.Errorf("sample rate must be in [0, 1], got %v", r.Rate)
		}
	}
	if r.Kind == KindEnrich {
		if len(r.SetAttributes) == 0 {
			return Rule{}, fmt.Errorf("enrich rule needs at least one attribute to set")
		}
	}
	if r.Signal != SignalSpans && r.Signal != SignalLogs && r.Signal != SignalMetrics {
		return Rule{}, fmt.Errorf("unknown signal %q", r.Signal)
	}
	r.When.Key = strings.TrimSpace(r.When.Key)
	r.When.Value = strings.TrimSpace(r.When.Value)
	if r.When.Key == "" {
		return Rule{}, fmt.Errorf("rule predicate key required")
	}

	e.mu.Lock()
	idx := -1
	for i := range e.rules {
		if e.rules[i].ID == r.ID {
			idx = i
			break
		}
	}
	if idx >= 0 {
		e.rules[idx] = r
	} else {
		e.rules = append(e.rules, r)
	}
	snapshot := append([]Rule{}, e.rules...)
	e.rules = normalise(e.rules)
	e.mu.Unlock()

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return Rule{}, err
	}
	if err := st.PutPipelineRulesRaw(ctx, raw); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Delete removes a rule by ID. Idempotent — unknown ID is a
// silent no-op so the audit log doesn't see double-fires when
// the admin clicks delete twice.
func (e *Engine) Delete(ctx context.Context, st store, id string) error {
	if st == nil {
		return fmt.Errorf("pipeline store not configured")
	}
	e.mu.Lock()
	next := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		if r.ID == id {
			continue
		}
		next = append(next, r)
	}
	e.rules = next
	snapshot := append([]Rule{}, e.rules...)
	e.mu.Unlock()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return st.PutPipelineRulesRaw(ctx, raw)
}

// AcceptSpan is the hot path — called once per incoming span
// before sampling. Returns false to drop the span entirely;
// the caller bumps its dropped-by-pipeline counter and never
// touches the consumer buffer.
//
// Rule evaluation walks the catalog in order. Multiple rules
// can match a single span:
//   • Drop short-circuits — first matching drop wins, span
//     is gone.
//   • Sample probability-rolls against rule.Rate; failure to
//     keep returns false (treated as dropped by the
//     ingester). Continues to subsequent rules only when the
//     keep roll succeeded.
//   • Enrich mutates the span's resource attributes in place
//     and continues; a later drop / sample may still discard.
//
// Hot-path discipline:
//   • read lock per call (uncontended in steady state)
//   • single math/rand/v2 call per sample-rule (lockless)
//   • no map lookups for well-known fields
//   • early-return on the first drop / drop-by-sample match
func (e *Engine) AcceptSpan(sp *chstore.Span) bool {
	if sp == nil {
		return true
	}
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()
	if len(rules) == 0 {
		return true
	}
	for i := range rules {
		r := &rules[i]
		if !r.Enabled {
			continue
		}
		if r.Signal != SignalSpans {
			continue
		}
		if !matchSpan(r.When, sp) {
			continue
		}
		switch r.Kind {
		case KindDrop:
			return false
		case KindSample:
			// v0.5.270 — probabilistic keep. math/rand/v2 is
			// lockless so the per-span overhead is a single
			// cmpxchg-free Float64() call. Rate < 0 already
			// rejected at Upsert; defensive clamp here mirrors
			// what the operator intent (kept rate 0 → drop).
			if r.Rate <= 0 || rand.Float64() >= r.Rate {
				return false
			}
		case KindEnrich:
			// v0.5.270 — set / override resource attributes
			// on the live span. Resource attrs (not span
			// attrs) is the right scope: enrichment usually
			// adds infra context (cluster, region, team)
			// that conceptually belongs to the SOURCE, not
			// the specific operation.
			applyEnrich(sp, r.SetAttributes)
		}
	}
	return true
}

// AcceptLog is the logs hot path — mirrors AcceptSpan exactly
// (v0.8.282). Called once per incoming log record before the
// consumer buffer. Returns false to drop; the caller bumps its
// logs-dropped-by-pipeline counter. Only rules whose Signal is
// SignalLogs are considered — spans/metrics rules are skipped so
// one shared rule catalog cleanly scopes per signal.
func (e *Engine) AcceptLog(l *chstore.Log) bool {
	if l == nil {
		return true
	}
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()
	if len(rules) == 0 {
		return true
	}
	for i := range rules {
		r := &rules[i]
		if !r.Enabled {
			continue
		}
		if r.Signal != SignalLogs {
			continue
		}
		if !matchLog(r.When, l) {
			continue
		}
		switch r.Kind {
		case KindDrop:
			return false
		case KindSample:
			if r.Rate <= 0 || rand.Float64() >= r.Rate {
				return false
			}
		case KindEnrich:
			applyEnrichAttrs(&l.ResKeys, &l.ResValues, r.SetAttributes)
		}
	}
	return true
}

// AcceptMetric is the metrics hot path — mirrors AcceptSpan
// (v0.8.282). Returns false to drop the data point. Drop is the
// dominant use case (silence a noisy debug gauge / drop a whole
// instrument for cost); enrich adds resource context. Sample is
// supported for symmetry but is a sharp tool on metrics —
// probabilistically discarding points corrupts cumulative sums,
// counts, and histogram buckets, so the operator opts in per rule
// knowing the aggregate becomes an estimate. Only SignalMetrics
// rules are considered.
func (e *Engine) AcceptMetric(m *chstore.MetricPoint) bool {
	if m == nil {
		return true
	}
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()
	if len(rules) == 0 {
		return true
	}
	for i := range rules {
		r := &rules[i]
		if !r.Enabled {
			continue
		}
		if r.Signal != SignalMetrics {
			continue
		}
		if !matchMetric(r.When, m) {
			continue
		}
		switch r.Kind {
		case KindDrop:
			return false
		case KindSample:
			if r.Rate <= 0 || rand.Float64() >= r.Rate {
				return false
			}
		case KindEnrich:
			applyEnrichAttrs(&m.ResKeys, &m.ResValues, r.SetAttributes)
		}
	}
	return true
}

// applyEnrich writes the given key/value pairs into the span's
// parallel-array resource attributes. Thin adapter over the
// signal-agnostic applyEnrichAttrs — the span, log, and metric
// records all carry the same ResKeys/ResValues layout so the
// enrich mechanism is shared (v0.8.282).
func applyEnrich(sp *chstore.Span, set map[string]string) {
	applyEnrichAttrs(&sp.ResKeys, &sp.ResValues, set)
}

// applyEnrichAttrs writes key/value pairs into a parallel-array
// attribute set (used for RESOURCE attributes on every signal).
// Existing keys are overridden in-place; new keys append. Mutates
// the slice headers via the pointers so the caller doesn't need
// to re-assign. Hot-path: no allocation when every key already
// exists (the common case after a brief warm-up) — the append
// only fires on the first record that seeds a new key.
func applyEnrichAttrs(keys, values *[]string, set map[string]string) {
	for k, v := range set {
		// Try in-place override first — the common case once
		// the slices have been seeded once.
		hit := false
		ks := *keys
		for i := 0; i < len(ks); i++ {
			if ks[i] == k {
				if i < len(*values) {
					(*values)[i] = v
				}
				hit = true
				break
			}
		}
		if !hit {
			*keys = append(*keys, k)
			*values = append(*values, v)
		}
	}
}

// matchSpan evaluates a Condition against a span. Well-known
// fields branch directly so the common case avoids a parallel-
// array lookup. Attribute / resource keys use the "attr.foo" /
// "resource.foo" prefixes — the same naming the FilterBuilder
// uses on the query side.
//
// Span attributes live as parallel AttrKeys / AttrValues slices
// (CH-friendly layout); the helper walks them linearly. Spans
// rarely carry > 20 attrs and the hot path runs once per span,
// so the O(n) scan is acceptable.
func matchSpan(c Condition, sp *chstore.Span) bool {
	var got string
	switch c.Key {
	case "service.name":
		got = sp.ServiceName
	case "name":
		got = sp.Name
	case "kind":
		got = sp.Kind
	case "status_code":
		got = sp.StatusCode
	default:
		if strings.HasPrefix(c.Key, "attr.") {
			got = lookupAttr(sp.AttrKeys, sp.AttrValues, strings.TrimPrefix(c.Key, "attr."))
		} else if strings.HasPrefix(c.Key, "resource.") {
			got = lookupAttr(sp.ResKeys, sp.ResValues, strings.TrimPrefix(c.Key, "resource."))
		} else {
			// Unprefixed unknown — fall back to span attributes
			// for the common "operator just typed http.route"
			// case without a prefix.
			got = lookupAttr(sp.AttrKeys, sp.AttrValues, c.Key)
		}
	}
	return matchOp(c.Op, got, c.Value)
}

// matchLog evaluates a Condition against a log record (v0.8.282).
// Well-known log fields branch directly; everything else routes
// through the attr. / resource. prefixes (unprefixed unknown falls
// back to log attributes, matching the span path's ergonomics).
func matchLog(c Condition, l *chstore.Log) bool {
	var got string
	switch c.Key {
	case "service.name":
		got = l.ServiceName
	case "severity_text":
		got = l.SeverityText
	case "severity_number":
		got = strconv.Itoa(int(l.SeverityNum))
	case "body":
		got = l.Body
	case "host.name":
		got = l.HostName
	case "trace_id":
		got = l.TraceID
	case "span_id":
		got = l.SpanID
	case "scope.name":
		got = l.ScopeName
	default:
		if strings.HasPrefix(c.Key, "attr.") {
			got = lookupAttr(l.AttrKeys, l.AttrValues, strings.TrimPrefix(c.Key, "attr."))
		} else if strings.HasPrefix(c.Key, "resource.") {
			got = lookupAttr(l.ResKeys, l.ResValues, strings.TrimPrefix(c.Key, "resource."))
		} else {
			got = lookupAttr(l.AttrKeys, l.AttrValues, c.Key)
		}
	}
	return matchOp(c.Op, got, c.Value)
}

// matchMetric evaluates a Condition against a metric data point
// (v0.8.282). "metric" (aliases "name" / "metric.name") targets the
// metric name; "instrument" (alias "type") the instrument kind;
// "unit" the unit string. Attribute / resource keys use the same
// prefixes as the span + log paths.
func matchMetric(c Condition, m *chstore.MetricPoint) bool {
	var got string
	switch c.Key {
	case "metric", "name", "metric.name":
		got = m.Metric
	case "instrument", "type":
		got = m.Instrument
	case "unit":
		got = m.Unit
	case "service.name":
		got = m.ServiceName
	case "host.name":
		got = m.HostName
	default:
		if strings.HasPrefix(c.Key, "attr.") {
			got = lookupAttr(m.AttrKeys, m.AttrValues, strings.TrimPrefix(c.Key, "attr."))
		} else if strings.HasPrefix(c.Key, "resource.") {
			got = lookupAttr(m.ResKeys, m.ResValues, strings.TrimPrefix(c.Key, "resource."))
		} else {
			got = lookupAttr(m.AttrKeys, m.AttrValues, c.Key)
		}
	}
	return matchOp(c.Op, got, c.Value)
}

func lookupAttr(keys, values []string, want string) string {
	for i := 0; i < len(keys) && i < len(values); i++ {
		if keys[i] == want {
			return values[i]
		}
	}
	return ""
}

func matchOp(op Op, got, want string) bool {
	switch op {
	case OpEq:
		return got == want
	case OpNeq:
		return got != want
	case OpContains:
		return strings.Contains(got, want)
	case OpStartsWith:
		return strings.HasPrefix(got, want)
	case OpEndsWith:
		return strings.HasSuffix(got, want)
	}
	return false
}

// normalise sorts the slice for deterministic persistence —
// `helm get values`-style diff readability + audit-log churn
// avoidance on no-op saves.
func normalise(rs []Rule) []Rule {
	// Simple by-name sort. The slice is small (<<100 rules per
	// install in practice); allocating a copy isn't worth the
	// optimisation.
	out := make([]Rule, len(rs))
	copy(out, rs)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Name > out[j].Name; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// uniqHash is a tiny non-crypto hash used to derive a stable ID
// when the operator submits a rule without one. FNV-1a 32-bit
// — collisions are theoretically possible but operator already
// hits "uniqueness" of names server-side so the risk is
// negligible.
func uniqHash(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// Log helper for the boot path — quick "loaded N rules" line
// without pulling the engine's lock through to main.go.
func (e *Engine) LogStats() {
	e.mu.RLock()
	n := len(e.rules)
	e.mu.RUnlock()
	log.Printf("[pipeline] loaded %d rule(s)", n)
}
