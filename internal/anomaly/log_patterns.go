package anomaly

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// LogPatternAnomaly is one production-grade signal pattern that
// either started firing for the first time within the window
// (Kind="new") or jumped 2x+ over its trailing baseline
// (Kind="spike"). What an SRE wants to see in their morning
// inbox: not the raw log volume, just what changed.
type LogPatternAnomaly struct {
	Pattern        string  `json:"pattern"`        // human-readable name
	Regex          string  `json:"regex"`          // the raw re2 used
	Kind           string  `json:"kind"`           // "new" | "spike"
	CurrentCount   uint64  `json:"currentCount"`
	BaselineCount  uint64  `json:"baselineCount"`  // trailing window
	Ratio          float64 `json:"ratio"`          // current / max(baseline,1)
	Service        string  `json:"service"`        // service emitting most matches in current window
	Sample         string  `json:"sample"`         // representative log body, truncated
	LastSeenNs     int64   `json:"lastSeenNs"`
	// TopServices — v0.5.287. Per-service breakdown of current
	// window hits, top 5, count desc. The /logs LogPatternStrip
	// renders these as a rosette under the pattern chip so the
	// operator can see "OOMKilled fires on foo-svc (12) and
	// bar-svc (3)" without expanding or filtering.
	TopServices []logstore.PatternServiceHit `json:"topServices,omitempty"`
}

type logPattern struct {
	Name  string
	Regex string
	// Tokens is a list of substrings — at least one must appear
	// in the log body for the regex `match()` to even be tried.
	// Drives a `multiSearchAny(body, [tokens])` prefilter that
	// hits the tokenbf_v1 skip index on `body`, so granules
	// containing none of the tokens are pruned before the
	// expensive regex evaluation. Critical at billion-logs/day
	// where a naked `match()` would scan every granule. Tokens
	// are all-lowercase substrings the body is guaranteed to
	// contain when the regex matches; case-insensitivity is
	// handled by `multiSearchAnyCaseInsensitive`.
	Tokens []string
}

// patterns are intentionally curated — every line here corresponds
// to a real production failure shape that an SRE wants paged on.
// Order doesn't matter; the response is sorted by ratio desc.
var patterns = []logPattern{
	{"Oracle errors (ORA-)",   `ORA-[0-9]+`,                                                                                                                  []string{"ora-"}},
	{"Oracle TNS errors",      `TNS-[0-9]+`,                                                                                                                  []string{"tns-"}},
	{"Out of memory",          `OutOfMemoryError|out of memory|OOMKilled|cannot allocate memory`,                                                             []string{"outofmemoryerror", "out of memory", "oomkilled", "cannot allocate"}},
	{"Null pointer",           `NullPointerException|null pointer dereference|null pointer exception`,                                                        []string{"nullpointer", "null pointer"}},
	{"Database deadlock",      `[Dd]eadlock|deadlock detected`,                                                                                               []string{"deadlock"}},
	{"Connection refused",     `ECONNREFUSED|[Cc]onnection refused`,                                                                                          []string{"econnrefused", "connection refused"}},
	{"Read / write timeout",   `i/o timeout|read timeout|write timeout|context deadline exceeded`,                                                            []string{"timeout", "deadline exceeded"}},
	{"Go panic",               `^panic:|runtime error:`,                                                                                                     []string{"panic:", "runtime error"}},
	{"TLS / certificate",      `x509:|certificate has expired|SSL handshake failed|tls: handshake failure`,                                                  []string{"x509:", "certificate", "ssl handshake", "tls: handshake"}},
	{"Auth failures",          `(?i)401 Unauthorized|invalid credentials|access denied|forbidden`,                                                            []string{"401", "credentials", "access denied", "forbidden"}},
	{"Disk full",              `no space left on device|disk full|ENOSPC`,                                                                                    []string{"no space left", "disk full", "enospc"}},
	{"Java exceptions",        `(ClassCast|IllegalState|IllegalArgument|UnsupportedOperation|ArrayIndexOutOfBounds|ConcurrentModification|NumberFormat|StackOverflow)Exception`, []string{"exception"}},
	// v0.5.284 — JBoss / WildFly / Spring Boot / JDBC stack
	// patterns. Operator runs a Java estate (JBoss + Spring
	// Boot + Oracle); the generic Java patterns above missed
	// the framework-specific shapes that come up on prod
	// failures — bean wiring, deployment hooks, Hikari pool
	// exhaustion. Each token list is lowercase and represents
	// substrings the body MUST contain when the regex matches
	// (case-insensitive prefilter).
	{"JBoss / WildFly errors", `(WFLY|JBAS)[0-9]+`,                                                                                                           []string{"wfly", "jbas"}},
	{"JBoss deployment fail",  `Failed to start service|Deployment ".*" was rolled back|service .* in service registry has failed`,                          []string{"failed to start service", "was rolled back", "service registry"}},
	{"Spring app failed",      `APPLICATION FAILED TO START|Error starting ApplicationContext`,                                                              []string{"application failed to start", "error starting applicationcontext"}},
	{"Spring bean failure",    `(BeanCreation|NoSuchBeanDefinition|BeanInstantiation|UnsatisfiedDependency|CircularDependency)Exception`,                    []string{"beancreation", "nosuchbeandefinition", "beaninstantiation", "unsatisfieddependency", "circulardependency"}},
	{"JDBC pool exhausted",    `HikariPool-.* - Connection is not available|connection pool .*exhausted|IJ000453|IJ000655|Could not acquire JDBC Connection`, []string{"hikaripool", "exhausted", "ij000453", "ij000655", "could not acquire jdbc"}},
	{"Hibernate / JPA",        `(LazyInitialization|OptimisticLock|StaleObjectState|TransactionTimedOut|TransactionRequired)Exception`,                      []string{"lazyinitialization", "optimisticlock", "staleobjectstate", "transactiontimedout", "transactionrequired"}},
	{"DB constraint violation",`(DataIntegrityViolation|ConstraintViolation|SQLIntegrityConstraintViolation)Exception`,                                       []string{"dataintegrityviolation", "constraintviolation", "sqlintegrityconstraint"}},
}

