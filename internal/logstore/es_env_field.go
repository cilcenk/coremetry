package logstore

// Deployment-environment field SELF-DISCOVERY against the configured
// Elasticsearch (v0.8.400 — env-separation Phase 4, the trace-context
// pattern of es_trace_context.go extended to ?env=).
//
// The audit's open question 5 ("which env field do prod ES logs carry?")
// stays unanswerable from here — so instead of asking the operator, the
// system inspects its OWN cluster: one field_caps over the candidate
// shapes, pick the first aggregatable keyword-capable candidate that
// EXISTS, log the pick. An operator-configured fields.Env skips
// discovery entirely (explicit override wins, the ESFieldMap contract).
//
// Cost guards (ES-cost discipline, twice-stated operator constraint):
// field_caps is metadata-only under a 3s ctx and the verdict — positive
// OR negative — is CACHED for envFieldTTL (10 min) per store instance,
// so steady-state load is ≤1 field_caps per 10 min per backend, never
// per request. A Settings-driven backend swap rebuilds the ESStore and
// naturally re-discovers.
//
// When NO candidate resolves, the env filter is deliberately NOT
// emitted (a term on a missing/text-only field matches nothing — the
// pivot audit's silent-killer class) and Search reports
// Page.EnvUnapplied so the /logs page shows an honest "filter not
// applied" chip instead of a silently-unfiltered view (the v0.8.398
// honesty pattern).

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// envFieldTTL bounds how long one discovery verdict (positive or
	// negative) is trusted. 10 min: mappings change on index rollover /
	// pipeline deploys, not per request; a wrong negative heals within
	// one rollover-scale interval without per-request field_caps.
	envFieldTTL = 10 * time.Minute
	// envFieldCapsTimeout — same budget as the trace-context probe:
	// field_caps touches mapping metadata only.
	envFieldCapsTimeout = 3 * time.Second
)

// envFieldCandidates — the deployment-environment field shapes to probe,
// in verdict priority order. Rationale per candidate:
//
//	resource.deployment.environment.name  — OTel ES exporter (ECS/OTel
//	                                         mode) nests resource attrs
//	                                         under `resource.`; the NEW
//	                                         semconv key (v0.8.379 class)
//	resource.deployment.environment       — same nesting, legacy key
//	deployment.environment.name           — flattened new semconv key
//	                                         (collector attrs-to-top
//	                                         pipelines, Filebeat decode)
//	deployment.environment                — flattened legacy key
//	resource.attributes.deployment.environment.name — OTel exporter in
//	                                         raw (non-ECS) mode keeps the
//	                                         attributes sub-object
//	labels.deployment_environment         — prometheus/loki-style label
//	                                         normalisation (dots → _)
//	env / environment                      — bare custom-pipeline shapes,
//	                                         last because they're the
//	                                         most collision-prone
//
// New-semconv spelling ranks above legacy at equal nesting — mirrors
// the ingest fallback chain (v0.8.379) and topoEnvChainSQL (v0.8.380).
func envFieldCandidates() []string {
	return []string{
		"resource.deployment.environment.name",
		"resource.deployment.environment",
		"deployment.environment.name",
		"deployment.environment",
		"resource.attributes.deployment.environment.name",
		"labels.deployment_environment",
		"env",
		"environment",
	}
}

// envFieldCapsFields expands the candidates with their .keyword
// variants for the single field_caps probe (a text+keyword multi-field
// is keyword-capable through its subfield).
func envFieldCapsFields(candidates []string) []string {
	out := make([]string, 0, len(candidates)*2)
	for _, c := range candidates {
		out = append(out, c, c+".keyword")
	}
	return out
}

