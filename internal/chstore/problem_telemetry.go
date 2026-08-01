package chstore

// problem_telemetry.go — v0.9.507.
//
// problem.go'dan AYRILAN telemetri okumaları. Ayrılma sebebi mimari:
// okuma havuzunun (v0.9.496) güvenlik kapısı DOSYA düzeyinde çalışıyor —
// bir dosya RoundRobin havuzunu kullanıyorsa o dosya HİÇBİR state
// tablosu okumamalı, yoksa v0.9.486'nın /users tutarsızlığı geri gelir.
//
// problem.go karışıktı: 16 fonksiyonundan 13'ü state okuyor
// (problems, alert_rules), 3'ü saf telemetri. Karışık dosyayı beyaz
// listeye alamazdım; almasaydım da bu üç okuma node 1'de çakılı
// kalacaktı. Çözüm dosyayı ikiye ayırmak — burası %100 telemetri
// (spans + deploy türevleri), problem.go %100 state.
//
// Buraya bir state tablosu okuması EKLEME. Gerekiyorsa problem.go'ya
// koy; conn_strategy_test.go bu dosyayı tarıyor ve patlar.

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"
)

// spanDeploy is one observed (version, first_seen) pair for a service.
type spanDeploy struct {
	version string
	ns      int64
}

// deploysCacheKey builds the cache key for one deploys fetch: the FNV
// digest of the SORTED service set (cf. the v0.5.187 cache-key rule —
// never length-only) plus the exact query window. Pure — table-tested.
func deploysCacheKey(services map[string]struct{}, from, to time.Time) string {
	names := make([]string, 0, len(services))
	for svc := range services {
		names = append(names, svc)
	}
	sort.Strings(names)
	h := fnv.New64a()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x:%d:%d", h.Sum64(), from.UnixNano(), to.UnixNano())
}

