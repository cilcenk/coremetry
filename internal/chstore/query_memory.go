package chstore

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Per-query memory ceilings, PROPORTIONED to the server's own ceiling
// (v0.9.975).
//
// The measured problem — Coremetry pinned per-query caps LARGER than
// the whole server's budget:
//
//	chc pod memory limit                       3584 MiB
//	max_server_memory_usage (server_settings)  3006477107 B (2.8 GiB)
//	driver default max_memory_usage            4_000_000_000 (4 GB)
//	SQL-embedded max_memory_usage              8_000_000_000 (8 GB)
//
// A per-query cap ABOVE the server total can never fire. The query
// exhausts 2.8 GiB of server-wide budget long before it reaches its own
// 4 GB / 8 GB limit, so the thing that actually fires is ClickHouse's
// OvercommitTracker — and that does NOT kill the query which caused the
// pressure. It picks a VICTIM by overcommit ratio. 5.4% of
// behaviour-engine ticks (21/389) died with code 241 while the tick
// itself was holding ~25 MiB.
//
// The fix is not a smaller hardcoded number — the right number depends
// on the node, which is exactly why hardcoding failed. It is a RATIO of
// whatever the server reports: a per-query cap at 60% of the server
// ceiling makes a heavy query throttle ITSELF (spill to disk, or die
// naming its own query) before the server-wide tracker ever has to
// choose a victim.
//
// FAIL-OPEN is deliberate. When the probe cannot read
// max_server_memory_usage — restricted credential, exotic build, proxy
// in front of CH — serverMax is 0 and every clamp returns the requested
// value untouched, i.e. byte-identical to pre-v0.9.975 behaviour. A
// memory guard that breaks reads because it could not introspect the
// server would be worse than the bug it fixes.

const (
	// Built-in per-query defaults (pre-v0.9.184 constants, kept as the
	// single source now that no SQL string carries its own copy).
	defaultQueryMemory     int64 = 4_000_000_000
	defaultExternalGroupBy int64 = 1_000_000_000
	defaultExternalSort    int64 = 1_000_000_000

	// heavyScanMemory / heavyScanSpillBytes — the deliberately HIGHER
	// request carried by the bounded raw-spans GROUP BYs and the
	// topology/backtrace writers. These queries genuinely need more than
	// the driver default; the clamp does not take that away, it only
	// stops them asking for more than the server has (v0.9.975).
	heavyScanMemory     int64 = 8_000_000_000
	heavyScanSpillBytes int64 = 2_000_000_000

	// flowTopologyMemory — the ad-hoc /api/topology flow read. Lower
	// than the writers on purpose: it is request-scoped, not a
	// background bucket job.
	flowTopologyMemory int64 = 4_000_000_000

	// defaultQueryMemFraction — the share of the server ceiling ONE
	// query may claim. 0.6 leaves 40% for concurrent reads, background
	// merges and the async-insert buffers, which is what the
	// OvercommitTracker was eating into when it started shooting
	// bystanders.
	defaultQueryMemFraction = 0.6

	// spillMemFraction — spill thresholds (external GROUP BY / sort)
	// get a tighter ratio than the hard cap so a query starts writing
	// to disk well BEFORE it approaches the ceiling. A spill threshold
	// at or above the cap can never fire: the query would OOM having
	// never spilled.
	spillMemFraction = 0.25

	// Sanity rails for an operator-supplied fraction. Below 0.1 the cap
	// is so tight that ordinary reads fail; above 0.9 there is no
	// headroom left for merges and the clamp stops meaning anything.
	minQueryMemFraction = 0.1
	maxQueryMemFraction = 0.9
)

// normalizeMemFraction maps any operator input onto the usable band.
//
// Unset (0), negative and NaN all mean "operator did not choose" and
// resolve to the default rather than to the floor — a config parse
// failure must not silently install the tightest possible cap.
// Anything else is clamped into [0.1, 0.9], +Inf included.
func normalizeMemFraction(f float64) float64 {
	if math.IsNaN(f) || f <= 0 {
		return defaultQueryMemFraction
	}
	if f < minQueryMemFraction {
		return minQueryMemFraction
	}
	if f > maxQueryMemFraction {
		return maxQueryMemFraction
	}
	return f
}

