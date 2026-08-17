package vmetrics

// v0.9.1164 — the unfiltered-bucket-scan guard.
//
// SEMPTOM (operator, prod): ~12M active series, +281% in a day. A percentile
// or heatmap query whose only matcher is the metric name is not slow, it is
// unbounded — the name alternation is a regex over the whole index and the
// bucket family fans every match out again by `le`. One such panel degrades a
// shared vmselect for every user on the install.
//
// Every case below is a DECISION, not an implementation detail: the guard
// refuses queries a previous release happily sent, so each in/out call has to
// be pinned or the next refactor will quietly widen or narrow it.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ── Test helpers ────────────────────────────────────────────────────────────
//
// buildTranslate / buildHistogramTranslate run the builders with the guard
// LIFTED, and every pre-existing TRANSLATION test goes through them.
//
// That is deliberate rather than lazy. Those tests pin the shape of the
// rendered expression — the `or` composition, the by-clause, the window, the
// candidate alternation — on filter-less fixtures, because a filter is noise
// in an assertion about aggregation shape. If they carried the guard they
// would all have had to grow a service matcher, which would have changed the
// very strings they exist to pin, and a v0.9.1160 regression would then hide
// behind a rewritten expectation.
//
// So the guard's coverage lives HERE, in one table that names every class, and
// the translation tests keep asserting translation. The one thing this split
// must not do is let the guard rot untested — hence the decision table below
// covers the refusing AND the passing side of every branch, including the
// classes deliberately left out.
func buildTranslate(f chstore.MetricQueryFilter) (string, error) {
	return buildPromQL(f, promOpts{AllowUnfilteredPercentiles: true})
}

func buildHistogramTranslate(f chstore.MetricQueryFilter) (string, int, error) {
	return buildHistogramPromQL(f, promOpts{AllowUnfilteredPercentiles: true})
}

// svcFilter / labelFilter — the two ways a query earns scope.
func labelFilter() []chstore.FilterExpr {
	return []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/checkout"}}}
}

