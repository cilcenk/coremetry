package logstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// CHStore adapts the existing ClickHouse logs table to the LogStore
// interface. Pure delegation — chstore.GetLogs already takes a similar
// filter shape.
type CHStore struct {
	store *chstore.Store
}

func NewCH(store *chstore.Store) *CHStore { return &CHStore{store: store} }

func (s *CHStore) Backend() string { return "clickhouse" }

// Ping delegates to the wrapped chstore — the same CH connection the
// rest of the app uses, so no separate liveness contract.
func (s *CHStore) Ping(ctx context.Context) error { return s.store.Ping(ctx) }

func (s *CHStore) Search(ctx context.Context, f Filter) (*Page, error) {
	rows, total, err := s.store.GetLogs(ctx, chstore.LogFilter{
		Service:     f.Service,
		Search:      f.Search,
		From:        f.From,
		To:          f.To,
		SeverityMin: f.SeverityMin,
		TraceID:     f.TraceID,
		SpanID:      f.SpanID,
		Limit:       f.Limit,
		Offset:      f.Offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*LogRecord, 0, len(rows))
	for _, l := range rows {
		out = append(out, &LogRecord{
			ID:                 int64(l.ID),
			Timestamp:          l.Timestamp,
			Severity:           l.SeverityNumber,
			SeverityText:       l.SeverityText,
			Body:               l.Body,
			ServiceName:        l.ServiceName,
			TraceID:            l.TraceID,
			SpanID:             l.SpanID,
			Attributes:         l.Attributes,
			ResourceAttributes: l.ResourceAttributes,
		})
	}
	return &Page{Total: int(total), Logs: out}, nil
}

// Histogram buckets log volume server-side via the same logs
// table. Whitelisted groupBy options ("service", "severity",
// or "" for total) map to indexed LowCardinality columns so the
// query stays partition-pruned + index-friendly even at billion
// log/day. Unknown groupBy collapses to a single _total series
// rather than failing — operator notices empty break-down and
// can pick a different field.
func (s *CHStore) Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error) {
	if bucketSec <= 0 {
		bucketSec = 30
	}
	groupExpr := "'_total'"
	switch groupBy {
	case "service":
		groupExpr = "service_name"
	case "severity":
		groupExpr = "if(severity_text != '', severity_text, toString(severity_num))"
	}

	from, to := f.From, f.To
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	args := []any{from, to}
	wc := "time >= ? AND time <= ?"
	if f.Cluster != "" {
		// v0.5.471 — coalesce the three resource-attribute paths
		// the chstore.spans table also uses (clusterDeriveExpr).
		// logs reuses the same key conventions emitted by OTel
		// SDKs at init time.
		wc += ` AND coalesce(
			nullIf(resource_attributes['k8s.cluster.name'], ''),
			nullIf(resource_attributes['openshift.cluster.name'], ''),
			nullIf(resource_attributes['cluster'], ''),
			''
		) = ?`
		args = append(args, f.Cluster)
	}
	if f.Service != "" {
		wc += " AND service_name = ?"
		args = append(args, f.Service)
	}
	if f.Search != "" {
		// multiSearchAnyCaseInsensitive uses the tokenbf_v1 index
		// on body via the per-token bloom filter, so granules
		// that don't contain the search substring are pruned
		// before the row-level match runs. positionCaseInsensitive
		// (the obvious choice) cannot use the index — at
		// billion-log/day scale that's a full scan.
		wc += " AND multiSearchAnyCaseInsensitive(body, [?])"
		args = append(args, f.Search)
	}
	if f.SeverityMin > 0 {
		wc += " AND severity_num >= ?"
		args = append(args, f.SeverityMin)
	}
	if f.TraceID != "" {
		wc += " AND trace_id = ?"
		args = append(args, f.TraceID)
	}
	// v0.5.271 — multi-trace filter for the DQL cross-signal
	// join. AND-merge with the single-trace TraceID; mostly
	// they're mutually exclusive in practice (UI uses one,
	// join executor uses the other).
	if len(f.TraceIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(f.TraceIDs)), ",")
		wc += " AND trace_id IN (" + placeholders + ")"
		for _, id := range f.TraceIDs {
			args = append(args, id)
		}
	}

	// Top-20 groups by total count (mirrors the ES path's
	// terms.size:20 cap). Without this a high-cardinality group
	// like service_name with 10k+ services would return 10k ×
	// N_buckets rows; the chart can't render that anyway.
	sql := fmt.Sprintf(`
		WITH top_groups AS (
		  SELECT %s AS g, count() AS c
		  FROM logs WHERE %s
		  GROUP BY g
		  ORDER BY c DESC
		  LIMIT 20
		)
		SELECT %s AS g,
		       toStartOfInterval(time, INTERVAL %d SECOND) AS bucket,
		       count() AS c
		FROM logs
		WHERE %s AND (%s) GLOBAL IN (SELECT g FROM top_groups)
		GROUP BY g, bucket
		ORDER BY g, bucket
		SETTINGS max_execution_time = 30,
		         distributed_product_mode = 'global'`,
		groupExpr, wc,
		groupExpr, bucketSec, wc, groupExpr)
	// The IN-subquery references the same args twice (top_groups
	// CTE + outer SELECT), so we duplicate the binding list.
	args = append([]any{}, append(args, args...)...)

	rows, err := s.store.Conn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*LogSeries{}
	order := []string{}
	for rows.Next() {
		var g string
		var t time.Time
		var c uint64
		if err := rows.Scan(&g, &t, &c); err != nil {
			return nil, err
		}
		s, ok := byName[g]
		if !ok {
			s = &LogSeries{Name: g}
			byName[g] = s
			order = append(order, g)
		}
		s.Points = append(s.Points, LogPoint{T: t.UnixNano(), V: int64(c)})
	}
	out := make([]LogSeries, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, rows.Err()
}