// fetchDeploysByService runs the bulk deploys GROUP BY for the given
// service set + window, behind a 15s TTL cache (v0.8.359, perf P2-C:
// the problems list / buckets / inbox / sidebar all re-run this every
// 5s poll with an unchanged problem set — the ~80-130ms scan collapsed
// to one query per window per TTL). Keys are exact (service set digest
// + ns-precise window), so a changed problem set is always a miss.
func (s *Store) fetchDeploysByService(ctx context.Context, services map[string]struct{}, from, to time.Time) (map[string][]spanDeploy, error) {
	key := deploysCacheKey(services, from, to)
	now := time.Now()
	s.deploysMu.Lock()
	if e, ok := s.deploysCache[key]; ok && now.Sub(e.at) < deploysCacheTTL {
		s.deploysMu.Unlock()
		return e.byService, nil
	}
	s.deploysMu.Unlock()

	svcList := make([]any, 0, len(services))
	for svc := range services {
		svcList = append(svcList, svc)
	}
	holders := ""
	for i := range svcList {
		if i > 0 {
			holders += ","
		}
		holders += "?"
	}
	// v0.9.66 (operator-reported) — bu okuma effectiveVersionExpr
	// zincirini BYPASS edip yalnız service.version okuyordu; filoda
	// service.version sabit olduğundan "fresh deploy" sinyali (P1
	// triage) hiç ateşlemiyordu. Artık merkez zincir (image-tag önde).
	sql := `
		SELECT service_name,
		       ` + effectiveVersionExpr + ` AS version,
		       toUnixTimestamp64Nano(min(time))                 AS first_seen_ns
		FROM spans
		WHERE service_name IN (` + holders + `)
		  AND time >= ? AND time <= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
		GROUP BY service_name, version
		HAVING version != ''
		ORDER BY service_name, first_seen_ns ASC
		SETTINGS max_execution_time = 10`
	args := append([]any{}, svcList...)
	args = append(args, from, to)
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byService := map[string][]spanDeploy{}
	for rows.Next() {
		var svc, ver string
		var ns int64
		if err := rows.Scan(&svc, &ver, &ns); err != nil {
			return nil, err
		}
		byService[svc] = append(byService[svc], spanDeploy{ver, ns})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.storeDeploysCacheEntry(key, deploysCacheEntry{at: now, byService: byService}, now)
	return byService, nil
}

// storeDeploysCacheEntry inserts one fetch result, keeping the cache
// bounded: expired entries are swept on every store, and if a burst of
// distinct problem sets still overflows the cap, the oldest entry is
// dropped. Split out so the bound is table-testable without a CH conn.
func (s *Store) storeDeploysCacheEntry(key string, e deploysCacheEntry, now time.Time) {
	s.deploysMu.Lock()
	defer s.deploysMu.Unlock()
	if s.deploysCache == nil {
		s.deploysCache = map[string]deploysCacheEntry{}
	}
	for k, old := range s.deploysCache {
		if now.Sub(old.at) >= deploysCacheTTL {
			delete(s.deploysCache, k)
		}
	}
	if len(s.deploysCache) >= deploysCacheMax {
		var oldestK string
		var oldestAt time.Time
		for k, old := range s.deploysCache {
			if oldestK == "" || old.at.Before(oldestAt) {
				oldestK, oldestAt = k, old.at
			}
		}
		delete(s.deploysCache, oldestK)
	}
	s.deploysCache[key] = e
}

// EnrichProblemsWithDeploys attaches the most recent
// observed service.version deploy that happened up to
// `lookback` before each problem's started_at. Single bulk
// CH query covers every service across every problem in the
// slice — N+1 free regardless of problem count. Soft-fails:
// CH error returns the slice unchanged rather than blocking
// the page render.
//
// Mechanism: one GROUP BY over spans for the union of
// involved services in [min(started)-lookback, max(started)]
// (cached ~15s, v0.8.359), then per-problem in-memory match
// against the highest first_seen time ≤ that problem's
// started_at.
func (s *Store) EnrichProblemsWithDeploys(ctx context.Context, problems []Problem, lookback time.Duration) []Problem {
	if len(problems) == 0 {
		return problems
	}
	// Distinct services + global time window across the page.
	services := map[string]struct{}{}
	var minStarted, maxStarted int64
	for i, p := range problems {
		if p.Service == "" {
			continue
		}
		services[p.Service] = struct{}{}
		if i == 0 || p.StartedAt < minStarted {
			minStarted = p.StartedAt
		}
		if p.StartedAt > maxStarted {
			maxStarted = p.StartedAt
		}
	}
	if len(services) == 0 {
		return problems
	}
	from := time.Unix(0, minStarted).Add(-lookback)
	to := time.Unix(0, maxStarted)

	byService, err := s.fetchDeploysByService(ctx, services, from, to)
	if err != nil {
		return problems
	}
	lookbackNs := int64(lookback)
	for i := range problems {
		list := byService[problems[i].Service]
		if len(list) == 0 {
			continue
		}
		// Find latest deploy with ns ≤ problem.StartedAt and
		// ns ≥ problem.StartedAt-lookback. List is asc, so
		// walk from the end.
		var pick *spanDeploy
		for j := len(list) - 1; j >= 0; j-- {
			if list[j].ns > problems[i].StartedAt {
				continue
			}
			if list[j].ns < problems[i].StartedAt-lookbackNs {
				break
			}
			pick = &list[j]
			break
		}
		if pick != nil {
			problems[i].RecentDeploy = &RecentDeploy{
				Version:    pick.version,
				TimeUnixNs: pick.ns,
				AgeSeconds: (problems[i].StartedAt - pick.ns) / 1e9,
			}
		}
	}
	return problems
}

// EnrichAnomaliesWithDeploys is the AnomalyEvent twin of
// EnrichProblemsWithDeploys. v0.5.286 — same one-shot
// bulk-query pattern (one round-trip regardless of how many
// services / events are in the slice). Each event's
// RecentDeploy points at the most recent deploy of that
// service whose first_seen falls in
// [event.startedAt-lookback, event.startedAt]. Uses the
// effectiveVersionExpr chain (v0.5.283) so Helm-only
// installs (app.kubernetes.io/version label) and image-tag
// fallbacks correlate too, not just bare service.version.
func (s *Store) EnrichAnomaliesWithDeploys(ctx context.Context, events []AnomalyEvent, lookback time.Duration) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	services := map[string]struct{}{}
	var minStarted, maxStarted int64
	for i, e := range events {
		if e.Service == "" {
			continue
		}
		services[e.Service] = struct{}{}
		if i == 0 || e.StartedAt < minStarted {
			minStarted = e.StartedAt
		}
		if e.StartedAt > maxStarted {
			maxStarted = e.StartedAt
		}
	}
	if len(services) == 0 {
		return events
	}
	svcList := make([]any, 0, len(services))
	for s := range services {
		svcList = append(svcList, s)
	}
	from := time.Unix(0, minStarted).Add(-lookback)
	to := time.Unix(0, maxStarted)

	holders := ""
	for i := range svcList {
		if i > 0 {
			holders += ","
		}
		holders += "?"
	}
	// v0.5.286 — uses effectiveVersionExpr (the same Helm /
	// image-tag / placeholder-filtered chain GetRecentDeploys
	// uses) so the correlation finds deploys even when
	// service.version stays at "0.0.1-SNAPSHOT" or the
	// pipeline only ships labels via Helm.
	sql := `
		SELECT service_name,
		       ` + effectiveVersionExpr + ` AS version,
		       toUnixTimestamp64Nano(min(time))                 AS first_seen_ns
		FROM spans
		WHERE service_name IN (` + holders + `)
		  AND time >= ? AND time <= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
		GROUP BY service_name, version
		HAVING version != ''
		ORDER BY service_name, first_seen_ns ASC
		SETTINGS max_execution_time = 10`
	args := append([]any{}, svcList...)
	args = append(args, from, to)
	rows, err := s.telemetryReadConn().Query(ctx, sql, args...)
	if err != nil {
		return events
	}
	defer rows.Close()
	type d struct {
		version string
		ns      int64
	}
	byService := map[string][]d{}
	for rows.Next() {
		var svc, ver string
		var ns int64
		if err := rows.Scan(&svc, &ver, &ns); err != nil {
			return events
		}
		byService[svc] = append(byService[svc], d{ver, ns})
	}
	if err := rows.Err(); err != nil {
		return events
	}
	lookbackNs := int64(lookback)
	for i := range events {
		list := byService[events[i].Service]
		if len(list) == 0 {
			continue
		}
		var pick *d
		for j := len(list) - 1; j >= 0; j-- {
			if list[j].ns > events[i].StartedAt {
				continue
			}
			if list[j].ns < events[i].StartedAt-lookbackNs {
				break
			}
			pick = &list[j]
			break
		}
		if pick != nil {
			events[i].RecentDeploy = &RecentDeploy{
				Version:    pick.version,
				TimeUnixNs: pick.ns,
				AgeSeconds: (events[i].StartedAt - pick.ns) / 1e9,
			}
		}
	}
	return events
}
// CalleesOf returns services that `service` calls (outgoing dependency view).
func (s *Store) CalleesOf(ctx context.Context, service string, since time.Duration) ([]ServiceEdgeStats, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT peer_service AS callee,
		       count() AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6 AS avg_ms,
		       quantile(0.99)(duration) / 1e6 AS p99_ms
		FROM spans
		WHERE time >= ?
		  AND service_name = ?
		  AND peer_service != ''
		  AND kind IN ('client', 'producer')
		GROUP BY callee
		ORDER BY calls DESC
		-- v0.9.231 (scale-audit) — had neither, so it inherited the 60s
		-- connection default while the only caller keeps just the top 5
		-- (api.go, root-cause callees). Matches the sibling query in that
		-- same handler, which already carries LIMIT + a 5s ceiling.
		LIMIT 20
		SETTINGS max_execution_time = 5`, cutoff, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdgeStats
	for rows.Next() {
		var e ServiceEdgeStats
		if err := rows.Scan(&e.Service, &e.Calls, &e.ErrorRate, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
