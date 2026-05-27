package chstore

// v0.6.29 — Service dependency impact scorer ("blast radius").
//
// Given an open Problem on service X, an operator wants to know:
// "if X is degraded, which OTHER services see broken calls right
// now?" The answer is X's upstream callers — the services that
// invoke X. Coremetry already aggregates these in
// service_callers_5m (v0.5.368); this file rolls the per-(host,
// instance, client_address) rows up to a per-CALLER-SERVICE
// summary, joins in open-problem status so cascade indicators
// surface cleanly, and exposes the result for the /problems UI.
//
// Direction note: in Coremetry's edge model, "caller" = the
// service that invokes the inspected one. If X has alert,
// blast radius = services that CALL X (their requests fail when
// X is down). Not services that X calls — those just see less
// outbound traffic, not failure.

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// BlastRadiusCaller is one upstream service impacted by an issue
// on the inspected service. RPS is computed over the window;
// HasOpenProblem flags the cascade case where the caller is
// ALREADY firing its own alert, suggesting the failure is
// propagating up the call graph.
type BlastRadiusCaller struct {
	Service        string  `json:"service"`
	Calls          uint64  `json:"calls"`           // total invocations in window
	Errors         uint64  `json:"errors"`
	RPS            float64 `json:"rps"`             // calls / window seconds
	ErrorRate      float64 `json:"errorRate"`       // percent
	HasOpenProblem bool    `json:"hasOpenProblem"`  // cascade indicator
}

// BlastRadius bundles the per-caller list with a summary so the
// frontend chip can render "↘ N svcs · M rps" without summing
// client-side.
type BlastRadius struct {
	Service           string              `json:"service"`           // inspected service
	WindowSec         int                 `json:"windowSec"`
	TotalCallers      int                 `json:"totalCallers"`
	CascadingCallers  int                 `json:"cascadingCallers"`  // count of callers WITH their own open problem
	TotalRPS          float64             `json:"totalRps"`
	TotalErrorsPerSec float64             `json:"totalErrorsPerSec"`
	Callers           []BlastRadiusCaller `json:"callers"`           // sorted by calls desc
}

// GetServiceBlastRadius returns the upstream-caller impact
// summary for `service` over [now - since, now]. Reads
// service_callers_5m (FINAL) for the per-bucket aggregates and
// joins open-problem status in one extra query.
//
// Top-N cap at 25 callers. At billion-span scale a single
// service can have hundreds of callers (sidecars, mesh
// daemons); the UI surfaces the worst-impacted by calls desc
// + provides a chip-level summary for the long tail.
func (s *Store) GetServiceBlastRadius(
	ctx context.Context, service string, since time.Duration,
) (BlastRadius, error) {
	out := BlastRadius{
		Service:   service,
		WindowSec: int(since.Seconds()),
		Callers:   []BlastRadiusCaller{},
	}
	if service == "" {
		return out, fmt.Errorf("service required")
	}
	if since <= 0 {
		since = time.Hour
	}
	now := time.Now()
	from := now.Add(-since)
	bucketStart := from.Truncate(5 * time.Minute)

	// Per-caller-service rollup. Aggregate the v0.5.368 MV by
	// caller_service only — same FINAL semantics, just at
	// service granularity not host/instance.
	rows, err := s.conn.Query(ctx, `
		SELECT caller_service,
		       sum(calls)  AS calls,
		       sum(errors) AS errors
		FROM service_callers_5m FINAL
		WHERE service = ?
		  AND time_bucket >= ?
		  AND time_bucket <= ?
		  AND caller_service != ''
		GROUP BY caller_service
		ORDER BY calls DESC
		LIMIT 25
		SETTINGS max_execution_time = 10`,
		service, bucketStart, now)
	if err != nil {
		return out, fmt.Errorf("blast-radius callers: %w", err)
	}
	defer rows.Close()

	windowSec := since.Seconds()
	if windowSec < 1 {
		windowSec = 1
	}
	for rows.Next() {
		var r BlastRadiusCaller
		if err := rows.Scan(&r.Service, &r.Calls, &r.Errors); err != nil {
			return out, fmt.Errorf("scan blast-radius row: %w", err)
		}
		r.RPS = float64(r.Calls) / windowSec
		if r.Calls > 0 {
			r.ErrorRate = float64(r.Errors) * 100.0 / float64(r.Calls)
		}
		out.Callers = append(out.Callers, r)
		out.TotalRPS += r.RPS
		out.TotalErrorsPerSec += float64(r.Errors) / windowSec
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.TotalCallers = len(out.Callers)

	// Cascade flag — caller services that ALSO have an open
	// problem right now. One additional FINAL read against
	// problems (small table; sub-ms). Index lookup keeps the
	// inner loop O(callers).
	openProblems, _ := s.openProblemServices(ctx)
	for i := range out.Callers {
		if _, has := openProblems[out.Callers[i].Service]; has {
			out.Callers[i].HasOpenProblem = true
			out.CascadingCallers++
		}
	}

	// Re-sort cascading callers first, then by calls desc.
	// Operator focus: cascade-affected services land at the top
	// regardless of absolute call volume.
	sort.SliceStable(out.Callers, func(i, j int) bool {
		a, b := out.Callers[i], out.Callers[j]
		if a.HasOpenProblem != b.HasOpenProblem {
			return a.HasOpenProblem
		}
		return a.Calls > b.Calls
	})
	return out, nil
}

// openProblemServices returns the set of service names that have
// at least one open problem RIGHT NOW. Used as a fast lookup for
// blast-radius cascade flagging. FINAL on the ReplacingMergeTree
// so resolved/regressed transitions are honoured.
func (s *Store) openProblemServices(ctx context.Context) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	rows, err := s.conn.Query(ctx, `
		SELECT DISTINCT service FROM problems FINAL
		WHERE status = 'open' AND service != ''
		SETTINGS max_execution_time = 5`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var svc string
		if err := rows.Scan(&svc); err != nil {
			return out, err
		}
		out[svc] = struct{}{}
	}
	return out, rows.Err()
}