// CountPatterns matches each detector pattern against the raw
// logs table. CH path runs sequentially because each query is
// already cheap behind the tokenbf_v1 skip index — granules
// without any of the tokens get pruned before the per-row regex
// eval. ES batches via _msearch (see elasticsearch.go) because
// its per-pattern cost dominates the round-trip cost there.
func (s *CHStore) CountPatterns(
	ctx context.Context,
	pats []PatternSpec,
	curStart, baseStart, now time.Time,
) ([]PatternStats, error) {
	out := make([]PatternStats, len(pats))
	for i, pat := range pats {
		stats, err := s.countOnePattern(ctx, pat, curStart, baseStart, now)
		if err != nil {
			return out, err
		}
		out[i] = stats
	}
	return out, nil
}

func (s *CHStore) countOnePattern(
	ctx context.Context,
	pat PatternSpec,
	curStart, baseStart, now time.Time,
) (PatternStats, error) {
	var out PatternStats
	tokensSQL := chBuildTokenLiteral(pat.Tokens)
	where := "time >= ? AND time < ? AND match(body, ?)"
	if tokensSQL != "" {
		where = "time >= ? AND time < ? AND multiSearchAnyCaseInsensitive(body, " + tokensSQL + ") AND match(body, ?)"
	}
	sql := `
		SELECT
		  countIf(time >= ?)                                      AS cur,
		  countIf(time <  ?)                                      AS base,
		  anyHeavyIf(service_name, time >= ?)                     AS svc,
		  anyIf(body, time >= ?)                                  AS sample,
		  toUnixTimestamp64Nano(maxIf(time, time >= ?))           AS last_ns
		FROM logs
		WHERE ` + where
	err := s.store.Conn().QueryRow(ctx, sql,
		curStart, curStart, curStart, curStart, curStart,
		baseStart, now, pat.Regex,
	).Scan(&out.Cur, &out.Base, &out.Service, &out.Sample, &out.LastSeenNs)
	if err != nil {
		return PatternStats{}, err
	}
	// v0.5.287 — per-service breakdown for the current window
	// (top 5). Only fires when cur > 0 so the no-match common
	// case stays one query. The WHERE clause + tokenbf prefilter
	// matches the aggregate above, so the same granules are
	// pruned + the LowCardinality service_name GROUP BY is
	// in-memory after the existing scan.
	if out.Cur > 0 {
		topSQL := `
			SELECT service_name, count() AS cnt
			FROM logs
			WHERE time >= ? AND time < ? AND ` +
			func() string {
				if tokensSQL != "" {
					return "multiSearchAnyCaseInsensitive(body, " + tokensSQL + ") AND match(body, ?)"
				}
				return "match(body, ?)"
			}() + `
			GROUP BY service_name
			ORDER BY cnt DESC
			LIMIT 5
			SETTINGS max_execution_time = 5`
		topRows, terr := s.store.Conn().Query(ctx, topSQL, curStart, now, pat.Regex)
		if terr == nil {
			defer topRows.Close()
			for topRows.Next() {
				var svc string
				var cnt uint64
				if err := topRows.Scan(&svc, &cnt); err != nil {
					break
				}
				out.TopServices = append(out.TopServices, PatternServiceHit{
					Service: svc, Count: cnt,
				})
			}
		}
	}
	return out, nil
}

