// Package watcher parses Elasticsearch Watcher definitions (the exact
// PUT _watcher/watch body) and projects them onto Coremetry alert
// rules — Faz-1 of the operator requirement "birebir aynı Watcher
// JSON'ı Coremetry çalışabilsin" (v0.9.x).
//
// Design contract:
//
//   - Field names mirror the ES Watcher schema verbatim. Anything we
//     don't interpret rides along as json.RawMessage — the FULL raw
//     JSON is stored on the rule (AlertRule.WatcherJSON) so the
//     definition round-trips byte-identical regardless of what this
//     package understands.
//   - Parse never rejects unknown fields; it records them so Validate
//     can report "preserved but ignored".
//   - Validate is a field-by-field mapping report
//     (Supported / Partial / Unsupported + reason), the honest
//     contract shown to the operator at import time.
//   - ToRule is the executable projection: metric="watcher",
//     comparator/threshold from condition.compare, window from the
//     schedule interval, cooldown from throttle_period. Watches whose
//     condition cannot be evaluated (script / never / non-hits.total
//     compare) or that have no search input import DISABLED — the
//     definition is kept, nothing silently fires wrong.
//
// Pure package: no store, no network. The evaluator executes the
// projected rules via logstore.RawSearch.
package watcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ── Schema (ES PUT _watcher/watch body, names verbatim) ─────────────────────

// Watch is the parsed ES watcher definition. Interpreted fields are
// typed; everything else is carried as raw JSON.
type Watch struct {
	Trigger   *Trigger          `json:"trigger,omitempty"`
	Input     *Input            `json:"input,omitempty"`
	Condition *Condition        `json:"condition,omitempty"`
	Actions   map[string]Action `json:"actions,omitempty"`
	// ThrottlePeriod is the watch-level action throttle ("5m", "30s",
	// "1d"); the millis twin is the alternative ES serialisation.
	ThrottlePeriod         string          `json:"throttle_period,omitempty"`
	ThrottlePeriodInMillis int64           `json:"throttle_period_in_millis,omitempty"`
	Metadata               json.RawMessage `json:"metadata,omitempty"`
	Transform              json.RawMessage `json:"transform,omitempty"`
	// Unknown lists top-level keys this package doesn't model
	// (sorted). They are preserved via the stored raw JSON and
	// surfaced on the Validate report.
	Unknown []string `json:"-"`
}

// Trigger — only the schedule trigger exists in ES today.
type Trigger struct {
	Schedule *Schedule `json:"schedule,omitempty"`
}

// Schedule is the parsed trigger.schedule. Interval keeps the ES
// string form ("5m"); a numeric interval (seconds) is normalised to
// "Ns". Cron carries the expression(s) — string or array in ES.
// Other lists further schedule kinds present (hourly / daily / …).
type Schedule struct {
	Interval string   `json:"interval,omitempty"`
	Cron     []string `json:"cron,omitempty"`
	Other    []string `json:"-"`
}

// HasTimezone — trigger.schedule bir timezone anahtarı taşıyor mu
// (ES 8.13+; UnmarshalJSON bilinmeyen anahtarları Other'a düşürür).
// v0.9.202: timezone'lu cron takvim yoluna ALINMAZ — UTC pacing ES'in
// tz-aware ateşlemesinden saatlerce kayar (kanıtlı 3h drift).
func (s *Schedule) HasTimezone() bool {
	for _, k := range s.Other {
		if strings.EqualFold(k, "timezone") {
			return true
		}
	}
	return false
}

// UnmarshalJSON tolerates the ES shapes: interval as string or
// number-of-seconds, cron as string or []string, plus any other
// schedule kind recorded by name.
func (s *Schedule) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, v := range m {
		switch k {
		case "interval":
			var str string
			if err := json.Unmarshal(v, &str); err == nil {
				s.Interval = str
				continue
			}
			var n float64
			if err := json.Unmarshal(v, &n); err == nil {
				s.Interval = strconv.FormatInt(int64(n), 10) + "s"
			}
		case "cron":
			s.Cron = stringOrList(v)
		default:
			s.Other = append(s.Other, k)
		}
	}
	sort.Strings(s.Other)
	return nil
}

// Input — search is the only kind Faz-1 executes; http / simple /
// chain are preserved raw and reported Unsupported.
type Input struct {
	Search *SearchInput    `json:"search,omitempty"`
	Simple json.RawMessage `json:"simple,omitempty"`
	HTTP   json.RawMessage `json:"http,omitempty"`
	Chain  json.RawMessage `json:"chain,omitempty"`
}

type SearchInput struct {
	Request SearchRequest   `json:"request"`
	Extract json.RawMessage `json:"extract,omitempty"`
	Timeout string          `json:"timeout,omitempty"`
}