// TestBucketScanGuardDecisionTable is the whole contract in one place: which
// (aggregation × name × scope × setting) tuples are refused.
//
// `histoName` is a plain OTLP-ish name, so mayHaveHistogramParts is TRUE and
// the avg branch grows its `_sum`/`_count` arm — the shape the guard cares
// about. The suffixed names pin the other side of that predicate.
func TestBucketScanGuardDecisionTable(t *testing.T) {
	const histoName = "http.server.request.duration"

	tests := []struct {
		name string
		f    chstore.MetricQueryFilter
		// heatmap routes through buildHistogramPromQL instead of buildPromQL.
		heatmap    bool
		allow      bool
		wantRefuse bool
		// wantClass, when set, must appear in the refusal — the operator has
		// several panels open and cannot tell which one 400'd otherwise.
		wantClass string
		why       string
	}{
		// ── percentiles: ALWAYS guarded ─────────────────────────────────────
		{
			name:       "p99 with a service passes",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "p99", Service: "checkout"},
			wantRefuse: false,
			why:        "a service matcher bounds the scan — the normal panel",
		},
		{
			name:       "p99 with only a label filter passes",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "p99", Filters: labelFilter()},
			wantRefuse: false,
			why:        "the operator asked for a route — no service needed",
		},
		{
			name:       "p99 with neither is refused",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "p99"},
			wantRefuse: true,
			wantClass:  classPercentile,
			why:        "the reason this release exists",
		},
		{
			name:       "p50 and p95 are guarded too",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "p50"},
			wantRefuse: true,
			wantClass:  classPercentile,
			why:        "all three percentiles read the bucket family",
		},
		{
			name:       "p99 unfiltered passes once the operator allows it",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "p99"},
			allow:      true,
			wantRefuse: false,
			why:        "the off-switch the refusal message points at must work",
		},
		{
			name: "group-by is NOT scope",
			f: chstore.MetricQueryFilter{Name: histoName, Aggregation: "p99",
				GroupBy: []string{"http.route"}},
			wantRefuse: true,
			wantClass:  classPercentile,
			why: "a by-clause groups AFTER the scan; counting it would let " +
				"`by (le)` — which every percentile carries — disable the guard everywhere",
		},
		{
			name: "an explicitly-picked _bucket row is still guarded",
			f: chstore.MetricQueryFilter{Name: "http_server_request_duration_seconds_bucket",
				Aggregation: "p99"},
			wantRefuse: true,
			wantClass:  classPercentile,
			why: "the le fan-out is a property of the SERIES, not of whether " +
				"Coremetry appended the suffix",
		},

		// ── heatmap: ALWAYS guarded ─────────────────────────────────────────
		{
			name:       "heatmap with a service passes",
			f:          chstore.MetricQueryFilter{Name: histoName, Service: "checkout"},
			heatmap:    true,
			wantRefuse: false,
		},
		{
			name:       "heatmap with only a label filter passes",
			f:          chstore.MetricQueryFilter{Name: histoName, Filters: labelFilter()},
			heatmap:    true,
			wantRefuse: false,
		},
		{
			name:       "heatmap with neither is refused",
			f:          chstore.MetricQueryFilter{Name: histoName},
			heatmap:    true,
			wantRefuse: true,
			wantClass:  classHeatmap,
			why:        "same _bucket selector, plus a time × le grid built in Go",
		},
		{
			name:       "heatmap unfiltered passes once allowed",
			f:          chstore.MetricQueryFilter{Name: histoName},
			heatmap:    true,
			allow:      true,
			wantRefuse: false,
			why:        "ONE setting governs both surfaces — two switches would be a trap",
		},

		// ── avg: guarded only where it expands into the histogram family ────
		{
			name:       "avg on a histogram-family name with neither is refused",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "avg"},
			wantRefuse: true,
			wantClass:  classAvgFamily,
			why:        "operator decision — this is the arm that grows _sum/_count",
		},
		{
			name:       "the default (empty) aggregation IS avg and is guarded",
			f:          chstore.MetricQueryFilter{Name: histoName},
			wantRefuse: true,
			wantClass:  classAvgFamily,
			why:        "promAggregator maps \"\" to avg; a caller omitting it must not slip through",
		},
		{
			name:       "avg on a histogram-family name with a service passes",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "avg", Service: "checkout"},
			wantRefuse: false,
		},
		{
			name:       "avg unfiltered passes once allowed",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "avg"},
			allow:      true,
			wantRefuse: false,
		},
		{
			name:       "PLAIN GAUGE avg is free",
			f:          chstore.MetricQueryFilter{Name: "jvm_memory_used_bytes_total", Aggregation: "avg"},
			wantRefuse: false,
			why: "one candidate, no `or` arm — guarding it would break the most " +
				"ordinary chart on the install for a cost it does not have",
		},
		{
			name:       "avg on an explicitly-picked _count row is free",
			f:          chstore.MetricQueryFilter{Name: "http_server_request_duration_seconds_count", Aggregation: "avg"},
			wantRefuse: false,
			why:        "mayHaveHistogramParts is false — nothing was expanded, one flat selector",
		},

		// ── deliberately OUT: rate / increase and the single-arm aggs ───────
		{
			name:       "unfiltered rate on a histogram name PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "rate"},
			wantRefuse: false,
			why: "its histogram arm reads _count — one series per attribute set, " +
				"no le fan-out. The asymmetry with avg is an operator call, " +
				"recorded here rather than smoothed over",
		},
		{
			name:       "unfiltered increase on a histogram name PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "increase"},
			wantRefuse: false,
		},
		{
			name:       "unfiltered sum PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "sum"},
			wantRefuse: false,
			why:        "no histogram arm at all — one selector over the base family",
		},
		{
			name:       "unfiltered last PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "last"},
			wantRefuse: false,
		},
		{
			name:       "unfiltered max PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "max"},
			wantRefuse: false,
		},
		{
			name:       "unfiltered count PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "count"},
			wantRefuse: false,
		},
		{
			name:       "unfiltered min PASSES",
			f:          chstore.MetricQueryFilter{Name: histoName, Aggregation: "min"},
			wantRefuse: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := promOpts{AllowUnfilteredPercentiles: tc.allow}
			var err error
			if tc.heatmap {
				_, _, err = buildHistogramPromQL(tc.f, opts)
			} else {
				_, err = buildPromQL(tc.f, opts)
			}
			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("query was refused but must pass (%s): %v", tc.why, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("query was sent but must be refused (%s)", tc.why)
			}
			if !errors.Is(err, ErrUnfilteredBuckets) {
				t.Fatalf("refusal not tagged ErrUnfilteredBuckets — the API would 502 "+
					"and send the operator to check a healthy VM: %v", err)
			}
			if tc.wantClass != "" && !strings.Contains(err.Error(), tc.wantClass) {
				t.Fatalf("refusal does not name the query class %q: %v", tc.wantClass, err)
			}
		})
	}
}

