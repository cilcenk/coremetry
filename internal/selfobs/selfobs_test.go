package selfobs

import "testing"

// v0.8.x — guards the 2026-05-31 self-observability regression: buildResource
// did resource.Merge(resource.Default(), NewWithAttributes(semconv.SchemaURL,…)).
// When the SDK's bundled semconv schema URL drifted from the imported semconv
// package's (a dependency bump), resource.Merge returned a "conflicting Schema
// URL" error → buildResource failed → selfobs.Init fell back to noop → the
// backend coremetry-api/worker/agent self-telemetry silently stopped emitting.
// The fix builds the custom attrs schemaless so the merge can't conflict.
func TestBuildResourceNoSchemaConflict(t *testing.T) {
	tests := []struct {
		mode    string
		wantSvc string
	}{
		{"all", "coremetry-monolithic"},
		{"api", "coremetry-api"},
		{"worker", "coremetry-worker"},
		{"ingest", "coremetry-ingest"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			res, err := buildResource(tc.mode, "v9.9.9")
			if err != nil {
				t.Fatalf("buildResource(%q) errored (schema-URL conflict regression): %v", tc.mode, err)
			}
			var svc string
			for _, kv := range res.Attributes() {
				if string(kv.Key) == "service.name" {
					svc = kv.Value.AsString()
				}
			}
			if svc != tc.wantSvc {
				t.Errorf("service.name = %q, want %q", svc, tc.wantSvc)
			}
		})
	}
}