// clampQueryMemory returns the per-query byte ceiling actually worth
// setting: the smaller of what the caller asked for and the configured
// share of the server's own ceiling.
//
// serverMax <= 0 means the probe failed → fail open, return requested.
// requested <= 0 means the caller has no opinion → unchanged.
//
// The clamped value is never allowed to reach 0, because to ClickHouse
// `max_memory_usage = 0` means UNLIMITED — clamping to zero would
// invert the whole point of this function.
func clampQueryMemory(requested, serverMax int64, fraction float64) int64 {
	if serverMax <= 0 || requested <= 0 {
		return requested
	}
	allowed := int64(float64(serverMax) * normalizeMemFraction(fraction))
	if allowed <= 0 {
		return requested
	}
	if requested <= allowed {
		return requested
	}
	return allowed
}

// clampSpillMemory is clampQueryMemory for the external GROUP BY / sort
// thresholds. It uses the tighter spill ratio, but never a ratio LARGER
// than the query fraction: if an operator pins COREMETRY_CH_MEM_FRACTION
// to 0.1, a 0.25 spill threshold would sit ABOVE the hard cap and could
// never fire.
func clampSpillMemory(requested, serverMax int64, fraction float64) int64 {
	f := normalizeMemFraction(fraction)
	if spillMemFraction < f {
		f = spillMemFraction
	}
	return clampQueryMemory(requested, serverMax, f)
}

// ── Boot probe ──────────────────────────────────────────────────────

// serverMaxMemorySetting — the one server setting the whole clamp hangs
// off. Named once so the SQL, the retry log and the failure log cannot
// drift apart.
const serverMaxMemorySetting = "max_server_memory_usage"

const (
	// probeMaxExecSeconds — the server-side max_execution_time budget
	// for the boot probe.
	//
	// v0.9.984, MEASURED on the operator's live v0.9.983 boot: 5 s was
	// NOT enough on a 2-node cluster coming up —
	//
	//	[chstore] max_server_memory_usage okunamadı (code: 159,
	//	  Timeout exceeded: elapsed 5.861160586 seconds, maximum: 5)
	//
	// …and because the probe fails open, the whole of v0.9.975 became a
	// NO-OP on exactly the node it was written for: serverMax fell back
	// to 0, the 4 GB per-query default was applied verbatim, and that
	// number sits ABOVE the node's real 3_006_477_107 B (2.80 GiB)
	// ceiling — so the cap could never fire, which is the original bug.
	// A fix that silently un-applies itself under load is worse than no
	// fix, because the boot log then reads like success.
	//
	// 15 s is derived from the timeouts already in this package rather
	// than invented: the read pool's driver ReadTimeout is 30 s
	// (store.go) and one distributed-DDL round is
	// ddlTaskTimeoutSeconds = 20 s. 15 s stays under both. The setup
	// connection this actually runs on leaves ReadTimeout at the driver
	// default (300 s), so the server-side budget is the binding one.
	// The cost of the larger budget is bounded and paid once: a single
	// row out of an in-memory system table, one time per process, at a
	// point where boot has already spent ~13 s dialling and creating
	// the database.
	probeMaxExecSeconds = 15

	// probeAttempts / probeRetryDelay — ONE retry, because the observed
	// failure was CONTENTION and not incapacity: the same read on the
	// same credential answers in milliseconds once the node is idle. A
	// short backoff converts a transient boot-storm loss into a correct
	// clamp. Two losses still fail open, byte-identical to v0.9.975 —
	// the retry adds a chance to succeed, never a chance to block boot.
	probeAttempts   = 2
	probeRetryDelay = 2 * time.Second
)

// serverMaxMemoryQuery renders the probe SQL.
//
// On a cluster the answer is the SMALLEST non-zero ceiling in the
// fleet. Heterogeneous nodes exist, and any node may coordinate the
// query — a cap that is safe only on the biggest node is not a cap.
// Zeros are excluded rather than min'd in: one node reporting
// "unlimited" must not disarm the clamp for the constrained ones. If
// every node reports 0 the min over an empty set is 0 → fail-open.
func serverMaxMemoryQuery(clusterName string) string {
	budget := strconv.Itoa(probeMaxExecSeconds)
	if cn := strings.TrimSpace(clusterName); cn != "" {
		return `SELECT toInt64(min(v))
		       FROM (
		            SELECT toInt64OrZero(value) AS v
		              FROM clusterAllReplicas('` + cn + `', system.server_settings)
		             WHERE name = '` + serverMaxMemorySetting + `'
		             LIMIT 1000
		       )
		      WHERE v > 0
		      SETTINGS max_execution_time = ` + budget
	}
	return `SELECT toInt64OrZero(value)
	        FROM system.server_settings
	       WHERE name = '` + serverMaxMemorySetting + `'
	       LIMIT 1
	       SETTINGS max_execution_time = ` + budget
}