// SearchRequest carries the ES search verbatim: indices + body pass
// straight through to logstore.RawSearch (which injects the cost
// guards). Template / indices_options ride raw.
type SearchRequest struct {
	Indices        indices         `json:"indices,omitempty"`
	Body           json.RawMessage `json:"body,omitempty"`
	Template       json.RawMessage `json:"template,omitempty"`
	SearchType     string          `json:"search_type,omitempty"`
	IndicesOptions json.RawMessage `json:"indices_options,omitempty"`
	// RestTotalHitsAsInt is a response-SHAPE flag (hits.total as a
	// bare int instead of {value, relation}); Coremetry issues the
	// search itself and reads hits.total.value directly, so this is
	// ignored by design — reported Supported, not scary-Partial.
	RestTotalHitsAsInt bool `json:"rest_total_hits_as_int,omitempty"`
}

// indices tolerates both ES serialisations: ["app-*"] and "app-*".
type indices []string

func (i *indices) UnmarshalJSON(b []byte) error {
	*i = stringOrList(b)
	return nil
}

// stringOrList decodes a JSON string or array-of-strings.
func stringOrList(b []byte) []string {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(b, &many); err == nil {
		return many
	}
	return nil
}

// Condition — compare on ctx.payload.hits.total is the executable
// subset; always maps to unconditional fire, never to a disabled
// rule; script / array_compare have no engine here.
type Condition struct {
	Compare      map[string]CompareClause `json:"compare,omitempty"`
	ArrayCompare json.RawMessage          `json:"array_compare,omitempty"`
	Script       json.RawMessage          `json:"script,omitempty"`
	Always       bool                     `json:"-"`
	Never        bool                     `json:"-"`
}

// CompareClause is one {op: value} object, e.g. {"gte": 100}.
type CompareClause map[string]any

// UnmarshalJSON handles the presence-typed always/never members
// ({"always": {}}) alongside the object-valued kinds.
func (c *Condition) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, v := range m {
		switch k {
		case "compare":
			if err := json.Unmarshal(v, &c.Compare); err != nil {
				return fmt.Errorf("condition.compare: %w", err)
			}
		case "array_compare":
			c.ArrayCompare = v
		case "script":
			c.Script = v
		case "always":
			c.Always = true
		case "never":
			c.Never = true
		}
	}
	return nil
}

// Action is one named entry under "actions". Kind payloads ride raw —
// Faz-1 never executes them (Coremetry's own notification channels
// fire on problem open/resolve); they're kept for round-trip + report.
type Action struct {
	Email   json.RawMessage `json:"email,omitempty"`
	Webhook json.RawMessage `json:"webhook,omitempty"`
	Slack   json.RawMessage `json:"slack,omitempty"`
	Logging json.RawMessage `json:"logging,omitempty"`
	Index   json.RawMessage `json:"index,omitempty"`
	// Per-action modifiers.
	ThrottlePeriod string          `json:"throttle_period,omitempty"`
	Condition      json.RawMessage `json:"condition,omitempty"`
	Transform      json.RawMessage `json:"transform,omitempty"`
	// Other lists action kinds this package doesn't model
	// (pagerduty, jira, …) — reported Unsupported.
	Other []string `json:"-"`
}

func (a *Action) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, v := range m {
		switch k {
		case "email":
			a.Email = v
		case "webhook":
			a.Webhook = v
		case "slack":
			a.Slack = v
		case "logging":
			a.Logging = v
		case "index":
			a.Index = v
		case "throttle_period":
			_ = json.Unmarshal(v, &a.ThrottlePeriod)
		case "throttle_period_in_millis":
			// normalise millis to the string form for the report
			var n int64
			if err := json.Unmarshal(v, &n); err == nil && n > 0 {
				a.ThrottlePeriod = strconv.FormatInt(n/1000, 10) + "s"
			}
		case "condition":
			a.Condition = v
		case "transform":
			a.Transform = v
		case "foreach", "max_iterations":
			a.Other = append(a.Other, k)
		default:
			a.Other = append(a.Other, k)
		}
	}
	sort.Strings(a.Other)
	return nil
}

// ── Mapping report ──────────────────────────────────────────────────────────

// Support classifies how faithfully one watcher field maps onto
// Coremetry's evaluator.
type Support string

const (
	Supported   Support = "supported"
	Partial     Support = "partial"
	Unsupported Support = "unsupported"
)

// Finding is one field's verdict with the reason an operator reads at
// import time.
type Finding struct {
	Field  string  `json:"field"`
	Status Support `json:"status"`
	Reason string  `json:"reason"`
}

// Report is the whole-watch mapping verdict. Disabled means the
// projected rule cannot (or must not) run and imports with
// Enabled=false; the definition itself is always kept.
type Report struct {
	Findings       []Finding `json:"findings"`
	Disabled       bool      `json:"disabled"`
	DisabledReason string    `json:"disabledReason,omitempty"`
}

func (r *Report) add(field string, st Support, reason string) {
	r.Findings = append(r.Findings, Finding{Field: field, Status: st, Reason: reason})
}

// disable marks the rule non-runnable; the first reason wins.
func (r *Report) disable(reason string) {
	if !r.Disabled {
		r.Disabled = true
		r.DisabledReason = reason
	}
}

