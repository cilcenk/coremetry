package api

// v0.9.1151 — per-request metric-source override (`?metricsrc=vm|ch`,
// deneme modu).
//
// Four properties are pinned here and every one of them fails SILENTLY if
// it breaks:
//
//  1. RESOLUTION — the param's semantics, including the two deliberate
//     asymmetries (vm needs a base URL but NOT Enabled; ch is always
//     valid). A wrong branch does not error, it reads the other store.
//  2. NO-PARAM REGRESSION PIN — a request without the param must behave
//     byte-for-byte like v0.9.1150. This is the property a "small
//     refactor" of the resolver breaks first.
//  3. CACHE KEY — the key a trial request lands on must carry the source
//     it actually read from. If it did not, `?metricsrc=vm` would be
//     served the default backend's cached body for a full TTL and the
//     operator would conclude VM works (or does not) from ClickHouse
//     numbers — v0.5.187 with a query param as the poisoning input.
//  4. HANDLER PIN — every metric handler must call metricSourceFor(r).
//     One that calls metricSource() still compiles, still answers, and
//     silently IGNORES the param: the operator sees ClickHouse data on a
//     page they believe is reading VictoriaMetrics.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// ── 1 + 2. Resolution ───────────────────────────────────────────────────────

func TestResolveMetricSourceParam(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		vmAvailable bool
		want        string // "" = no opinion, use the Settings default
		wantErr     bool
		errContains string
	}{
		// The regression pin: no param = no opinion, whether or not VM is
		// reachable. Both rows matter — a resolver that returned "ch" for
		// the empty case would pin every unparameterised request to
		// ClickHouse and quietly un-apply the operator's global toggle.
		{name: "absent, vm reachable", raw: "", vmAvailable: true, want: ""},
		{name: "absent, no vm", raw: "", vmAvailable: false, want: ""},
		// Whitespace-only is absent, not invalid: a hand-typed URL with a
		// stray space should not 400.
		{name: "whitespace is absent", raw: "   ", vmAvailable: true, want: ""},

		// ch is ALWAYS valid — the escape hatch has to work in the
		// direction "VM is the default, show me ClickHouse" too.
		{name: "ch with vm reachable", raw: "ch", vmAvailable: true, want: metricSourceCH},
		{name: "ch without vm", raw: "ch", vmAvailable: false, want: metricSourceCH},

		// vm needs a base URL, NOT the Enabled toggle. vmAvailable=true
		// with Enabled off is exactly the trial-mode case.
		{name: "vm reachable", raw: "vm", vmAvailable: true, want: metricSourceVM},
		{
			name: "vm unconfigured is 400 with the fix in the message",
			raw:  "vm", vmAvailable: false, wantErr: true,
			errContains: "Settings → Metrik backend",
		},

		// Anything else is a typo, and naming it is most of the diagnosis.
		{name: "typo", raw: "vmetrics", vmAvailable: true, wantErr: true, errContains: `"vmetrics"`},
		{name: "wrong case", raw: "VM", vmAvailable: true, wantErr: true, errContains: `"VM"`},
		{name: "clickhouse spelled out", raw: "clickhouse", vmAvailable: true, wantErr: true, errContains: "clickhouse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMetricSourceParam(tc.raw, tc.vmAvailable)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error for %q (vmAvailable=%v), got source %q", tc.raw, tc.vmAvailable, got)
				}
				// Must be 400-class, never 502: nothing upstream was
				// contacted, so blaming VictoriaMetrics would send the
				// operator to check a healthy cluster.
				if !isBadRequest(err) {
					t.Fatalf("error is not errBadRequest (%v) — it would surface as 500/502", err)
				}
				if isUpstream(err) {
					t.Fatalf("error is tagged errUpstream (%v) — a rejected param must not 502", err)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveMetricSourceParam(%q, %v) = %q, want %q", tc.raw, tc.vmAvailable, got, tc.want)
			}
		})
	}
}

// The invalid-value message must name BOTH accepted values. An operator
// typing the param by hand has no other feedback channel — a bare
// "invalid" forces them into the source.
func TestInvalidParamMessageNamesTheAcceptedSet(t *testing.T) {
	_, err := resolveMetricSourceParam("nope", true)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{`"vm"`, `"ch"`, metricSourceParam} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %s", err.Error(), want)
		}
	}
}

