package anomaly

// v0.6.27 — new-template detector. Complements log_patterns.go
// (curated regex against ~20 known production failure shapes)
// with an OPEN-ENDED signal: when the Drain log templater
// (internal/templater) first observes a shape it's never seen
// before, that's an anomaly worth surfacing — operators don't
// know what tomorrow's log line looks like, but they want a
// pinned "this just started happening" feed.
//
// Spike detection (a known template firing 10× more than
// baseline) is intentionally deferred — log_templates rows
// store cumulative total_count + first_seen + last_seen, no
// per-window count column, so spike math needs a schema
// addition. Ship "new" first; revisit spike when the operator
// asks.
//
// Fired anomalies flow through the same UpsertAnomalyEvent
// path as log_pattern + trace_op detections; the /anomalies
// page renders them for free.

import (
	"context"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// LogTemplateAnomaly is one Drain template that crossed into
// existence within `window`. The same fingerprint (template ID)
// surfaces every tick until the row falls outside the window —
// UpsertAnomalyEvent dedupes via ReplacingMergeTree(version).
type LogTemplateAnomaly struct {
	TemplateID  string
	Template    string
	Service     string // first service in the template's seen-services array
	FirstSeenNs int64
	LastSeenNs  int64
	TotalCount  uint64
	Sample      string
}

// DetectNewLogTemplates returns Drain templates whose first_seen
// landed inside [now-window, now]. The templater puller writes
// these rows; we just query them.
//
// Window guidance: the templater puller runs every 5min by
// default (see templater/puller.go), so window=10min covers the
// last two puller cycles — enough to absorb any clock drift
// between puller + recorder. The anomaly_event dedupe collapses
// repeated detections.
func DetectNewLogTemplates(ctx context.Context, store *chstore.Store, window time.Duration) ([]LogTemplateAnomaly, error) {
	if window <= 0 {
		window = 10 * time.Minute
	}
	sinceNs := time.Now().Add(-window).UnixNano()
	tmpls, err := store.ListLogTemplates(ctx, chstore.ListLogTemplatesFilter{
		SinceNs: sinceNs,
		SortBy:  "first_seen",
		Limit:   100,
	})
	if err != nil {
		return nil, err
	}
	out := []LogTemplateAnomaly{}
	for _, t := range tmpls {
		// Filter to templates whose first_seen is in the window —
		// the SinceNs above filters on last_seen, so a template
		// last seen recently but first-seen years ago would also
		// match. We want "born in the window" specifically.
		if t.FirstSeen < sinceNs {
			continue
		}
		svc := ""
		if len(t.Services) > 0 {
			svc = t.Services[0]
		}
		// Template body is the cluster signature with placeholders
		// (e.g. "User <*> logged in from <*>"). Truncate the
		// human-facing pattern field for the anomaly_event ID +
		// rendering — anomaly_events.pattern is a String we don't
		// want unbounded.
		pattern := truncTemplate(t.Template, 160)
		out = append(out, LogTemplateAnomaly{
			TemplateID:  t.ID,
			Template:    pattern,
			Service:     svc,
			FirstSeenNs: t.FirstSeen,
			LastSeenNs:  t.LastSeen,
			TotalCount:  t.TotalCount,
			Sample:      t.Sample,
		})
	}
	return out, nil
}

// truncTemplate truncates the Drain template at the nearest word
// boundary past max bytes; appends an ellipsis when cut.
func truncTemplate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndexAny(s[:max], " \t")
	if cut < max/2 {
		// no good boundary; hard cut
		cut = max
	}
	return s[:cut] + "…"
}