// HasUnsupported reports whether any finding is Unsupported.
func (r Report) HasUnsupported() bool {
	for _, f := range r.Findings {
		if f.Status == Unsupported {
			return true
		}
	}
	return false
}

// ── Parse ───────────────────────────────────────────────────────────────────

// Parse decodes an ES watcher body. Unknown top-level fields never
// fail the parse — they land in Watch.Unknown and the stored raw JSON
// preserves them byte-for-byte for round-trip.
func Parse(raw []byte) (*Watch, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("watcher JSON: %w", err)
	}
	if top == nil {
		return nil, fmt.Errorf("watcher JSON: empty document")
	}
	w := &Watch{}
	for k, v := range top {
		var err error
		switch k {
		case "trigger":
			err = json.Unmarshal(v, &w.Trigger)
		case "input":
			err = json.Unmarshal(v, &w.Input)
		case "condition":
			err = json.Unmarshal(v, &w.Condition)
		case "actions":
			err = json.Unmarshal(v, &w.Actions)
		case "throttle_period":
			err = json.Unmarshal(v, &w.ThrottlePeriod)
		case "throttle_period_in_millis":
			err = json.Unmarshal(v, &w.ThrottlePeriodInMillis)
		case "metadata":
			w.Metadata = v
		case "transform":
			w.Transform = v
		default:
			w.Unknown = append(w.Unknown, k)
		}
		if err != nil {
			return nil, fmt.Errorf("watcher field %q: %w", k, err)
		}
	}
	sort.Strings(w.Unknown)
	return w, nil
}

// ── Validate ────────────────────────────────────────────────────────────────

// compareOps maps ES compare operators onto the evaluator's
// comparator set (internal/evaluator compare(): > >= < <=). eq /
// not_eq are deliberately ABSENT: exact-equality alerting is not
// expressible there, and approximating it with a band would silently
// change semantics — such watches import disabled with a report.
var compareOps = map[string]string{
	"gte": ">=",
	"gt":  ">",
	"lte": "<=",
	"lt":  "<",
}

// hitsTotalPaths are the compare paths Faz-1 evaluates: the search's
// total hit count (ES 7+ nests it as total.value; watcher payloads
// expose both spellings).
var hitsTotalPaths = map[string]bool{
	"ctx.payload.hits.total":       true,
	"ctx.payload.hits.total.value": true,
}

// Faz-2 (v0.9.x): compare / array_compare paths under the search
// response's aggregations are also executable — the evaluator fetches
// the payload via logstore.RawSearchPayload and walks the dotted path.
const (
	payloadPathPrefix = "ctx.payload."
	aggPathPrefix     = "ctx.payload.aggregations."
)

func isAggPath(path string) bool {
	return strings.HasPrefix(path, aggPathPrefix) && len(path) > len(aggPathPrefix)
}

// ── Executable condition projection (Faz-2) ─────────────────────────────────

// CondKind classifies how the evaluator obtains the value it compares.
type CondKind int

const (
	// CondAlways — no condition / always / empty compare: fires
	// whenever the search runs (hits.total >= 0).
	CondAlways CondKind = iota
	// CondHitsTotal — compare on ctx.payload.hits.total(.value).
	CondHitsTotal
	// CondAggValue — compare on a ctx.payload.aggregations.… path.
	CondAggValue
	// CondArrayCompare — array_compare over an aggregations array:
	// fires when ANY element matches (value = MAX of the matches).
	CondArrayCompare
)

// ConditionSpec is the executable projection of the watch condition.
// Path/ArrayPath come back PAYLOAD-RELATIVE (the "ctx.payload."
// prefix stripped) so the evaluator can walk the RawSearchPayload
// result — {"hits":{"total":{"value":N}},"aggregations":{…}} —
// directly. Comparator is in the evaluator's set (> >= < <=).
type ConditionSpec struct {
	Kind       CondKind
	Path       string // CondAggValue: dotted payload path
	ArrayPath  string // CondArrayCompare: path to the array
	ItemPath   string // CondArrayCompare: per-element path ("" = the element itself)
	Comparator string
	Threshold  float64
}