// resolveEnvFieldFromCaps is the VERDICT rule (pure, table-tested —
// es_env_field_test.go): walk candidates in priority order and return
// the first one that EXISTS in the mapping and is keyword-capable —
// either the bare field is keyword-mapped (aggregatable term target)
// or its .keyword subfield is. Returns the BASE candidate name; the
// query side matches both shapes via exactTermsBothShapes, consistent
// with the service/cluster filters. A text-only candidate (no keyword
// path) is SKIPPED: a term clause against an analyzed field can't
// match a hyphenated env value — the silent-killer class the
// trace-context diagnoser exists for.
func resolveEnvFieldFromCaps(candidates []string, caps map[string]traceFieldCap) (string, bool) {
	keywordCapable := func(c traceFieldCap) bool {
		for _, t := range c.Types {
			if t == "keyword" {
				return c.Aggregatable
			}
		}
		return false
	}
	for _, name := range candidates {
		if keywordCapable(caps[name]) || keywordCapable(caps[name+".keyword"]) {
			return name, true
		}
	}
	return "", false
}

// esEnvFieldCache is the per-store memo for the discovery verdict.
// Positive AND negative verdicts are cached for the same TTL — a
// missing field is just as stable as a present one, and re-probing a
// negative per request is exactly the ES-cost shape this cache bans.
type esEnvFieldCache struct {
	mu      sync.Mutex
	field   string // resolved base field; "" when unresolved
	ok      bool
	expires time.Time
}

// envFilterField returns the ES field the env term filter should
// target, resolving (and caching) via field_caps when the operator
// hasn't configured fields.Env. ok=false ⇒ the filter cannot be
// applied on this backend (caller reports Page.EnvUnapplied).
func (s *ESStore) envFilterField(ctx context.Context) (string, bool) {
	// Explicit operator override — no discovery, no caching, trusted
	// verbatim (the ESFieldMap contract: the operator knows their
	// mapping better than a probe does).
	if s.fields.Env != "" {
		return s.fields.Env, true
	}
	now := time.Now()
	s.envField.mu.Lock()
	if now.Before(s.envField.expires) {
		f, ok := s.envField.field, s.envField.ok
		s.envField.mu.Unlock()
		return f, ok
	}
	s.envField.mu.Unlock()

	candidates := envFieldCandidates()
	fcCtx, cancel := context.WithTimeout(ctx, envFieldCapsTimeout)
	defer cancel()
	// Window-narrowed concrete indices, same as every query path — the
	// verdict must describe the indices queries actually hit.
	idx := s.queryIndices(fcCtx, Filter{From: now.Add(-24 * time.Hour), To: now})
	caps, err := s.fieldCaps(fcCtx, idx, envFieldCapsFields(candidates))
	field, ok := "", false
	if err != nil {
		// field_caps failed (already recorded on /admin/elastic via
		// recordQueryError) — treat as unresolved for one TTL so a
		// flapping cluster isn't re-probed per request; the /logs chip
		// reports the filter as unapplied in the meantime.
		log.Printf("[logstore-es] env field discovery failed (?env= filter reported unapplied for %s): %v", envFieldTTL, err)
	} else if field, ok = resolveEnvFieldFromCaps(candidates, caps); ok {
		log.Printf("[logstore-es] env filter field resolved: %q (types=%v; override via the Settings → Elasticsearch field map)",
			field, caps[field].Types)
	} else {
		log.Printf("[logstore-es] no environment field resolvable among %v — ?env= filter reported unapplied (configure fields.env in Settings → Elasticsearch to override)",
			candidates)
	}
	s.envField.mu.Lock()
	s.envField.field, s.envField.ok, s.envField.expires = field, ok, now.Add(envFieldTTL)
	s.envField.mu.Unlock()
	return field, ok
}

// applyEnvResolution stamps Filter.envField for buildQuery when an env
// filter is requested and resolvable. Idempotent (safe across the PIT
// denial retry, which re-enters Search with the same Filter). The
// returned flag is the honest "requested but not applied" signal.
func (s *ESStore) applyEnvResolution(ctx context.Context, f Filter) (Filter, bool) {
	if f.Env == "" {
		return f, false
	}
	if fld, ok := s.envFilterField(ctx); ok {
		f.envField = fld
		return f, false
	}
	f.envField = ""
	return f, true
}