// A pathological value must not be echoed back unbounded, and the clamp
// must cut on rune boundaries — a byte-sliced multibyte value comes back
// as a replacement char, i.e. differing from what the operator typed for
// reasons they cannot see.
func TestInvalidParamEchoIsClampedByRune(t *testing.T) {
	long := strings.Repeat("ş", 500) // 2 bytes per rune
	_, err := resolveMetricSourceParam(long, true)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if strings.ContainsRune(msg, '�') {
		t.Fatal("clamp cut mid-rune — the echoed value contains U+FFFD")
	}
	if len([]rune(msg)) > 200 {
		t.Fatalf("error message is %d runes — the echo is not clamped", len([]rune(msg)))
	}
	if !strings.Contains(msg, "…") {
		t.Error("a truncated echo must say it was truncated")
	}
	// Short values pass through untouched, ellipsis and all.
	if got := clampParamValue("vm", 32); got != "vm" {
		t.Fatalf("clampParamValue(%q) = %q", "vm", got)
	}
	if got := clampParamValue("şşş", 3); got != "şşş" {
		t.Fatalf("clamp fired at exactly max: %q", got)
	}
}

// metricSourceFor is the request-level wrapper. The Available() /
// Configured() split is the whole feature, so the matrix crosses the
// param against BOTH settings states.
func TestMetricSourceForRequest(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *vmetrics.Settings // nil = no service wired at all
		query   string
		want    string
		wantErr bool
	}{
		// No param → v0.9.1150 behaviour, unchanged.
		{name: "no param, no vm", cfg: nil, query: "", want: metricSourceCH},
		{name: "no param, vm enabled", cfg: &vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"}, query: "", want: metricSourceVM},
		{name: "no param, vm url but disabled", cfg: &vmetrics.Settings{BaseURL: "http://vm:8428"}, query: "", want: metricSourceCH},

		// THE trial-mode row: VM off in Settings, one request asks for it.
		{name: "trial vm while disabled", cfg: &vmetrics.Settings{BaseURL: "http://vm:8428"}, query: "?metricsrc=vm", want: metricSourceVM},

		// The reverse escape hatch: VM is the default, one request wants CH.
		{name: "escape to ch while vm is default", cfg: &vmetrics.Settings{Enabled: true, BaseURL: "http://vm:8428"}, query: "?metricsrc=ch", want: metricSourceCH},

		// No URL anywhere → 400, never a quiet ClickHouse read.
		{name: "trial vm with no url", cfg: &vmetrics.Settings{Enabled: true}, query: "?metricsrc=vm", wantErr: true},
		{name: "trial vm with no service", cfg: nil, query: "?metricsrc=vm", wantErr: true},

		// ch works even with no VM wired at all.
		{name: "ch with no vm wired", cfg: nil, query: "?metricsrc=ch", want: metricSourceCH},

		{name: "garbage", cfg: nil, query: "?metricsrc=x", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			if tc.cfg != nil {
				s.vmetrics = vmetrics.New()
				s.vmetrics.Configure(*tc.cfg)
			}
			src, err := s.metricSourceFor(httptest.NewRequest("GET", "/api/metrics/names"+tc.query, nil))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got source %q", src.Name())
				}
				if src != nil {
					t.Fatal("a rejected request must not also return a source — a caller " +
						"that forgets the error check would read the wrong store")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := src.Name(); got != tc.want {
				t.Fatalf("source = %q, want %q", got, tc.want)
			}
		})
	}
}

// The no-param path must be the SAME decision metricSource() makes, not a
// re-derivation that could drift. Asserted across every settings state.
func TestNoParamMatchesSettingsDefaultExactly(t *testing.T) {
	for _, cfg := range []*vmetrics.Settings{
		nil,
		{},
		{BaseURL: "http://vm:8428"},
		{Enabled: true},
		{Enabled: true, BaseURL: "http://vm:8428"},
	} {
		s := &Server{}
		if cfg != nil {
			s.vmetrics = vmetrics.New()
			s.vmetrics.Configure(*cfg)
		}
		want := s.metricSource().Name()
		src, err := s.metricSourceFor(httptest.NewRequest("GET", "/api/metrics/names", nil))
		if err != nil {
			t.Fatalf("cfg %+v: unexpected error: %v", cfg, err)
		}
		if got := src.Name(); got != want {
			t.Fatalf("cfg %+v: paramless request routed to %q, settings default is %q", cfg, got, want)
		}
	}
}

// ── 3. Cache key ────────────────────────────────────────────────────────────

