package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// metricresolve.go — "Every metric is a doorway" Phase D, increment 4
// (v0.8.53). GET /api/metrics/resolve is the single server-side resolution
// endpoint behind the MetricQuery descriptor: the frontend encodes a panel's
// descriptor with the SAME base64url(JSON) codec it uses for /explore deep
// links, sends it as ?m=, and the store's ResolveMetricQuery turns it into a
// tier-selected (spanmetrics) or two-level (tracemetrics) ClickHouse read.
// Read-only — no role gate beyond the global auth middleware, same as
// /api/metrics/query.

// metricDescriptor is the backend view of the frontend MetricQuery
// (frontend/src/lib/metricQuery.ts). Only the fields the resolver needs are
// decoded; viz/unit/step/range are frontend display concerns (the resolved
// from/to/step ride as their own query params, exactly like /api/metrics/query).
type metricDescriptor struct {
	Source  string            `json:"source"`
	Metric  string            `json:"metric"`
	Agg     string            `json:"agg"`
	Filters map[string]string `json:"filters"`
	GroupBy []string          `json:"groupBy"`
}

// decodeMetricDescriptor base64url-decodes the ?m= param and unmarshals it. The
// frontend strips '=' padding (RawURLEncoding). Returns an error on malformed
// input so the handler can 400 rather than serve an empty chart.
func decodeMetricDescriptor(m string) (metricDescriptor, error) {
	var d metricDescriptor
	if m == "" {
		return d, fmt.Errorf("missing m param")
	}
	raw, err := base64.RawURLEncoding.DecodeString(m)
	if err != nil {
		return d, fmt.Errorf("m is not base64url: %w", err)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("m is not a valid descriptor: %w", err)
	}
	if d.Metric == "" || d.Agg == "" {
		return d, fmt.Errorf("descriptor missing metric/agg")
	}
	return d, nil
}

func (s *Server) resolveMetric(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m := q.Get("m")
	desc, err := decodeMetricDescriptor(m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	step, _ := strconv.Atoi(q.Get("step"))
	exemplars := q.Get("exemplars") == "1" || q.Get("exemplars") == "true"

	// Cache key hashes ALL inputs. m is base64url(JSON of the whole
	// descriptor) — already a full, collision-free digest of source/metric/
	// agg/filters/groupBy — so embedding it raw is correct (not a length
	// collapse). from/to bucketed to the minute to share a warm entry across
	// a polling window.
	key := fmt.Sprintf("metric-resolve:m=%s:from=%d:to=%d:step=%d:ex=%t",
		m, from.Unix()/60, to.Unix()/60, step, exemplars)

	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		return s.store.ResolveMetricQuery(ctx, chstore.MetricResolveQuery{
			Source:           desc.Source,
			Metric:           desc.Metric,
			Agg:              desc.Agg,
			Filters:          desc.Filters,
			GroupBy:          desc.GroupBy,
			From:             from,
			To:               to,
			StepSeconds:      step,
			IncludeExemplars: exemplars,
		})
	})
}
