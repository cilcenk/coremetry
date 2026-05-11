package config

import (
	"testing"
	"time"
)

// TestApplyBackgroundDefaults locks in the zero-value → canonical
// fallback for the cadence knobs. Pre-v0.4.95 these were magic
// numbers in main.go (2*time.Minute / 30*time.Second / etc.);
// the audit's "config-ize hardcoded TTLs" hit moved them here.
// Test catches a future contributor who pulls a default out of
// the defaults struct without updating the applier.
func TestApplyBackgroundDefaults(t *testing.T) {
	t.Run("zero values fall back to defaults", func(t *testing.T) {
		b := BackgroundConfig{}
		applyBackgroundDefaults(&b)
		if b.AnomalyInterval != 2*time.Minute {
			t.Errorf("AnomalyInterval: got %v, want 2m", b.AnomalyInterval)
		}
		if b.AnomalyRecordInterval != 1*time.Minute {
			t.Errorf("AnomalyRecordInterval: got %v, want 1m", b.AnomalyRecordInterval)
		}
		if b.AnomalyRecordBackfill != 5*time.Minute {
			t.Errorf("AnomalyRecordBackfill: got %v, want 5m", b.AnomalyRecordBackfill)
		}
		if b.SMTPCacheTTL != 30*time.Second {
			t.Errorf("SMTPCacheTTL: got %v, want 30s", b.SMTPCacheTTL)
		}
		if b.StatusProbeTimeout != 5*time.Second {
			t.Errorf("StatusProbeTimeout: got %v, want 5s", b.StatusProbeTimeout)
		}
	})
	t.Run("explicit values pass through", func(t *testing.T) {
		b := BackgroundConfig{
			AnomalyInterval:    10 * time.Second,
			StatusProbeTimeout: 1 * time.Second,
		}
		applyBackgroundDefaults(&b)
		if b.AnomalyInterval != 10*time.Second {
			t.Errorf("explicit AnomalyInterval clobbered: got %v", b.AnomalyInterval)
		}
		if b.StatusProbeTimeout != 1*time.Second {
			t.Errorf("explicit StatusProbeTimeout clobbered: got %v", b.StatusProbeTimeout)
		}
		// Untouched fields still get defaults.
		if b.AnomalyRecordInterval != 1*time.Minute {
			t.Errorf("AnomalyRecordInterval should default when others set: got %v", b.AnomalyRecordInterval)
		}
	})
}