// keyRecorderCache captures every key serveCached looks up, then answers
// with a HIT so the handler returns without touching a store.
//
// The hit is what makes this test store-free: these handlers build the key
// BEFORE the read, and a miss would send them into a zero-value
// *chstore.Store (nil CH connection → panic inside a singleflight
// goroutine, i.e. an unhelpful crash instead of an assertion). The
// returned bytes are deliberately NOT a cache envelope, which routes
// serveCached down its legacy-entry branch: body served as-is, upstream fn
// never invoked.
type keyRecorderCache struct {
	fakeCache
	mu   sync.Mutex
	keys []string
}

func (c *keyRecorderCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	c.keys = append(c.keys, key)
	c.mu.Unlock()
	return []byte(`{"recorded":true}`), true, nil
}

func (c *keyRecorderCache) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) == 0 {
		return ""
	}
	return c.keys[len(c.keys)-1]
}

func TestTrialRequestUsesTheTrialBackendsCacheKey(t *testing.T) {
	// Each endpoint is exercised through its REAL handler, so the key
	// under test is the one production builds — not a copy of the format
	// string, which is exactly the thing that drifts.
	newServer := func(cfg vmetrics.Settings) (*Server, *keyRecorderCache) {
		rc := &keyRecorderCache{}
		vm := vmetrics.New()
		vm.Configure(cfg)
		return &Server{
			// A zero Store is enough: only MetricExclusions().Digest() is
			// read while building the key, and both are nil-safe.
			store:    &chstore.Store{},
			vmetrics: vm,
			cache:    rc,
			l1:       newL1Cache(8),
			stats:    newCacheStats(),
		}, rc
	}

	// VM has a URL but is DISABLED — so the Settings default is ClickHouse
	// and any `src=vm` in a key can only have come from the param.
	disabled := vmetrics.Settings{BaseURL: "http://vm:8428"}

	cases := []struct {
		name string
		path string
		call func(*Server, string)
	}{
		{
			name: "names (envelope shape)",
			path: "/api/metrics/names?q=jvm&limit=10",
			call: func(s *Server, url string) {
				s.getMetricNames(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))
			},
		},
		{
			name: "names (legacy bare-array shape)",
			path: "/api/metrics/names?service=cart",
			call: func(s *Server, url string) {
				s.getMetricNames(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))
			},
		},
		{
			name: "query",
			path: "/api/metrics/query?name=jvm.memory.used&agg=avg",
			call: func(s *Server, url string) {
				s.queryMetric(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))
			},
		},
		{
			name: "labels",
			path: "/api/metrics/labels?metric=m&key=pod",
			call: func(s *Server, url string) {
				s.getMetricLabelValues(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))
			},
		},
		{
			name: "attr-keys",
			path: "/api/metrics/attr-keys?metric=m",
			call: func(s *Server, url string) {
				s.getMetricAttrKeys(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))
			},
		},
		// v0.9.1157 (Faz 2) — the last two surfaces. They matter MORE than
		// the four above for the poisoning property, because they are the
		// only ones whose ClickHouse and VictoriaMetrics answers are built by
		// entirely different arithmetic (bucket differencing vs a CH GROUP
		// BY; two different query languages), so a shared key would serve
		// numbers from the other engine rather than a differently-sampled
		// version of the same ones.
		{
			name: "histogram",
			path: "/api/metrics/histogram?name=http.server.request.duration&step=60",
			call: func(s *Server, url string) {
				s.getMetricHistogram(httptest.NewRecorder(), httptest.NewRequest("GET", url, nil))
			},
		},
		{
			name: "promql",
			path: "/api/metrics/promql?query=" + url.QueryEscape(`sum(rate(m[5m]))`),
			call: func(s *Server, u string) {
				s.queryPromQL(httptest.NewRecorder(), httptest.NewRequest("GET", u, nil))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sep := "&"
			if !strings.Contains(tc.path, "?") {
				sep = "?"
			}

			// Default (no param) → ClickHouse marker.
			s, rc := newServer(disabled)
			tc.call(s, tc.path)
			base := rc.last()
			if base == "" {
				t.Fatal("serveCached never consulted the cache — the key was not built")
			}
			if !strings.Contains(base, "src="+metricSourceCH) {
				t.Fatalf("paramless key %q does not carry src=ch", base)
			}

			// Trial (?metricsrc=vm) → VM marker, and a DIFFERENT key.
			s, rc = newServer(disabled)
			tc.call(s, tc.path+sep+metricSourceParam+"=vm")
			trial := rc.last()
			if !strings.Contains(trial, "src="+metricSourceVM) {
				t.Fatalf("trial key %q does not carry src=vm — a ?metricsrc=vm request would be "+
					"served ClickHouse's cached body for a full TTL and the operator would judge "+
					"VictoriaMetrics by ClickHouse numbers (v0.5.187 class)", trial)
			}
			if trial == base {
				t.Fatalf("trial and default requests share the cache key %q", base)
			}

			// Explicit ch must land back on the default key, byte for
			// byte — an operator comparing the two backends on the same
			// chart must not pay a cold read per toggle.
			s, rc = newServer(disabled)
			tc.call(s, tc.path+sep+metricSourceParam+"=ch")
			if got := rc.last(); got != base {
				t.Fatalf("explicit ch key %q != paramless key %q", got, base)
			}
		})
	}
}

