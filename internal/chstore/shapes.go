package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TraceShape (v0.5.264) — operator-facing "trace shape" cluster.
// A shape is the sorted-unique set of (service.name, operation)
// pairs that appear in a trace; two traces share a shape if and
// only if they exercise the exact same set of (service, op)
// touchpoints regardless of count / order. This collapses
// millions of traces into a few dozen distinct "what is this app
// doing" patterns, the Dynatrace OpenTelemetry "mass data
// analysis" workflow native to Coremetry.
//
// Counts are estimated from a 10%-by-trace_id hash sample so the
// query stays under the 30s ceiling at billion-span scale. The
// p99 / avg / error_rate stats are computed across the sample
// (no extrapolation — they're representative of the cohort
// regardless of the multiplier).
type TraceShape struct {
	// ShapeID is the stable hash key; useful as a React key.
	ShapeID string `json:"shapeId"`
	// Signature is the sorted-unique list of "service|operation"
	// strings. The frontend renders this as a multi-chip card so
	// the operator can see the cohort at a glance.
	Signature []string `json:"signature"`
	// TraceCount is the estimated count over the requested
	// window (sample count × sample-rate-inverse). The
	// frontend tags this as "~N traces" so operators don't
	// read it as an exact figure.
	TraceCount uint64 `json:"traceCount"`
	// AvgMs / P99Ms / ErrorRate are NOT extrapolated — they're
	// computed across the sample so the cohort's stats stay
	// representative.
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
	ErrorRate float64 `json:"errorRate"`
	// SamplingRate is the trace_id hash divisor we used (e.g.
	// 0.1 for the default 10% sample). UI surfaces it as a
	// "estimated counts" tag so the operator knows the
	// numbers are sampled.
	SamplingRate float64 `json:"samplingRate"`
}

// TraceShapesFilter shapes the request to GetTraceShapes.
type TraceShapesFilter struct {
	From, To time.Time
	Service  string // optional — pins shapes to a single root service
	Limit    int    // default 30; capped at 100
}

// GetTraceShapes runs a two-level GROUP BY: first per-trace_id
// to compute each trace's shape fingerprint + duration, then
// per-shape to count traces + roll up p99 / error rate. Both
// stages run inside one CH query so the network round trip
// stays at one.
//
// trace_id hash-sampling at 1/10 keeps the inner aggregate from
// touching the full spans table on long windows. The 30s
// execution-time ceiling guards against pathological windows
// where even a 10% sample stalls.
func (s *Store) GetTraceShapes(ctx context.Context, f TraceShapesFilter) ([]TraceShape, error) {
	if f.Limit <= 0 {
		f.Limit = 30
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	sampleDivisor := 10 // 10% sample
	sampleRate := 1.0 / float64(sampleDivisor)

	args := []any{
		f.From, f.To, // outer time window
		sampleDivisor,
		f.From, f.To,
	}
	serviceFilter := ""
	if f.Service != "" {
		serviceFilter = ` AND service_name = ?`
		args = append(args, f.Service)
	}
	args = append(args, f.Limit)

	// CH idiom:
	//   groupUniqArray(...) gathers per-trace_id distinct
	//   "service|operation" tuples; arraySort gives a
	//   canonical ordering so cityHash64 produces stable
	//   shape IDs across traces with equivalent signatures.
	//   arrayStringConcat is needed because cityHash64 wants
	//   a String input.
	q := fmt.Sprintf(`
		SELECT
			shape_hash,
			any(shape_signature) AS signature,
			count() AS sample_count,
			avg(trace_dur_ms)    AS avg_ms,
			quantile(0.99)(trace_dur_ms) AS p99_ms,
			countIf(has_error)   AS sample_error
		FROM (
			SELECT
				trace_id,
				cityHash64(arrayStringConcat(
					arraySort(groupUniqArray(concat(service_name, '|', name))),
					',')) AS shape_hash,
				arraySort(groupUniqArray(concat(service_name, '|', name))) AS shape_signature,
				(max(toUnixTimestamp64Milli(time)) - min(toUnixTimestamp64Milli(time))) AS trace_dur_ms,
				maxIf(1, status_code = 'error') AS has_error
			FROM spans
			WHERE time >= ? AND time <= ?
			  AND cityHash64(trace_id) %% ? = 0%s
			GROUP BY trace_id
		)
		GROUP BY shape_hash
		ORDER BY sample_count DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		serviceFilter)

	// %s placeholder for the optional " AND service_name = ?"
	// already inlined above; the inner WHERE block has its own
	// time + sample-divisor placeholders. Re-order args for the
	// final placeholder positions: inner-time, sample-divisor,
	// outer-time(unused — we removed the outer time filter
	// since the inner WHERE catches it), service?, limit.
	//
	// Actually the simpler form is: inner WHERE catches all
	// filtering, the outer query just rolls up. Drop the outer
	// f.From/f.To.
	finalArgs := []any{f.From, f.To, sampleDivisor}
	if f.Service != "" {
		finalArgs = append(finalArgs, f.Service)
	}
	finalArgs = append(finalArgs, f.Limit)

	// Re-emit a simpler form of the query: outer SELECT has no
	// own WHERE; everything's in the inner.
	q = fmt.Sprintf(`
		SELECT
			shape_hash,
			any(shape_signature) AS signature,
			count() AS sample_count,
			avg(trace_dur_ms)    AS avg_ms,
			quantile(0.99)(trace_dur_ms) AS p99_ms,
			countIf(has_error)   AS sample_error
		FROM (
			SELECT
				trace_id,
				cityHash64(arrayStringConcat(
					arraySort(groupUniqArray(concat(service_name, '|', name))),
					',')) AS shape_hash,
				arraySort(groupUniqArray(concat(service_name, '|', name))) AS shape_signature,
				(max(toUnixTimestamp64Milli(time)) - min(toUnixTimestamp64Milli(time))) AS trace_dur_ms,
				maxIf(1, status_code = 'error') AS has_error
			FROM spans
			WHERE time >= ? AND time <= ?
			  AND cityHash64(trace_id) %% ? = 0%s
			GROUP BY trace_id
		)
		GROUP BY shape_hash
		ORDER BY sample_count DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		serviceFilter)

	rows, err := s.conn.Query(ctx, q, finalArgs...)
	if err != nil {
		return nil, fmt.Errorf("trace shapes: %w", err)
	}
	defer rows.Close()

	out := make([]TraceShape, 0, f.Limit)
	for rows.Next() {
		var (
			shapeHash   uint64
			signature   []string
			sampleCount uint64
			avgMs       float64
			p99Ms       float64
			sampleErr   uint64
		)
		if err := rows.Scan(&shapeHash, &signature, &sampleCount, &avgMs, &p99Ms, &sampleErr); err != nil {
			return nil, err
		}
		errRate := 0.0
		if sampleCount > 0 {
			errRate = float64(sampleErr) / float64(sampleCount)
		}
		out = append(out, TraceShape{
			ShapeID:      fmt.Sprintf("%x", shapeHash),
			Signature:    signature,
			TraceCount:   uint64(float64(sampleCount) / sampleRate),
			AvgMs:        avgMs,
			P99Ms:        p99Ms,
			ErrorRate:    errRate,
			SamplingRate: sampleRate,
		})
	}
	return out, rows.Err()
}

// shapeSignatureString joins a shape's signature into a single
// string for cache keys. Used by the API layer.
func ShapeSignatureKey(sig []string) string {
	return strings.Join(sig, ",")
}