// ConditionSpec projects the condition onto the executable subset.
// ok=false means the condition cannot run (script / never /
// unsupported path / non-numeric value…) — Validate carries the
// operator-facing reason; this is the machine-facing twin.
func (w *Watch) ConditionSpec() (ConditionSpec, bool) {
	if w == nil {
		return ConditionSpec{}, false
	}
	c := w.Condition
	if c == nil {
		// ES semantics: no condition = always.
		return ConditionSpec{Kind: CondAlways, Comparator: ">=", Threshold: 0}, true
	}
	if len(c.Script) > 0 || c.Never {
		return ConditionSpec{}, false
	}
	if len(c.ArrayCompare) > 0 {
		spec, err := parseArrayCompare(c.ArrayCompare)
		if err != nil {
			return ConditionSpec{}, false
		}
		return spec, true
	}
	if c.Always || len(c.Compare) == 0 {
		return ConditionSpec{Kind: CondAlways, Comparator: ">=", Threshold: 0}, true
	}
	if len(c.Compare) > 1 {
		return ConditionSpec{}, false
	}
	for path, clause := range c.Compare {
		if len(clause) != 1 {
			return ConditionSpec{}, false
		}
		for op, val := range clause {
			cmp, ok := compareOps[op]
			if !ok {
				return ConditionSpec{}, false
			}
			n, isNum := val.(float64)
			if !isNum {
				return ConditionSpec{}, false
			}
			switch {
			case hitsTotalPaths[path]:
				return ConditionSpec{Kind: CondHitsTotal, Comparator: cmp, Threshold: n}, true
			case isAggPath(path):
				return ConditionSpec{
					Kind:       CondAggValue,
					Path:       strings.TrimPrefix(path, payloadPathPrefix),
					Comparator: cmp,
					Threshold:  n,
				}, true
			}
		}
	}
	return ConditionSpec{}, false
}

// parseArrayCompare decodes the ES condition.array_compare shape in
// the Faz-2 subset:
//
//	{"ctx.payload.aggregations.X.buckets": {
//	    "path": "doc_count",
//	    "gte":  {"value": N, "quantifier": "some"}}}
//
// Exactly one array path (must be under ctx.payload.aggregations.),
// exactly one operator from the compare set, a plain-numeric value,
// and quantifier absent/"some" (ANY element matching fires — "all"
// has different semantics and stays unsupported). The error text is
// the Unsupported reason shown on the Validate report.
func parseArrayCompare(raw json.RawMessage) (ConditionSpec, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ConditionSpec{}, fmt.Errorf("array_compare: %w", err)
	}
	if len(top) != 1 {
		return ConditionSpec{}, fmt.Errorf("array_compare needs exactly one array path, got %d", len(top))
	}
	for arrayPath, clauseRaw := range top {
		if !isAggPath(arrayPath) {
			return ConditionSpec{}, fmt.Errorf("array_compare path %q is not under ctx.payload.aggregations.", arrayPath)
		}
		var clause map[string]json.RawMessage
		if err := json.Unmarshal(clauseRaw, &clause); err != nil {
			return ConditionSpec{}, fmt.Errorf("array_compare clause: %w", err)
		}
		spec := ConditionSpec{
			Kind:      CondArrayCompare,
			ArrayPath: strings.TrimPrefix(arrayPath, payloadPathPrefix),
		}
		ops := 0
		for k, v := range clause {
			if k == "path" {
				if err := json.Unmarshal(v, &spec.ItemPath); err != nil {
					return ConditionSpec{}, fmt.Errorf("array_compare path field: %w", err)
				}
				continue
			}
			cmp, ok := compareOps[k]
			if !ok {
				return ConditionSpec{}, fmt.Errorf("array_compare operator %q has no equivalent (supported: gte gt lte lt)", k)
			}
			ops++
			if ops > 1 {
				return ConditionSpec{}, fmt.Errorf("array_compare supports exactly one comparison operator per clause")
			}
			var body struct {
				Value      any    `json:"value"`
				Quantifier string `json:"quantifier"`
			}
			if err := json.Unmarshal(v, &body); err != nil {
				return ConditionSpec{}, fmt.Errorf("array_compare %s: %w", k, err)
			}
			n, isNum := body.Value.(float64)
			if !isNum {
				return ConditionSpec{}, fmt.Errorf("array_compare value %v is not a plain number (mustache/date-math not evaluated)", body.Value)
			}
			if body.Quantifier != "" && body.Quantifier != "some" {
				return ConditionSpec{}, fmt.Errorf("array_compare quantifier %q not supported (only some — any element matching fires)", body.Quantifier)
			}
			spec.Comparator, spec.Threshold = cmp, n
		}
		if ops == 0 {
			return ConditionSpec{}, fmt.Errorf("array_compare clause has no comparison operator")
		}
		return spec, nil
	}
	return ConditionSpec{}, fmt.Errorf("array_compare: empty clause") // unreachable, len check above
}