// A rejected param must not reach the cache at all: a 400 body is not a
// cacheable answer, and a key built from an unresolved source would be a
// lie about which store it holds.
func TestRejectedParamNeverTouchesTheCache(t *testing.T) {
	rc := &keyRecorderCache{}
	s := &Server{store: &chstore.Store{}, cache: rc, l1: newL1Cache(8), stats: newCacheStats()}

	for _, url := range []string{
		"/api/metrics/names?metricsrc=vm", // no VM wired → 400
		"/api/metrics/names?metricsrc=zzz",
		"/api/metrics/query?name=m&metricsrc=vm",
		"/api/metrics/labels?metric=m&key=k&metricsrc=vm",
		"/api/metrics/attr-keys?metric=m&metricsrc=vm",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", url, nil)
		switch {
		case strings.Contains(url, "/names"):
			s.getMetricNames(rec, req)
		case strings.Contains(url, "/query"):
			s.queryMetric(rec, req)
		case strings.Contains(url, "/labels"):
			s.getMetricLabelValues(rec, req)
		default:
			s.getMetricAttrKeys(rec, req)
		}
		if rec.Code != 400 {
			t.Errorf("%s → status %d, want 400", url, rec.Code)
		}
		if len(rc.keys) != 0 {
			t.Fatalf("%s consulted the cache with %v before validating the param", url, rc.keys)
		}
	}
}