// DetectLogPatterns runs each pattern against the raw `logs` CH
// table over a current window + a much longer trailing baseline
// (default: 5-min current vs 1-hour trailing). Returns only the
// patterns that changed significantly: brand new, or 2x+ over the
// per-window-length baseline rate.
//
// The asymmetric windows keep an anomaly visible for ~1 hour
// after it first fires — a 5m-vs-5m comparison has the spike fall
// into baseline within minutes, which makes the anomaly section
// flicker. With a 1h baseline the same spike stays visible until
// the baseline absorbs it.
//
// Performance:
//   - One query per pattern (combines current + baseline via
//     countIf), N=11 → 11 round-trips total instead of 22.
//   - All N queries fire in parallel; total cold-cache cost is
//     bounded by the slowest single query, not the sum.
//   - Each query is partition-pruned to ~10 minutes of logs and
//     the regex runs against the LowCardinality body column.
//   - Caller should cache the result for 60s; the detector is
//     idempotent and the cache absorbs page reloads.
//
// At 1B logs/day this completes in well under a second cold;
// warm requests serve directly from Redis.
//
// v0.5.241 — refactored to take a logstore.Store instead of
// *chstore.Store so the detector works against BOTH the CH and
// the ES log backend. CH path remains the regex+tokenbf prefilter
// route; ES path uses query_string token-OR against the body
// field (regex is ignored; tokens must be zero-false-negative
// vs the regex). Cross-backend correctness depends on detector
// authors keeping the Tokens list synchronized with the Regex.
func DetectLogPatterns(ctx context.Context, store logstore.Store, window time.Duration) ([]LogPatternAnomaly, error) {
	now := time.Now()
	curStart := now.Add(-window)
	// Trailing baseline = 1h (or the longer of 1h vs 12×window).
	// Cap the lookback so a freshly deployed instance doesn't try
	// to scan more than a day on the first call.
	baseLookback := time.Hour
	if 12*window > baseLookback {
		baseLookback = 12 * window
	}
	if baseLookback > 24*time.Hour {
		baseLookback = 24 * time.Hour
	}
	baseStart := now.Add(-baseLookback)

	// One batched call covers all patterns. CH iterates
	// internally; ES batches via _msearch so we pay one HTTP
	// round-trip total — at billion-log/day on an external ES
	// cluster, that's the only way to keep wall time bounded.
	specs := make([]logstore.PatternSpec, len(patterns))
	for i, p := range patterns {
		specs[i] = logstore.PatternSpec{Regex: p.Regex, Tokens: p.Tokens}
	}
	stats, err := store.CountPatterns(ctx, specs, curStart, baseStart, now)
	if err != nil {
		return nil, err
	}

	type result struct {
		anomaly LogPatternAnomaly
		ok      bool
	}
	results := make([]result, len(patterns))

	var wg sync.WaitGroup
	wg.Add(len(patterns))
	for i, p := range patterns {
		i, p := i, p // capture for the goroutine
		go func() {
			defer wg.Done()
			st := stats[i]
			if st.Cur == 0 {
				return
			}
			cur := st.Cur
			base := st.Base
			service := st.Service
			sample := st.Sample
			lastNs := st.LastSeenNs

			// Normalise the trailing baseline to the same window
			// length as `cur` so a spike is "current rate is 2x+
			// the trailing-window rate" regardless of how long
			// the baseline lookback is. Without this, a 12x
			// longer baseline window inflates `base` by 12x and
			// the spike check would never fire.
			windowRatio := float64(window) / float64(baseLookback)
			basePerWindow := float64(base) * windowRatio

			var (
				ratio float64
				kind  string
			)
			switch {
			case basePerWindow == 0 && cur >= 3:
				// Brand-new pattern this window. cur >= 3 floor
				// suppresses single-line noise (a one-off OOM in
				// a quiet hour rarely needs a page) without
				// requiring sustained volume to register.
				ratio = float64(cur)
				kind = "new"
			case basePerWindow > 0 && float64(cur)/basePerWindow >= 2.0:
				ratio = float64(cur) / basePerWindow
				kind = "spike"
			default:
				return
			}

			results[i] = result{
				anomaly: LogPatternAnomaly{
					Pattern: p.Name,
					Regex:   p.Regex,
					Kind:    kind,
					CurrentCount: cur,
					// Rendered to the operator as the
					// per-window-equivalent baseline rate so
					// the UI's "cur vs base" reads intuitively.
					BaselineCount: uint64(basePerWindow),
					Ratio:         ratio,
					Service:       service,
					Sample:        truncateSample(sample, 240),
					LastSeenNs:    lastNs,
					TopServices:   st.TopServices,
				},
				ok: true,
			}
		}()
	}
	wg.Wait()

	out := []LogPatternAnomaly{}
	for _, r := range results {
		if r.ok {
			out = append(out, r.anomaly)
		}
	}

	// Sort: new ones first (most operationally interesting), then
	// spikes by ratio desc, ties broken by current count.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "new"
		}
		if out[i].Ratio != out[j].Ratio {
			return out[i].Ratio > out[j].Ratio
		}
		return out[i].CurrentCount > out[j].CurrentCount
	})
	return out, nil
}

func truncateSample(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
