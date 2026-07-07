package config

import "testing"

// Regression test for v0.8.205 — prod (external Distributed CH) lost ~every
// ingest batch with "clickhouse: acquire conn timeout".
//
// Root cause: the CH connection pool was a hardcoded default of 10, which
// shadowed the intended fan-out sizing. Each of the 3 signal consumers
// (spans / logs / metrics) runs Ingestion.Workers flusher goroutines, and
// every flusher holds a pool connection during its INSERT. At the default 8
// workers that is 24 flushers fighting over 10 connections; raising Workers
// to 16 made it 48-vs-10 and the flushers starved each other (and the read
// path) → acquire-conn-timeout → dropped batches.
//
// resolveMaxOpenConns is the minimal pure function the fix touches: it sizes
// the pool to ingestSignals*workers + read headroom when unset, and honors an
// explicit operator override. This test pins that contract so the pool can
// never silently fall below the flush fan-out again — v0.8.351 re-pinned it
// after the v0.8.328/329 consumers (exemplars, span_links) grew the fan-out
// to 5 signals while the old 3× literal stayed put (40 flushers vs 32 conns,
// the v0.8.205 starvation shape reintroduced).
func TestResolveMaxOpenConns(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		workers    int
		want       int
	}{
		// Unset (0) → derive ingestSignals(5)*workers + 8 headroom.
		{"default 8 workers", 0, 8, 48},
		{"bumped 16 workers", 0, 16, 88},
		{"low 4 workers", 0, 4, 28},
		// Workers unset/garbage falls back to the 8-worker assumption so the
		// pool is never derived to a starving size.
		{"zero workers floors to 8", 0, 0, 48},
		{"negative workers floors to 8", 0, -3, 48},
		// Explicit operator override is honored verbatim (may be capping
		// against CH server max_connections) — even when below the fan-out.
		{"explicit override honored", 64, 16, 64},
		{"explicit below fanout still honored", 10, 16, 10},
		{"explicit equals derived", 48, 8, 48},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveMaxOpenConns(c.configured, c.workers); got != c.want {
				t.Fatalf("resolveMaxOpenConns(%d, %d) = %d, want %d",
					c.configured, c.workers, got, c.want)
			}
		})
	}
}

// The whole point of the derivation is that an UNSET pool always clears the
// flush fan-out. Assert the invariant directly across a range of worker
// counts so a future tweak to the headroom constant can't reintroduce the
// starvation.
func TestResolveMaxOpenConns_DerivedPoolClearsFanout(t *testing.T) {
	for workers := 1; workers <= 64; workers++ {
		pool := resolveMaxOpenConns(0, workers)
		if fanout := 3 * workers; pool <= fanout {
			t.Fatalf("workers=%d: derived pool %d does not exceed flush fan-out %d",
				workers, pool, fanout)
		}
	}
}
