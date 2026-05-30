package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.7.28 — putRetention now writes an audit entry + kicks an immediate
// enforcer sweep so a retention REDUCTION reclaims old day-partitions at once
// instead of waiting for the worker's hourly tick. retentionDetails builds the
// audit detail string; it must list ONLY the fields the operator actually set,
// because empty fields preserve the prior value (they're not part of the
// mutation) — emitting "logs=" for an untouched signal would mislead the audit
// trail. Table-driven per CLAUDE.md #11.
func TestRetentionDetails(t *testing.T) {
	tests := []struct {
		name string
		sp   chstore.RetentionSpec
		want string
	}{
		{"only spans", chstore.RetentionSpec{Spans: "1d"}, "spans=1d"},
		{"spans + logs", chstore.RetentionSpec{Spans: "1d", Logs: "7d"}, "spans=1d logs=7d"},
		{
			"all four in order",
			chstore.RetentionSpec{Spans: "1d", Logs: "7d", Metrics: "30d", Profiles: "48h"},
			"spans=1d logs=7d metrics=30d profiles=48h",
		},
		{"empty fields are omitted (not echoed blank)", chstore.RetentionSpec{Metrics: "14d"}, "metrics=14d"},
		{"nothing set → empty string", chstore.RetentionSpec{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retentionDetails(tt.sp); got != tt.want {
				t.Errorf("retentionDetails(%+v) = %q, want %q", tt.sp, got, tt.want)
			}
		})
	}
}
