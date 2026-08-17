package vmetrics

// v0.9.1151 — the trial-mode reachability contract.
//
// Trial mode (`?metricsrc=vm` on one request, api/metricsource.go) exists
// because metric NAMES differ between the two backends: VM sanitises
// `jvm.memory.used` to `jvm_memory_used`, so "will my dashboards work on
// VictoriaMetrics" cannot be answered from Settings — it needs one real
// chart. Flipping the global Enabled toggle to find out would move every
// panel of every logged-in user at once.
//
// That splits one question into two, and this file pins the split:
//
//	Configured() → VM is the DEFAULT for every metric read (Enabled + URL)
//	Available()  → a single trial request can REACH VM (URL alone)
//
// The failure mode if they collapse back into one is silent in both
// directions. Available() gaining the Enabled check makes trial mode
// answer "VictoriaMetrics yapılandırılmamış" while the operator stares at
// a filled-in base URL. Configured() LOSING it re-routes the whole install
// off ClickHouse the moment a URL is saved — before anyone chose to.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAvailableIsTheTrialGate(t *testing.T) {
	tests := []struct {
		name          string
		cfg           Settings
		wantAvailable bool
		// Deliberately asserted side by side: the interesting rows are the
		// ones where the two predicates DISAGREE, and a table that only
		// held Available() would not show them.
		wantConfigured bool
	}{
		// THE trial row. This is the state an operator is in while
		// evaluating VM: URL saved, toggle off.
		{name: "url saved, toggle off", cfg: Settings{BaseURL: "http://vm:8428"},
			wantAvailable: true, wantConfigured: false},

		{name: "url saved, toggle on", cfg: Settings{Enabled: true, BaseURL: "http://vm:8428"},
			wantAvailable: true, wantConfigured: true},

		// No URL = nothing to call, in either mode. A trial against this
		// must 400 at the API, never fall back to ClickHouse.
		{name: "toggle on, no url", cfg: Settings{Enabled: true},
			wantAvailable: false, wantConfigured: false},
		{name: "zero value", cfg: Settings{},
			wantAvailable: false, wantConfigured: false},

		// Whitespace is not a URL. Same trim as Configured() — a form
		// submitted with a stray space must not look reachable.
		{name: "blank url, toggle off", cfg: Settings{BaseURL: "   "},
			wantAvailable: false, wantConfigured: false},
		{name: "blank url, toggle on", cfg: Settings{Enabled: true, BaseURL: "\t\n "},
			wantAvailable: false, wantConfigured: false},

		// A token without a URL is still unreachable — the credential is
		// not the address.
		{name: "token but no url", cfg: Settings{AuthType: "bearer", Token: "tok"},
			wantAvailable: false, wantConfigured: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			s.Configure(tc.cfg)
			if got := s.Available(); got != tc.wantAvailable {
				t.Errorf("Available() = %v, want %v", got, tc.wantAvailable)
			}
			if got := s.Configured(); got != tc.wantConfigured {
				t.Errorf("Configured() = %v, want %v", got, tc.wantConfigured)
			}
		})
	}

	// nil receiver: main() always wires the service, but a partial init
	// must degrade to "not reachable", not panic. The API calls
	// s.vmetrics.Available() unconditionally.
	var nilSvc *Service
	if nilSvc.Available() {
		t.Fatal("nil service must not report available")
	}
}

// ready() is the reachability predicate the four read methods share. It
// must accept a base URL with Enabled OFF, or every trial read would fail
// with "not configured" — the routing authority (the API selector) and the
// reachability check would disagree, and the operator would have no way to
// tell which of the two refused them.
func TestReadyAcceptsDisabledWithURL(t *testing.T) {
	s := New()
	s.Configure(Settings{BaseURL: "http://vm:8428", AuthType: "bearer", Token: "tok"})

	cfg, err := s.ready()
	if err != nil {
		t.Fatalf("ready() with a URL but Enabled=false: %v — trial mode cannot read", err)
	}
	// The FULL config comes back, not a scrubbed copy: a trial read has to
	// carry the same auth the enabled path would, or an operator behind
	// vmauth would see a 401 and blame VM instead of the trial.
	if cfg.BaseURL != "http://vm:8428" || cfg.Token != "tok" || cfg.AuthType != "bearer" {
		t.Fatalf("ready() returned a partial config: %+v", cfg)
	}

	// No URL is still refused, and the message says what is missing.
	s.Configure(Settings{Enabled: true})
	if _, err := s.ready(); err == nil {
		t.Fatal("ready() accepted an empty base URL — reads would go nowhere")
	} else if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unhelpful message: %v", err)
	}

	// nil receiver stays an error, never a panic.
	var nilSvc *Service
	if _, err := nilSvc.ready(); err == nil {
		t.Fatal("nil service ready() must error")
	}
}

// End-to-end of the same property through a real read method: with Enabled
// off and a URL set, the read must ATTEMPT the call (and fail on transport)
// rather than short-circuit on configuration. A short-circuit here is what
// trial mode would look like if ready() regained the Enabled check — and
// the API would render it as "not configured", pointing the operator at a
// form that is already filled in.
func TestDisabledWithURLStillAttemptsTheCall(t *testing.T) {
	s := New()
	// Port 1 is reserved and closed; the dial fails fast and deterministically.
	s.Configure(Settings{BaseURL: "http://127.0.0.1:1"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := s.ListMetricNames(ctx, "", "", 10, 0)
	if err == nil {
		t.Fatal("want a transport error from a closed port")
	}
	if strings.Contains(err.Error(), "not configured") {
		t.Fatalf("read short-circuited on configuration instead of dialling: %v — "+
			"trial mode (Enabled off, URL set) is broken", err)
	}
}