// Validate produces the field-by-field mapping report for a parsed
// watch. It never mutates the watch.
func Validate(w *Watch) Report {
	var rep Report

	// trigger.schedule
	switch {
	case w.Trigger == nil || w.Trigger.Schedule == nil:
		rep.add("trigger.schedule", Partial,
			"watch has no schedule; Coremetry evaluates on its own cadence with a 5m default window")
	default:
		s := w.Trigger.Schedule
		if s.Interval != "" {
			if d, err := parseESDuration(s.Interval); err != nil {
				rep.add("trigger.schedule.interval", Partial,
					fmt.Sprintf("interval %q not parseable; falling back to the 5m default window", s.Interval))
			} else if d < time.Minute {
				rep.add("trigger.schedule.interval", Partial,
					fmt.Sprintf("interval %s clamped to 1m — Coremetry's evaluator tick floor", s.Interval))
			} else {
				rep.add("trigger.schedule.interval", Supported,
					fmt.Sprintf("evaluated every %s (window = interval)", s.Interval))
			}
		}
		if len(s.Cron) > 0 {
			// v0.9.x — real prod watches schedule via Quartz cron far
			// more often than via interval. The fixed-rate subset
			// ("every N minutes/hours") maps LOSSLESSLY onto an
			// interval, so those import Supported; anything with a
			// calendar shape (specific hour, day-of-week…) stays
			// Unsupported.
			if sec := cronIntervalSec(s.Cron); sec > 0 {
				rep.add("trigger.schedule.cron", Supported,
					fmt.Sprintf("cron %q maps to a fixed %s interval (cron→interval eşlendi); evaluated every %s",
						s.Cron[0], time.Duration(sec)*time.Second, time.Duration(sec)*time.Second))
			} else if parseableCrons(s.Cron) {
				// Faz-2: calendar-shaped crons in the ParseCron subset
				// run on their OWN schedule (next-fire due check, UTC —
				// ES Watcher parity). v0.9.202 review-fix: verdict,
				// CalendarCron'un GERÇEK kapılarıyla birebir — timezone'lu
				// ya da saniye-bazlı cron takvim yoluna girmez, rapor da
				// Supported DEMEZ (kendi-içinde-çelişik rapor sınıfı).
				switch {
				case s.HasTimezone():
					rep.add("trigger.schedule.cron", Unsupported,
						"cron timezone modellenmedi — UTC'de koşturmak ES'in tz-aware ateşlemesinden saatlerce kayardı; kural sürekli (5m default) değerlendirilir. UTC'ye çevrilmiş bir cron ile yeniden import edin")
				case !cronSecondsZero(s.Cron):
					rep.add("trigger.schedule.cron", Unsupported,
						"saniye-bazlı cron (saniye alanı ≠ 0) 1dk değerlendirme tick'iyle temsil edilemez; kural sürekli (5m default) değerlendirilir")
				default:
					rep.add("trigger.schedule.cron", Supported,
						fmt.Sprintf("takvim cron %q — kendi zamanlamasında değerlendirilir (next-fire due check, UTC)",
							strings.Join(s.Cron, " | ")))
				}
			} else {
				rep.add("trigger.schedule.cron", Unsupported,
					"cron expression outside the supported Quartz subset (L/W/# calendar specials are not modelled); the rule evaluates continuously (5m default window) instead")
			}
		}
		for _, k := range s.Other {
			if strings.EqualFold(k, "timezone") {
				// v0.9.202 — doğru metin: timezone cron'u sürekli-moda düşürür
				// (yukarıdaki cron verdict'iyle tutarlı), "yalnız interval" değil.
				rep.add("trigger.schedule.timezone", Unsupported,
					"cron timezones are not modelled; the rule evaluates continuously (5m default) instead")
				continue
			}
			rep.add("trigger.schedule."+k, Unsupported,
				"only interval schedules run in Faz-1; the rule evaluates continuously instead")
		}
		if s.Interval == "" && len(s.Cron) == 0 && len(s.Other) == 0 {
			rep.add("trigger.schedule", Partial,
				"empty schedule; Coremetry evaluates with a 5m default window")
		}
	}

	// input
	switch {
	case w.Input == nil:
		rep.add("input", Unsupported, "watch has no input; nothing to execute")
		rep.disable("no input.search — Coremetry has no search to run")
	case w.Input.Search != nil:
		body := bytes.TrimSpace(w.Input.Search.Request.Body)
		switch {
		case len(body) == 0:
			// Review F10 (v0.9.x): a template-driven (or body-less)
			// search has nothing executable — the old Supported verdict
			// let it import enabled and count ALL docs over 24h via the
			// injected match-all fallback (false Problem every run).
			if len(w.Input.Search.Request.Template) > 0 {
				rep.add("input.search.request.template", Unsupported,
					"search templates are not rendered in Faz-1 — there is no executable request.body; the rule imports disabled")
			} else {
				rep.add("input.search.request.body", Unsupported,
					"input.search has no request.body — nothing executable; the rule imports disabled")
			}
			rep.disable("input.search has no executable request.body")
		case bytes.Contains(body, []byte("{{")):
			// Review F12 (v0.9.x): ES renders the body as a mustache
			// template before searching; Coremetry ships it verbatim,
			// so an unrendered {{ctx…}} placeholder 400s on every run —
			// a permanently dead rule the report used to call
			// Supported. Honest verdict: disabled with the reason.
			// (Mustache in ACTIONS is fine — actions never execute.)
			rep.add("input.search.request.body", Unsupported,
				"mustache templating ({{ctx…}}) in the search body is not rendered in Faz-1 — the literal placeholder would fail on every run; the rule imports disabled")
			rep.disable("mustache templating in the search body is not rendered in Faz-1")
		default:
			rep.add("input.search", Supported,
				"indices + body pass through verbatim to the configured log backend (cost guards injected: size:0, capped track_total_hits, timeout, 24h range fallback on the configured logs timestamp field when the body carries no range)")
		}
		// Request-level execution tuning flags don't affect Coremetry —
		// it issues the search itself — so they're Supported/"ignored
		// by design", not a scary Partial (review improvement c).
		if w.Input.Search.Request.SearchType != "" {
			rep.add("input.search.request.search_type", Supported,
				fmt.Sprintf("%q ignored by design — Coremetry issues the search itself; search_type only tunes ES-internal execution and cannot change the hit count", w.Input.Search.Request.SearchType))
		}
		if w.Input.Search.Request.RestTotalHitsAsInt {
			rep.add("input.search.request.rest_total_hits_as_int", Supported,
				"ignored by design — response-shape flag; Coremetry reads hits.total.value directly")
		}
	default:
		for field, present := range map[string]bool{
			"input.http":   w.Input.HTTP != nil,
			"input.simple": w.Input.Simple != nil,
			"input.chain":  w.Input.Chain != nil,
		} {
			if present {
				rep.add(field, Unsupported, "only input.search runs in Faz-1")
			}
		}
		rep.disable("no input.search — Coremetry has no search to run")
	}

	// condition
	validateCondition(w.Condition, &rep)

	// actions — Faz-1 never auto-creates channels; Coremetry's own
	// notification channels fire when the projected rule opens or
	// resolves a Problem.
	actionIDs := make([]string, 0, len(w.Actions))
	for id := range w.Actions {
		actionIDs = append(actionIDs, id)
	}
	sort.Strings(actionIDs)
	for _, id := range actionIDs {
		a := w.Actions[id]
		for kind, payload := range map[string]json.RawMessage{
			"email":   a.Email,
			"webhook": a.Webhook,
			"slack":   a.Slack,
			"logging": a.Logging,
		} {
			if payload != nil {
				reason := "Faz-1 fires your existing Coremetry notification channels on problem open/resolve; the watch's own targets are not auto-created"
				// Mustache in action payloads is harmless — Coremetry
				// never executes actions — so it gets a reassuring
				// Supported note instead of the body-mustache disable.
				if bytes.Contains(payload, []byte("{{")) {
					reason += " (mustache placeholders in the action payload are fine — actions are never executed, the definition is preserved verbatim)"
				}
				rep.add("actions."+id+"."+kind, Supported, reason)
			}
		}
		if a.Index != nil {
			rep.add("actions."+id+".index", Unsupported,
				"index action needs ES write access — prod credentials are read-only; no write-back in Faz-1")
		}
		for _, kind := range a.Other {
			rep.add("actions."+id+"."+kind, Unsupported,
				"action kind not modelled in Faz-1; preserved in the raw definition")
		}
		if a.ThrottlePeriod != "" {
			rep.add("actions."+id+".throttle_period", Partial,
				"per-action throttle is not modelled; the watch-level throttle_period maps to the rule cooldown")
		}
	}

	// throttle_period → cooldown
	if w.ThrottlePeriod != "" || w.ThrottlePeriodInMillis > 0 {
		if sec := throttleSec(w); sec > 0 {
			rep.add("throttle_period", Supported,
				fmt.Sprintf("maps to the rule cooldown (%ds) — suppresses re-opens after a resolve", sec))
		} else {
			rep.add("throttle_period", Partial,
				fmt.Sprintf("throttle %q not parseable; no cooldown applied", w.ThrottlePeriod))
		}
	}

	// transform — shapes action payloads only; the count compare
	// doesn't need it. Reported, not disabling.
	if len(w.Transform) > 0 {
		rep.add("transform", Unsupported,
			"transforms are ignored — Coremetry compares hits.total directly (no Painless engine)")
	}

	// unknown top-level fields — preserved via raw JSON.
	for _, k := range w.Unknown {
		rep.add(k, Partial, "field not modelled; preserved verbatim in the stored definition")
	}

	return rep
}