// The bundle endpoint takes the same param, on the QUERY STRING of a POST.
func TestDashboardBundleHonoursTheParam(t *testing.T) {
	body := `{"from":0,"to":1,"requests":[{"id":"p1","type":"metric","name":"m"}]}`

	// Invalid param → 400 before the 50-panel body is even decoded.
	s := &Server{store: &chstore.Store{}}
	rec := httptest.NewRecorder()
	s.dashboardsData(rec, httptest.NewRequest("POST", "/api/dashboards/data?metricsrc=vm", strings.NewReader(body)))
	if rec.Code != 400 {
		t.Fatalf("unconfigured trial → status %d, want 400", rec.Code)
	}

	// Trial with a base URL present but Enabled off → the bundle reads VM.
	// The panel's read fails (no live VM), and the per-slot error is the
	// proof it went to VictoriaMetrics rather than silently to ClickHouse.
	vm := vmetrics.New()
	vm.Configure(vmetrics.Settings{BaseURL: "http://127.0.0.1:1/vm"})
	s = &Server{store: &chstore.Store{}, vmetrics: vm}
	rec = httptest.NewRecorder()
	s.dashboardsData(rec, httptest.NewRequest("POST", "/api/dashboards/data?metricsrc=vm", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("trial bundle → status %d, want 200 (per-slot errors, not a request failure)", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, "victoriametrics") && !strings.Contains(got, "connection refused") {
		t.Fatalf("bundle slot error does not name the VM read: %s", got)
	}
}

// ── 4. Handler pin ──────────────────────────────────────────────────────────

// Every metric handler must resolve its source PER REQUEST. A handler
// calling s.metricSource() compiles, answers, and ignores ?metricsrc= —
// the page looks fine while showing the wrong store's numbers.
//
// Scoped to api.go, mirroring metricsource_test.go's source pin: the
// settings-driven callers live elsewhere (mcp_deps.go, deliberately — MCP
// tools take no query param).
func TestMetricHandlersResolveSourcePerRequest(t *testing.T) {
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := stripGoComments(string(raw))

	// Vacuous-pass guard: if comment stripping ate the handlers, the scan
	// below proves nothing (the v0.9.982 lesson — a gate that stopped
	// biting kept reporting success).
	for _, marker := range []string{
		"func (s *Server) getMetricNames",
		"func (s *Server) queryMetric",
		"func (s *Server) getMetricLabelValues",
		"func (s *Server) getMetricAttrKeys",
		"func (s *Server) dashboardsData",
		// v0.9.1157 — Faz 2 brought the histogram in.
		"func (s *Server) getMetricHistogram",
	} {
		if !strings.Contains(src, marker) {
			t.Fatalf("comment stripping ate real code — %q is missing, so this scan proves nothing", marker)
		}
	}

	if loc := regexp.MustCompile(`s\.metricSource\(\)`).FindStringIndex(src); loc != nil {
		t.Errorf("api.go calls s.metricSource() at offset %d — HTTP handlers must use "+
			"s.metricSourceFor(r) or the ?%s= override is silently ignored on that surface",
			loc[0], metricSourceParam)
	}

	// And the positive half: one resolution site per surface. A dropped call
	// would show up as a lower count.
	//
	// v0.9.1157 — 5 → 6 (the histogram heatmap joined the seam). The count is
	// the thing that makes this gate bite: /api/metrics/histogram read
	// s.store directly for three releases while every OTHER metric surface
	// honoured ?metricsrc=, and nothing failed — the page rendered, from the
	// wrong store.
	if got := len(regexp.MustCompile(`s\.metricSourceFor\(r\)`).FindAllString(src, -1)); got != 6 {
		t.Errorf("found %d s.metricSourceFor(r) call sites in api.go, want 6 "+
			"(names, query, labels, attr-keys, dashboards bundle, histogram)", got)
	}

	// Every resolution must be error-checked. An ignored error would pair
	// a nil source with a live request → nil-pointer panic at best, and at
	// worst (if a future refactor returns a default alongside the error) a
	// read from the store the operator did not ask for.
	for _, m := range regexp.MustCompile(`(?s)s\.metricSourceFor\(r\).{0,160}`).FindAllString(src, -1) {
		if !strings.Contains(m, "writeErr") {
			t.Errorf("a metricSourceFor(r) call is not followed by writeErr within 160 chars:\n%s", m)
		}
	}

	// promql.go holds the SEVENTH surface. Scanned separately rather than by
	// widening the file list, because the two scans above are deliberately
	// api.go-scoped (metricsource_test.go's header explains why: the
	// legitimate direct-store callers live in other files and an exemption
	// list is the thing that quietly grows).
	praw, err := os.ReadFile("promql.go")
	if err != nil {
		t.Fatal(err)
	}
	psrc := stripGoComments(string(praw))
	if !strings.Contains(psrc, "func (s *Server) queryPromQL") {
		t.Fatal("comment stripping ate promql.go's handler — this scan proves nothing")
	}
	if !strings.Contains(psrc, "s.metricSourceFor(r)") {
		t.Error("promql.go's queryPromQL does not resolve the source per request — " +
			"?metricsrc=vm would be accepted and then silently answered from ClickHouse")
	}
	if strings.Contains(psrc, "promql.Eval") || strings.Contains(psrc, "promql.Parse") {
		t.Error("promql.go still calls internal/promql directly — the CH evaluator must be " +
			"reached through chMetricSource, or a VM-pinned request lands on the ClickHouse path")
	}
}

// The param name is part of the operator-facing contract (typed by hand,
// pasted into runbooks) AND rides in nothing else. Renaming it silently
// turns every existing trial URL into a default-backend read.
func TestParamNameIsStable(t *testing.T) {
	if metricSourceParam != "metricsrc" {
		t.Fatalf("param renamed to %q — existing trial URLs would silently read the default backend", metricSourceParam)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func isBadRequest(err error) bool {
	rec := httptest.NewRecorder()
	writeErr(rec, err)
	return rec.Code == 400
}

func isUpstream(err error) bool {
	rec := httptest.NewRecorder()
	writeErr(rec, err)
	return rec.Code == 502
}

// v0.9.1152 — isim paramı eksik /api/metrics/query 400 döner, 502 değil.
// 1150 canlı doğrulama bulgusu: store'un "metric name required" hatası VM
// yolunda errUpstream'e sarılıp 502 görünüyordu — istemci hatasına
// "backend bozuk" demek operatörü sağlıklı VM'yi kontrol etmeye gönderir.
func TestQueryMetricMissingNameIs400(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/api/metrics/query?agg=avg", nil)
	w := httptest.NewRecorder()
	s.queryMetric(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, beklenen 400; gövde: %s", w.Code, w.Body.String())
	}
}