// The guard's sentinel must be its OWN, distinguishable from ErrUnsupported.
//
// Both map to 400, so the split buys nothing at the HTTP layer — it buys the
// operator's next move. ErrUnsupported means "no MetricsQL form exists, ask a
// different question"; this one means "your question is fine and this install
// has it switched off". Collapsing them would make the checkbox
// undiscoverable, because a refusal that reads as a backend limitation is not
// one anybody goes looking in Settings for.
func TestGuardSentinelIsDistinctFromUnsupported(t *testing.T) {
	_, err := buildPromQL(chstore.MetricQueryFilter{Name: "m", Aggregation: "p99"}, promOpts{})
	if err == nil {
		t.Fatal("unfiltered p99 was not refused")
	}
	if !errors.Is(err, ErrUnfilteredBuckets) {
		t.Fatalf("not tagged ErrUnfilteredBuckets: %v", err)
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatal("guard refusal also matches ErrUnsupported — the two classes " +
			"have different fixes and must stay separable")
	}
}

// A refusal that does not say how to lift it is a dead end. Requirement:
// the message states WHICH guard is in force and HOW to turn it off.
func TestGuardMessageCarriesTheFixAndTheOffSwitch(t *testing.T) {
	for _, class := range []string{classPercentile, classHeatmap, classAvgFamily} {
		msg := unfilteredBucketMsg(class)
		for _, want := range []string{
			class,                             // which query was refused
			"filtre",                          // what to add
			"Settings",                        // where the switch is
			"Filtresiz yüzdeliklere izin ver", // the checkbox, spelled as on screen
			"Metrik okuma backend",            // the tab, spelled as on screen
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("class %q: refusal is missing %q\nmsg: %s", class, want, msg)
			}
		}
	}
}

// The checkbox label in the refusal must be the one the UI renders. A message
// naming a control that does not exist under that name sends the operator
// hunting through Settings — the failure the message was written to prevent.
//
// Pinned against the frontend SOURCE rather than a duplicated constant,
// because the drift this catches is someone editing the label in the tab.
func TestGuardMessageLabelMatchesTheFrontend(t *testing.T) {
	const label = "Filtresiz yüzdeliklere izin ver"
	if !strings.Contains(unfilteredBucketMsg(classPercentile), label) {
		t.Fatalf("refusal no longer names %q", label)
	}
	b, err := os.ReadFile(filepath.Join("..", "..",
		"frontend", "src", "pages", "settings", "MetricsBackendTab.tsx"))
	if err != nil {
		// NOT a skip. "I could not measure it" is not "it is fine" — a gate
		// that silently stops running is worse than no gate, because the
		// green suite now asserts something it never checked.
		t.Fatalf("MetricsBackendTab.tsx unreadable, cannot verify the label the "+
			"400 message points at: %v", err)
	}
	if !strings.Contains(string(b), label) {
		t.Fatalf("MetricsBackendTab.tsx no longer renders the checkbox label %q that "+
			"the 400 message tells the operator to look for", label)
	}
}

// An UNEXPRESSIBLE FILTER must beat the guard.
//
// Both refusals are 400s, so this is about which message the operator reads.
// "filter operator > is unsupported" names the actual problem; "add a filter"
// would be actively misleading when they already added one that failed to
// translate. The ordering is a property of where the guard sits — after the
// matcher loop — so it is asserted rather than assumed.
func TestUnexpressibleFilterBeatsTheGuard(t *testing.T) {
	bad := []chstore.FilterExpr{{Key: "n", Op: ">", Values: []string{"5"}}}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"percentile", func() error {
			_, err := buildPromQL(chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p99", Filters: bad}, promOpts{})
			return err
		}},
		{"avg", func() error {
			_, err := buildPromQL(chstore.MetricQueryFilter{
				Name: "m", Aggregation: "avg", Filters: bad}, promOpts{})
			return err
		}},
		{"heatmap", func() error {
			_, _, err := buildHistogramPromQL(chstore.MetricQueryFilter{
				Name: "m", Filters: bad}, promOpts{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("an inexpressible filter was silently dropped — the v0.9.566 class")
			}
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("the guard shadowed the filter refusal — the operator would be "+
					"told to add a filter they already added: %v", err)
			}
		})
	}
}