// SignificantPatterns has no efficient native equivalent on CH
// at billion-row scale (would need a tokenize + frequency-vs-
// baseline pass per query, which doesn't reuse any of CH's
// indexes). Returns nil so the API surface stays uniform; the
// frontend already hides the panel on empty results. Operators
// on the CH backend get coverage via the curated regex detector
// + saved-search alerts.
// v0.5.298 — CH backend's answer to ES `significant_text`: sample
// foreground + background slices, tokenise client-side, score
// each token by foreground share vs background share. NOT a
// drop-in replacement for ES `significant_text` (no per-shard
// chi-square scoring, no `filter_duplicate_text`, no native
// background_filter) — but at billion-log scale it gives the
// operator the same "what's rare-but-rising right now" signal
// using only CH's existing tokenbf_v1 skip index + a small
// in-Go counting pass. Two queries, both bounded by LIMIT, so
// the worst case is fixed cost regardless of underlying log
// volume.
//
// Tokenisation rules:
//   • Lowercase
//   • Split on whitespace + brackets/quotes; keep dotted Java
//     class names + URL paths + colon-suffixed tags ("panic:"
//     "ERROR:") intact.
//   • Drop tokens shorter than 3 chars (would just be the/and/
//     is etc. and pure-numeric tokens are dropped separately).
//   • Drop tokens that are pure numbers (timestamps / counters)
//     — they're noise, not signal.
//
// Score = (foreground share) / (background share + epsilon).
// Cap min foreground count at 3 so a single rare occurrence
// doesn't dominate (mirrors the curated-detector floor).
func (s *CHStore) SignificantPatterns(
	ctx context.Context,
	curStart, baseStart, now time.Time,
	topN int,
) ([]SignificantPattern, error) {
	if topN <= 0 {
		topN = 25
	}
	if topN > 100 {
		topN = 100
	}
	curBodies, err := s.sampleBodies(ctx, curStart, now, 5000)
	if err != nil {
		return nil, err
	}
	if len(curBodies) == 0 {
		return nil, nil
	}
	baseBodies, err := s.sampleBodies(ctx, baseStart, curStart, 20000)
	if err != nil {
		return nil, err
	}
	curFreq := tokenFreq(curBodies)
	baseFreq := tokenFreq(baseBodies)
	curTotal := float64(len(curBodies))
	baseTotal := float64(len(baseBodies))
	if baseTotal < 1 {
		baseTotal = 1
	}

	type scored struct {
		token string
		cur   uint64
		base  uint64
		score float64
	}
	out := make([]scored, 0, len(curFreq))
	const epsilon = 0.5 // smoothing — treat absent-from-baseline as half-occurrence
	for tok, n := range curFreq {
		if n < 3 {
			continue
		}
		bn := baseFreq[tok]
		curShare := float64(n) / curTotal
		baseShare := (float64(bn) + epsilon) / baseTotal
		score := curShare / baseShare
		if score < 1.5 {
			continue // not over-represented enough to surface
		}
		out = append(out, scored{token: tok, cur: n, base: bn, score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].cur > out[j].cur
	})
	if len(out) > topN {
		out = out[:topN]
	}
	result := make([]SignificantPattern, len(out))
	for i, r := range out {
		result[i] = SignificantPattern{
			Token: r.token, DocCount: r.cur, BgCount: r.base, Score: r.score,
		}
	}
	return result, nil
}

