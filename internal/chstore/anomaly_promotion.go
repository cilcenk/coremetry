package chstore

import (
	"context"
	"encoding/json"
)

// AnomalyPromotionConfig drives the evaluator's
// promoteStrongAnomalies sweep (v0.5.59). Was hard-coded
// constants in the binary until v0.5.70 — operators couldn't
// turn it off when the detector got chatty, or tighten the
// signal floor for a noisy fleet, without a redeploy. Now
// stored as a JSON blob under system_settings key
// "anomaly_promotion" and read every sweep through a tiny
// in-process memo.
//
// Zero-value config is the v0.5.59 default (enabled,
// peak ratio 5×, 5min sustained, 10 events) — operators who
// never visit the settings page keep the old behaviour.
type AnomalyPromotionConfig struct {
	// Enabled — master switch. False means the sweep runs
	// but no promotion happens; anomalies stay on
	// /anomalies for manual triage. Operators with a very
	// noisy detector tune this off until they've calibrated
	// the detector itself.
	Enabled bool `json:"enabled"`
	// MinPeakRatio — baseline-relative ratio gate. 5× means
	// the pattern is occurring at least 5× more than its
	// rolling baseline before becoming pageable.
	MinPeakRatio float64 `json:"minPeakRatio"`
	// MinSustainedSec — how long since started_at the
	// pattern must have been observed before it gets
	// promoted. Filters out one-tick flares.
	MinSustainedSec int `json:"minSustainedSec"`
	// MinCount — absolute volume floor. A 100× ratio on 2
	// occurrences is meaningless.
	MinCount uint64 `json:"minCount"`

	// Seasonal* — consumed by the anomaly DETECTOR's seasonal
	// baseline (internal/anomaly), NOT the promotion sweep. They
	// ride this blob so the anomaly feature keeps ONE operator
	// settings surface instead of a second key/route (v0.8.250).
	// Zero/absent → the detector's compile-time defaults via
	// GetAnomalyPromotion's patch below, so an unedited install
	// keeps the shipped behaviour. Frontend binding is a separate
	// follow-up; the backend persistence is wired now.
	//
	// SeasonalDays — days of same-slot history the baseline learns
	// from (default 14).
	SeasonalDays int `json:"seasonalDays"`
	// SeasonalMinSamples — min same-slot samples before the
	// seasonal baseline is trusted over the flat 24h window
	// (default 4).
	SeasonalMinSamples int `json:"seasonalMinSamples"`
	// SeasonalNeighborBuckets — ± same-class 5-min neighbour
	// buckets folded into the baseline to beat sample scarcity on
	// thin off-peak slots (default 3 ⇒ ±15 min).
	SeasonalNeighborBuckets int `json:"seasonalNeighborBuckets"`
}

// Defaults — match the hard-coded constants from v0.5.59 so
// the upgrade keeps the same behaviour for installs that
// haven't visited the new settings page.
func DefaultAnomalyPromotion() AnomalyPromotionConfig {
	return AnomalyPromotionConfig{
		Enabled:         true,
		MinPeakRatio:    5.0,
		MinSustainedSec: 5 * 60,
		MinCount:        10,
		// Mirror the anomaly-detector compile-time defaults
		// (internal/anomaly: seasonalDays / seasonalMinSamples /
		// seasonalNeighborBuckets). chstore can't import anomaly
		// (that package imports chstore), so the values are
		// duplicated here; the detector re-clamps on read, so a
		// drift can't push the CH query out of bounds.
		SeasonalDays:            14,
		SeasonalMinSamples:      4,
		SeasonalNeighborBuckets: 3,
	}
}

const anomalyPromotionKey = "anomaly_promotion"

// GetAnomalyPromotion returns the persisted config, or the
// defaults when nothing's saved. Soft-fails to defaults on
// CH error so a transient blip doesn't accidentally disable
// promotion in a long-running evaluator.
func (s *Store) GetAnomalyPromotion(ctx context.Context) AnomalyPromotionConfig {
	raw, err := s.GetSetting(ctx, anomalyPromotionKey)
	if err != nil || len(raw) == 0 {
		return DefaultAnomalyPromotion()
	}
	var c AnomalyPromotionConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultAnomalyPromotion()
	}
	// Patch missing / zero-but-load-bearing fields back to
	// defaults so a partial save (e.g. a future field that
	// existing rows don't carry) doesn't pin promotion to 0.
	d := DefaultAnomalyPromotion()
	if c.MinPeakRatio <= 0 {
		c.MinPeakRatio = d.MinPeakRatio
	}
	if c.MinSustainedSec <= 0 {
		c.MinSustainedSec = d.MinSustainedSec
	}
	if c.MinCount == 0 {
		c.MinCount = d.MinCount
	}
	// Seasonal knobs: zero/absent (older saved rows written before
	// v0.8.250 don't carry these fields, or a partial PUT omits
	// them) → detector defaults, so the GET always returns a usable
	// value and the detector never sees a zero. An `int` can't tell
	// "absent" from "explicit 0", so 0 is treated as default for ALL
	// three — including SeasonalNeighborBuckets: the widening is the
	// whole point of the fix, so an install that saved this blob
	// pre-v0.8.250 must inherit the ±3 window, not silently disable
	// it. (An operator who truly wants exact-slot-only reverts via
	// the compile-time default, not a 0 here.)
	if c.SeasonalDays <= 0 {
		c.SeasonalDays = d.SeasonalDays
	}
	if c.SeasonalMinSamples <= 0 {
		c.SeasonalMinSamples = d.SeasonalMinSamples
	}
	if c.SeasonalNeighborBuckets <= 0 {
		c.SeasonalNeighborBuckets = d.SeasonalNeighborBuckets
	}
	return c
}

// SaveAnomalyPromotion writes the config under system_settings.
// Backed by the same key/value table SMTP credentials and
// retention overrides use, so it survives restart without
// any new schema.
func (s *Store) SaveAnomalyPromotion(ctx context.Context, c AnomalyPromotionConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, anomalyPromotionKey, raw)
}