// probeServerMaxMemory reads the server's max_server_memory_usage.
//
// Runs on the boot SETUP connection, before the main pools exist, so
// the per-query Settings map can be proportioned before a single query
// is issued. Never returns an error: 0 means "unknown" and every caller
// treats that as fail-open.
func probeServerMaxMemory(ctx context.Context, conn driver.Conn, clusterName string) int64 {
	q := serverMaxMemoryQuery(clusterName)
	return probeWithRetry(ctx, probeAttempts, probeRetryDelay, func(ctx context.Context) (int64, error) {
		var v int64
		err := conn.QueryRow(ctx, q).Scan(&v)
		return v, err
	})
}

// probeWithRetry runs read up to attempts times, waiting delay between
// tries, and folds every outcome onto the fail-open contract: a
// non-negative value on success, 0 on "we could not find out".
//
// Split from the SQL so the retry decision itself is table-tested
// (v0.9.984) — the failure it exists for only reproduces under a real
// boot storm, which is precisely the condition a test cannot stage.
// The behaviour pinned is: first success wins and stops early; a
// transient first failure does NOT disarm the clamp; two failures land
// on exactly the pre-retry fail-open; a cancelled context stops
// retrying rather than sleeping out the shutdown.
func probeWithRetry(ctx context.Context, attempts int, delay time.Duration, read func(context.Context) (int64, error)) int64 {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
attemptLoop:
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Checked BEFORE the select, not only inside it: with a
			// zero delay both cases are ready at once and Go picks at
			// random, so "already cancelled" has to be decided first or
			// a shutdown gets one extra doomed round-trip.
			if ctx.Err() != nil {
				break attemptLoop
			}
			select {
			case <-ctx.Done():
				break attemptLoop
			case <-time.After(delay):
			}
		}
		v, err := read(ctx)
		if err == nil {
			if v < 0 {
				return 0
			}
			return v
		}
		lastErr = err
		if i < attempts-1 {
			log.Printf("[chstore] %s okunamadı (%v) — %v sonra bir kez daha denenecek (%d/%d); "+
				"gözlenen arıza boot sırasındaki ÇEKİŞMEYDİ, yetki değil (v0.9.984)",
				serverMaxMemorySetting, err, delay, i+1, attempts)
		}
	}
	log.Printf("[chstore] %s okunamadı (%v) — per-query bellek kelepçesi ORANTILANMAYACAK, "+
		"yapılandırılan değerler aynen uygulanacak (fail-open)", serverMaxMemorySetting, lastErr)
	return 0
}

// resolveQueryMemory folds the configured/default per-query limits
// against the probed server ceiling and reports what it did.
//
// Split out of New so the whole decision — including the two WARNING
// branches an operator reads in the boot log — is exercised by the
// table test rather than only by starting a process.
type queryMemoryPlan struct {
	ServerMax  int64   // 0 = unknown (fail-open)
	Fraction   float64 // normalized
	Configured int64   // what cfg/defaults asked for
	MaxMemory  int64   // effective per-query cap
	GroupBy    int64   // effective external GROUP BY threshold
	Sort       int64   // effective external sort threshold
}

// Clamped reports whether the configured per-query cap was actually
// lowered — i.e. the operator's number could never have fired.
func (p queryMemoryPlan) Clamped() bool { return p.MaxMemory < p.Configured }

func resolveQueryMemory(cfgMax, cfgGroupBy, cfgSort, serverMax int64, fraction float64) queryMemoryPlan {
	maxMem := defaultQueryMemory
	if cfgMax > 0 {
		maxMem = cfgMax
	}
	groupBy := defaultExternalGroupBy
	if cfgGroupBy > 0 {
		groupBy = cfgGroupBy
	}
	sort := defaultExternalSort
	if cfgSort > 0 {
		sort = cfgSort
	}
	f := normalizeMemFraction(fraction)
	return queryMemoryPlan{
		ServerMax:  serverMax,
		Fraction:   f,
		Configured: maxMem,
		MaxMemory:  clampQueryMemory(maxMem, serverMax, f),
		GroupBy:    clampSpillMemory(groupBy, serverMax, f),
		Sort:       clampSpillMemory(sort, serverMax, f),
	}
}

