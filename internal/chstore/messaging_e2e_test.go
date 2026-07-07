package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.372 — messaging end-to-end produce→consume latency (Stage-2 M2).
// SQL-shape + pure-fold invariants for messaging_e2e.go. Pins:
//   - every table touched is bounded: LIMIT on both spans subqueries, on
//     the joined pair set, on the output rows, max_execution_time = 10;
//   - BOTH joins are GLOBAL (spans shards by service_name, span_links by
//     trace_id — nothing is colocated; a plain JOIN silently undercounts
//     on distributed CH, the v0.8.185/186 class);
//   - semconv kind gates on both sides (consumer links, producer targets);
//   - lag clamps at 0 (clock skew must not surface as negative latency);
//   - placeholder count ↔ msgE2EArgs positional alignment;
//   - the fold: ROLLUP total row (t=0) → overall stats, bucket rows →
//     series, zero rows → Linkless=true honest empty state, NaN scrubbed.

func TestMsgE2ESQLShape(t *testing.T) {
	cases := []struct {
		name string
		frag string
		n    int // required occurrence count
	}{
		{"global join on both sides", "GLOBAL INNER JOIN", 2},
		{"consumer-side spans subquery bounded", "LIMIT 50000", 2},
		{"pair set bounded", "LIMIT 200000", 1},
		{"output rows bounded (series + rollup total)", "LIMIT 5001", 1},
		{"execution time capped at 10s", "SETTINGS max_execution_time = 10", 1},
		{"single-scan series+total via rollup", "GROUP BY b WITH ROLLUP", 1},
		{"negative lag (clock skew) clamped", "greatest(0.", 1},
		{"consumer semconv kind gate", "kind = 'consumer'", 1},
		{"producer semconv kind gate", "kind = 'producer'", 1},
		{"system scoped on both spans sides", "msg_system = ?", 2},
		{"destination resolution on both spans sides", "messaging.destination.name", 2},
		{"link window on the driving table", "l.time >= ? AND l.time <= ?", 1},
		{"lag derives from producer END (time+duration)", "toUnixTimestamp64Nano(p.time) + p.duration", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Count(msgE2ESQL, c.frag); got != c.n {
				t.Errorf("msgE2ESQL contains %q %d times, want %d", c.frag, got, c.n)
			}
		})
	}
}

// Placeholder count must match the args builder exactly — a drift binds a
// window bound into a destination filter silently (the class the v0.8.186
// named-column discipline exists for, read-side edition).
func TestMsgE2EArgsAlignment(t *testing.T) {
	from := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	args := msgE2EArgs("kafka", "(default)", "transfer.posted", from, to)

	if want := strings.Count(msgE2ESQL, "?"); len(args) != want {
		t.Fatalf("POSITIONAL MISALIGNMENT — %d placeholders vs %d args", want, len(args))
	}

	// Pin the load-bearing positions: consumer window verbatim, producer
	// window widened backwards by exactly msgE2EProducerSlack, link window
	// verbatim, and the (system, cluster, destination) triple on both
	// spans sides.
	pFrom := from.Add(-msgE2EProducerSlack)
	want := []any{
		from, to, "kafka", "(default)", "transfer.posted",
		pFrom, to, "kafka", "(default)", "transfer.posted",
		from, to,
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg %d = %v, want %v", i, args[i], want[i])
		}
	}
}

func TestBuildMsgE2E(t *testing.T) {
	nan := func() float64 { var z float64; return 0 / z }()

	cases := []struct {
		name string
		rows []msgE2ERow
		want MsgE2E
	}{
		{
			// No correlated pairs → zeros + Linkless, NOT a fake "0ms":
			// the drawer renders the "no span links from the SDKs" hint.
			name: "no rows is linkless",
			rows: nil,
			want: MsgE2E{Linkless: true, Series: []MsgE2EPoint{}},
		},
		{
			name: "total row feeds overall stats and slowest pair",
			rows: []msgE2ERow{
				{t: 0, n: 42, qs: []float64{5, 90, 240}, maxMs: 512.5, slowC: "c-trace", slowP: "p-trace"},
				{t: 1000, n: 40, avgMs: 12.5},
				{t: 1300, n: 2, avgMs: 300},
			},
			want: MsgE2E{
				Count: 42, P50Ms: 5, P95Ms: 90, P99Ms: 240,
				SlowestLagMs: 512.5, SlowestConsumerTraceID: "c-trace", SlowestProducerTraceID: "p-trace",
				Series: []MsgE2EPoint{
					{TimeS: 1000, Count: 40, AvgMs: 12.5},
					{TimeS: 1300, Count: 2, AvgMs: 300},
				},
			},
		},
		{
			// encoding/json rejects NaN/Inf (v0.5.301 class) — the fold
			// must scrub every float it copies out of the scan.
			name: "NaN scrubbed everywhere",
			rows: []msgE2ERow{
				{t: 0, n: 3, qs: []float64{nan, nan, nan}, maxMs: nan},
				{t: 1000, n: 3, avgMs: nan},
			},
			want: MsgE2E{
				Count:  3,
				Series: []MsgE2EPoint{{TimeS: 1000, Count: 3}},
			},
		},
		{
			// A short quantile array (defensive: server-side change or a
			// stale replica mid-deploy) must not panic — stats stay zero.
			name: "short quantile array tolerated",
			rows: []msgE2ERow{{t: 0, n: 7, qs: []float64{1}}},
			want: MsgE2E{Count: 7, Series: []MsgE2EPoint{}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildMsgE2E(c.rows)
			if got.Count != c.want.Count || got.Linkless != c.want.Linkless ||
				got.P50Ms != c.want.P50Ms || got.P95Ms != c.want.P95Ms || got.P99Ms != c.want.P99Ms ||
				got.SlowestLagMs != c.want.SlowestLagMs ||
				got.SlowestConsumerTraceID != c.want.SlowestConsumerTraceID ||
				got.SlowestProducerTraceID != c.want.SlowestProducerTraceID {
				t.Errorf("scalars = %+v, want %+v", *got, c.want)
			}
			if got.Series == nil {
				t.Fatal("Series must never be nil (JSON null crashes the drawer's .map)")
			}
			if len(got.Series) != len(c.want.Series) {
				t.Fatalf("series length = %d, want %d", len(got.Series), len(c.want.Series))
			}
			for i := range got.Series {
				if got.Series[i] != c.want.Series[i] {
					t.Errorf("series[%d] = %+v, want %+v", i, got.Series[i], c.want.Series[i])
				}
			}
		})
	}
}