// validateCondition applies the condition subset rules. nil condition
// is ES semantics for "always".
func validateCondition(c *Condition, rep *Report) {
	if c == nil {
		rep.add("condition", Supported,
			"no condition = ES always; mapped to hits.total >= 0 (fires whenever the search runs)")
		return
	}
	// script has no engine — the rule cannot run.
	if len(c.Script) > 0 {
		rep.add("condition.script", Unsupported,
			"no Painless engine in Coremetry; the rule imports disabled")
		rep.disable("script condition cannot be evaluated")
		return
	}
	// array_compare (Faz-2): the ES bucket-compare shape is executable
	// against the search's aggregations payload; anything outside the
	// subset keeps the Faz-1 disable.
	if len(c.ArrayCompare) > 0 {
		spec, err := parseArrayCompare(c.ArrayCompare)
		if err != nil {
			rep.add("condition.array_compare", Unsupported,
				fmt.Sprintf("array_compare outside the executable subset (%v) — supported shape: one ctx.payload.aggregations.… array path, path field, one of gte/gt/lte/lt with a numeric value, quantifier some; the rule imports disabled", err))
			rep.disable("array_compare condition outside the supported subset")
			return
		}
		item := spec.ItemPath
		if item == "" {
			item = "the element itself"
		}
		rep.add("condition.array_compare", Supported,
			fmt.Sprintf("fires when ANY element of %s matches %s %s %g — the Problem value is the MAX of the matching elements", spec.ArrayPath, item, spec.Comparator, spec.Threshold))
		return
	}
	if c.Never {
		rep.add("condition.never", Supported,
			"never never fires — imports as a DISABLED rule (exact semantics preserved)")
		rep.disable("condition.never — the watch never fires by definition")
		return
	}
	if c.Always || len(c.Compare) == 0 {
		rep.add("condition.always", Supported,
			"unconditional fire mapped to threshold 0 + >= on hits.total")
		return
	}
	// compare — exactly one clause on the hits.total path with one
	// supported operator and a numeric value.
	if len(c.Compare) > 1 {
		rep.add("condition.compare", Unsupported,
			"multiple compare paths in one watch are not expressible as a single rule; imports disabled")
		rep.disable("multiple compare paths")
		return
	}
	for path, clause := range c.Compare {
		if !hitsTotalPaths[path] && !isAggPath(path) {
			rep.add("condition.compare", Unsupported,
				fmt.Sprintf("only the ctx.payload.hits.total and ctx.payload.aggregations.… paths are evaluated (got %q); imports disabled", path))
			rep.disable("compare path is not hits.total or an aggregation value")
			return
		}
		if len(clause) != 1 {
			rep.add("condition.compare", Unsupported,
				"exactly one comparison operator per clause is supported; imports disabled")
			rep.disable("compare clause has multiple operators")
			return
		}
		for op, val := range clause {
			cmp, ok := compareOps[op]
			if !ok {
				rep.add("condition.compare", Unsupported,
					fmt.Sprintf("operator %q has no equivalent — the evaluator comparator set is >, >=, <, <=; imports disabled", op))
				rep.disable(fmt.Sprintf("compare operator %q not supported", op))
				return
			}
			if _, isNum := val.(float64); !isNum {
				rep.add("condition.compare", Unsupported,
					fmt.Sprintf("compare value %v is not a plain number (mustache/date-math not evaluated); imports disabled", val))
				rep.disable("compare value is not numeric")
				return
			}
			if hitsTotalPaths[path] {
				rep.add("condition.compare", Supported,
					fmt.Sprintf("hits.total %s threshold — evaluated via a guarded count search", cmp))
			} else {
				// Faz-2: aggregation value compare.
				rep.add("condition.compare", Supported,
					fmt.Sprintf("aggregation value %s %s threshold — read from the guarded search's aggregations payload", path, cmp))
			}
		}
	}
}