// ── Store-side accessors ────────────────────────────────────────────
//
// Every SQL site that used to carry its own `max_memory_usage = <n>`
// literal now goes through these. The rule is min(requested,
// effective): a heavy query may still ask for more than the driver
// default, it just cannot ask for more than the server has.

// EffectiveQueryMemory is the per-query byte ceiling in force. Never 0
// — to ClickHouse 0 means UNLIMITED, so a Store built without New
// (tests, tooling) answers the built-in default instead of disabling
// the cap.
func (s *Store) EffectiveQueryMemory() int64 {
	if s == nil || s.memPlan.MaxMemory <= 0 {
		return defaultQueryMemory
	}
	return s.memPlan.MaxMemory
}

// ConfiguredQueryMemory is what cfg/defaults asked for BEFORE the
// clamp. Equal to EffectiveQueryMemory when nothing was clamped.
func (s *Store) ConfiguredQueryMemory() int64 {
	if s == nil || s.memPlan.Configured <= 0 {
		return defaultQueryMemory
	}
	return s.memPlan.Configured
}

// QueryMemoryProbeFailed reports that the boot probe never learned the
// server's own ceiling, so NOTHING was proportioned and the configured
// per-query numbers are in force verbatim (v0.9.984).
//
// This is the state that made v0.9.975 look shipped while being a
// no-op, and it is invisible from the outside: /system/stats reads
// max_server_memory_usage live, so the panel happily shows a 2.80 GiB
// server ceiling next to a 4 GB per-query cap and no warning fires —
// the clamp-warning only triggers when a clamp actually happened. The
// flag is what lets the panel say "this was not measured" instead of
// implying it was measured and found fine.
func (s *Store) QueryMemoryProbeFailed() bool {
	return s == nil || s.memPlan.ServerMax <= 0
}

// queryMemory clamps one SQL site's request. nil/zero-value Stores
// fail open, which keeps the pure SQL-shape tests literal-identical.
func (s *Store) queryMemory(requested int64) int64 {
	if s == nil {
		return requested
	}
	return clampQueryMemory(requested, s.memPlan.ServerMax, s.memPlan.Fraction)
}

// spillMemory is queryMemory for an external GROUP BY / sort threshold.
func (s *Store) spillMemory(requested int64) int64 {
	if s == nil {
		return requested
	}
	return clampSpillMemory(requested, s.memPlan.ServerMax, s.memPlan.Fraction)
}

// queryMemSetting renders the SETTINGS fragment. Built with %d on
// purpose: TestNoEmbeddedQueryMemoryLiterals forbids a literal byte
// count next to this setting name anywhere under internal/, so a future
// edit cannot quietly re-introduce a cap the server can never enforce.
func (s *Store) queryMemSetting(requested int64) string {
	return fmt.Sprintf("max_memory_usage = %d", s.queryMemory(requested))
}

// heavyScanSpill is appended to the bounded raw-spans GROUP BYs that
// can't ride an MV (the /api/services fallback, endpoints raw, the
// service→cluster map). Without it those queries DIE rather than slow
// down — ClickHouse's memory guard (code 241) kills a ballooning GROUP
// BY hash table long before max_execution_time. External aggregation
// SPILLS the hash table to disk instead: slower, but the page renders.
//
// v0.8.392 pinned 2 GiB spill + 8 GiB ceiling as literals (the topology
// writer's prod-proven pair). v0.9.975 keeps both as the REQUEST and
// runs them through the server-ratio clamp — on a node with room the
// bytes are unchanged, on a 2.8 GiB node the 8 GiB ceiling becomes one
// that can actually fire. Pinned by TestHeavyScanSpill.
func (s *Store) heavyScanSpill() string {
	return ",\n" +
		"\t\t         max_bytes_before_external_group_by = " +
		strconv.FormatInt(s.spillMemory(heavyScanSpillBytes), 10) + ",\n" +
		"\t\t         " + s.queryMemSetting(heavyScanMemory)
}