// sampleBodies pulls a randomised slice of log bodies from CH
// within [from, to). LIMIT is the hard cap; the sampler uses
// CH's built-in SAMPLE clause when feasible, falling back to
// ORDER BY rand() LIMIT N when the table isn't SAMPLE-able.
// Bounded by SETTINGS max_execution_time so a heavy partition
// doesn't stall the detector.
func (s *CHStore) sampleBodies(ctx context.Context, from, to time.Time, limit int) ([]string, error) {
	rows, err := s.store.Conn().Query(ctx, `
		SELECT body
		FROM logs
		WHERE time >= ? AND time < ? AND body != ''
		ORDER BY rand()
		LIMIT ?
		SETTINGS max_execution_time = 8
	`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, limit)
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// tokenFreq splits each body into tokens (see SignificantPatterns
// docs for the rule set) and returns a token→count map.
func tokenFreq(bodies []string) map[string]uint64 {
	freq := make(map[string]uint64, 1024)
	seenInDoc := make(map[string]struct{}, 64)
	for _, body := range bodies {
		for k := range seenInDoc {
			delete(seenInDoc, k)
		}
		// Walk runes; collect token slices, then commit.
		start := -1
		for i := 0; i <= len(body); i++ {
			var c byte
			if i < len(body) {
				c = body[i]
			}
			if isTokenChar(c) {
				if start < 0 {
					start = i
				}
				continue
			}
			if start >= 0 {
				tok := strings.ToLower(body[start:i])
				start = -1
				if tokenWorth(tok) {
					if _, dup := seenInDoc[tok]; !dup {
						seenInDoc[tok] = struct{}{}
						freq[tok]++
					}
				}
			}
		}
	}
	return freq
}

// isTokenChar — keep dotted paths (com.example.X), URL paths
// (/api/v1/foo), colon-suffix tags (panic:) intact. Whitespace +
// brackets + quotes + commas + semicolons split.
func isTokenChar(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', '\v',
		'(', ')', '[', ']', '{', '}', '<', '>',
		'"', '\'', '`',
		',', ';', '|', '&', '=', '!', '?', '*', '#':
		return false
	}
	return true
}

// tokenWorth drops tokens that contribute noise rather than
// signal: too short, pure numeric (timestamps / counters), or
// looks like an opaque hex/uuid.
func tokenWorth(t string) bool {
	if len(t) < 3 {
		return false
	}
	allDigit := true
	allHex := true
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c < '0' || c > '9' {
			allDigit = false
		}
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || c == '-'
		if !hex {
			allHex = false
		}
	}
	if allDigit {
		return false
	}
	// Block long hex/uuid-shaped tokens (16+ hex chars). These
	// are typically request IDs / trace IDs / hashes — noise
	// for the "rare keyword" axis.
	if allHex && len(t) >= 16 {
		return false
	}
	return true
}

// chBuildTokenLiteral renders []string as a CH array literal
// `['t1', 't2', …]` for inlining. Tokens are detector-supplied
// (no untrusted input) so we just escape embedded single quotes.
func chBuildTokenLiteral(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = "'" + strings.ReplaceAll(t, "'", "\\'") + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// EQLSearch — CH stub (v0.5.468). EQL is ES-native; CH has no
// equivalent sequence-matching aggregate. Returning a typed
// error lets the handler surface "not supported on this
// backend" cleanly and lets the frontend hide the EQL panel.
func (s *CHStore) EQLSearch(ctx context.Context, q EQLQuery) ([]EQLSequence, error) {
	_ = ctx
	_ = q
	return nil, fmt.Errorf("EQL is Elasticsearch-only (ClickHouse backend has no equivalent)")
}

// Indices — CH stub (v0.5.466). Single physical table on the
// CH backend; per-shard / part-level surface is on the existing
// /admin/clickhouse page. Returning nil makes /admin/elastic
// render its "logs backend isn't Elasticsearch" empty state
// without touching CH-specific plumbing.
func (s *CHStore) Indices(ctx context.Context) ([]IndexInfo, error) {
	_ = ctx
	return nil, nil
}

// FieldValues — CH stub (v0.5.464). The KQL search box's
// field-aware autocomplete is Kibana-flavoured and primarily
// useful on ES installs; CH operators tend to filter via the
// explicit FilterBuilder UI instead. Returning empty makes the
// autocomplete simply not surface on CH backends, which
// degrades gracefully without spurious "no matches" rows.
// A CH-native implementation (SELECT DISTINCT field WHERE
// field LIKE prefix% LIMIT) can land as a follow-up if
// operators report wanting it.
func (s *CHStore) FieldValues(ctx context.Context, field, prefix string, limit int) ([]string, error) {
	_ = ctx
	_ = field
	_ = prefix
	_ = limit
	return nil, nil
}