// ── ToRule ──────────────────────────────────────────────────────────────────

// defaultWindowSec is the window when the watch has no (parseable)
// interval schedule — matches the built-in rules' 5m grain.
const defaultWindowSec = 300

// ToRule projects a parsed watch onto an executable alert rule and
// returns the mapping report alongside. The caller (Faz-2 import API)
// assigns ID and, when metadata.name is absent, Name. raw is stored
// verbatim on the rule for round-trip + re-evaluation.
func ToRule(w *Watch, raw []byte) (chstore.AlertRule, Report, error) {
	if w == nil {
		return chstore.AlertRule{}, Report{}, fmt.Errorf("watcher: nil watch")
	}
	if len(raw) == 0 {
		return chstore.AlertRule{}, Report{}, fmt.Errorf("watcher: empty raw definition")
	}
	rep := Validate(w)

	r := chstore.AlertRule{
		Name:        metadataName(w.Metadata),
		Service:     "",
		Metric:      "watcher",
		Severity:    "warning",
		WindowSec:   defaultWindowSec,
		CooldownSec: throttleSec(w),
		Enabled:     !rep.Disabled,
		WatcherJSON: string(raw),
	}
	if sec := w.IntervalSec(); sec > 0 {
		r.WindowSec = sec
	}

	// Comparator + threshold. always / no-condition → ">= 0"
	// (unconditional fire when the search runs). Faz-2: agg-value and
	// array_compare conditions project their comparator/threshold the
	// same way (Metric stays "watcher"). A disabled rule keeps
	// whatever could be mapped so a later manual enable behaves
	// predictably (the report already told the operator what's off).
	r.Comparator, r.Threshold = ">=", 0
	if spec, ok := w.ConditionSpec(); ok {
		r.Comparator, r.Threshold = spec.Comparator, spec.Threshold
	}
	return r, rep, nil
}