// THE GUARD REACHES THE LIVE PATH, and it refuses BEFORE dialling.
//
// The pure decision table above proves the builders refuse. This proves the
// refusal is what an actual request gets — the setting is read from the same
// config snapshot ready() returned, threaded through promOptions, and applied
// before any HTTP round trip. That last part is the whole point of the feature:
// a guard that refused after VM had already run the scan would protect nothing.
//
// Both service methods are covered because they take different routes to the
// same options (QueryMetricNoted → buildPromQL, QueryMetricHistogram →
// buildHistogramPromQL), and a release that wired only one of them would look
// finished.
func TestGuardReachesTheServiceLayer(t *testing.T) {
	const histoName = "http.server.request.duration"

	newProbe := func() (*httptest.Server, *int32) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		}))
		return srv, &hits
	}

	from := time.Unix(1_700_000_000, 0)
	to := from.Add(time.Hour)

	calls := map[string]func(*Service) error{
		"percentile": func(s *Service) error {
			_, _, err := s.QueryMetricNoted(context.Background(), chstore.MetricQueryFilter{
				Name: histoName, Aggregation: "p99", From: from, To: to})
			return err
		},
		"heatmap": func(s *Service) error {
			_, err := s.QueryMetricHistogram(context.Background(), chstore.MetricQueryFilter{
				Name: histoName, From: from, To: to})
			return err
		},
	}

	for name, call := range calls {
		t.Run(name+"/refused without touching VM", func(t *testing.T) {
			srv, hits := newProbe()
			defer srv.Close()
			s := New()
			// The DEFAULT config — no AllowUnfilteredPercentiles. A fresh
			// install must be protected without anyone opting in.
			s.Configure(Settings{BaseURL: srv.URL})
			err := call(s)
			if err == nil {
				t.Fatal("the live path sent an unfiltered bucket query")
			}
			if !errors.Is(err, ErrUnfilteredBuckets) {
				t.Fatalf("live refusal not tagged ErrUnfilteredBuckets: %v", err)
			}
			if n := atomic.LoadInt32(hits); n != 0 {
				t.Fatalf("VM was dialled %d time(s) — a guard that refuses AFTER the "+
					"scan protects nothing", n)
			}
		})

		t.Run(name+"/allowed by the setting", func(t *testing.T) {
			srv, hits := newProbe()
			defer srv.Close()
			s := New()
			s.Configure(Settings{BaseURL: srv.URL, AllowUnfilteredPercentiles: true})
			if err := call(s); err != nil {
				t.Fatalf("the off-switch did not reach the live path: %v", err)
			}
			if n := atomic.LoadInt32(hits); n != 1 {
				t.Fatalf("VM dialled %d time(s), want 1", n)
			}
		})
	}

	t.Run("a scoped query dials with the guard on", func(t *testing.T) {
		srv, hits := newProbe()
		defer srv.Close()
		s := New()
		s.Configure(Settings{BaseURL: srv.URL})
		_, _, err := s.QueryMetricNoted(context.Background(), chstore.MetricQueryFilter{
			Name: histoName, Aggregation: "p99", Service: "checkout", From: from, To: to})
		if err != nil {
			t.Fatalf("a normal scoped percentile panel was refused: %v", err)
		}
		if n := atomic.LoadInt32(hits); n != 1 {
			t.Fatalf("VM dialled %d time(s), want 1", n)
		}
	})
}

// The guard must not change the EXPRESSION for a query that passes it. A
// release that silently rewrote every scoped percentile would be a much bigger
// change than the one that was asked for.
func TestGuardDoesNotAlterPassingExpressions(t *testing.T) {
	f := chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Aggregation: "p99",
		Service: "checkout", StepSeconds: 600,
	}
	guarded, err := buildPromQL(f, promOpts{})
	if err != nil {
		t.Fatalf("scoped percentile refused: %v", err)
	}
	lifted, err := buildPromQL(f, promOpts{AllowUnfilteredPercentiles: true})
	if err != nil {
		t.Fatalf("with the guard lifted: %v", err)
	}
	if guarded != lifted {
		t.Fatalf("the setting changed the expression:\n guard on  %s\n guard off %s", guarded, lifted)
	}
}