// IntervalSec returns the schedule interval in seconds, clamped to
// Coremetry's 1m evaluator tick floor. A fixed-rate Quartz cron
// (cron→interval subset, v0.9.x) counts as an interval schedule.
// 0 = no parseable interval schedule (caller applies the 5m default).
func (w *Watch) IntervalSec() uint32 {
	if w == nil || w.Trigger == nil || w.Trigger.Schedule == nil {
		return 0
	}
	s := w.Trigger.Schedule
	if s.Interval != "" {
		d, err := parseESDuration(s.Interval)
		if err != nil {
			return 0
		}
		if d < time.Minute {
			d = time.Minute
		}
		return uint32(d / time.Second)
	}
	return cronIntervalSec(s.Cron)
}

// cronIntervalSec maps the fixed-rate Quartz cron subset real prod
// watches use onto seconds (v0.9.x — cron→interval):
//
//	0 0/N * * * ?   every N minutes   (also */N)
//	0 0   * * * ?   hourly
//	0 0 0/N * * ?   every N hours     (also */N)
//
// Quartz field order: sec min hour day-of-month month day-of-week
// [year]. Anything with a calendar shape (nonzero seconds, a specific
// hour/day/month, a constrained year, or multiple expressions) is NOT
// a fixed rate and returns 0 — the caller keeps it Unsupported.
func cronIntervalSec(exprs []string) uint32 {
	if len(exprs) != 1 {
		return 0
	}
	f := strings.Fields(strings.TrimSpace(exprs[0]))
	if len(f) != 6 && len(f) != 7 {
		return 0
	}
	if len(f) == 7 && f[6] != "*" && f[6] != "?" {
		return 0 // constrained year — calendar semantics
	}
	anyOf := func(s string) bool { return s == "*" || s == "?" }
	// "0/N" or "*/N" → N; both start at 0 in Quartz, so they are the
	// same fixed rate.
	everyN := func(s string) (int, bool) {
		base, step, ok := strings.Cut(s, "/")
		if !ok || (base != "0" && base != "*") {
			return 0, false
		}
		n, err := strconv.Atoi(step)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	}
	sec, min, hour := f[0], f[1], f[2]
	if sec != "0" || f[4] != "*" || !anyOf(f[3]) || !anyOf(f[5]) {
		return 0
	}
	switch {
	case anyOf(hour):
		if n, ok := everyN(min); ok {
			return uint32(n) * 60 // every N minutes
		}
		if min == "0" {
			return 3600 // hourly
		}
	case min == "0":
		if n, ok := everyN(hour); ok {
			return uint32(n) * 3600 // every N hours
		}
	}
	return 0
}

// NormalizeDevTools converts Kibana DevTools' triple-quoted string
// syntax ("""…raw body…""" — not valid JSON) into properly escaped
// JSON strings, so an operator can paste a watch straight out of the
// DevTools console (v0.9.x — review improvement b). Input without
// `"""` is returned UNCHANGED (byte-identical — the verbatim-storage
// contract holds); the import path stores the NORMALIZED form, since
// the evaluator re-parses the stored definition as JSON on every run.
// An unterminated `"""` is left as-is for Parse to reject with a
// pointer.
func NormalizeDevTools(raw []byte) []byte {
	tq := []byte(`"""`)
	if !bytes.Contains(raw, tq) {
		return raw
	}
	var out bytes.Buffer
	inStr := false
	i := 0
	for i < len(raw) {
		c := raw[i]
		if inStr {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(raw) {
				out.WriteByte(raw[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' && bytes.HasPrefix(raw[i:], tq) {
			end := bytes.Index(raw[i+3:], tq)
			if end < 0 {
				out.Write(raw[i:])
				return out.Bytes()
			}
			enc, err := json.Marshal(string(raw[i+3 : i+3+end]))
			if err != nil { // unreachable for a Go string; belt+braces
				out.Write(raw[i:])
				return out.Bytes()
			}
			out.Write(enc)
			i += 3 + end + 3
			continue
		}
		if c == '"' {
			inStr = true
		}
		out.WriteByte(c)
		i++
	}
	return out.Bytes()
}

// throttleSec resolves the watch-level throttle to cooldown seconds
// (string form wins over millis; unparseable → 0).
func throttleSec(w *Watch) uint32 {
	if w.ThrottlePeriod != "" {
		if d, err := parseESDuration(w.ThrottlePeriod); err == nil {
			return uint32(d / time.Second)
		}
		return 0
	}
	if w.ThrottlePeriodInMillis > 0 {
		return uint32(w.ThrottlePeriodInMillis / 1000)
	}
	return 0
}

// metadataName pulls metadata.name when it's a plain string.
func metadataName(meta json.RawMessage) string {
	if len(meta) == 0 {
		return ""
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Name)
}

// parseESDuration parses the ES time-unit strings watches use.
// time.ParseDuration covers ms/s/m/h (and compounds like "1h30m");
// ES additionally allows "d" for days, which Go rejects — handled
// here. Negative or empty durations error.
func parseESDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// "Nd" — days suffix with an integer count (ES has no compound
	// day forms like "1d12h" in throttle/interval usage).
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return d, nil
}
