package chstore

import (
	"context"
	"encoding/json"
)

// KibanaSettings — operator-curated link to an external Kibana
// install so the Logs page can render a "Open in Kibana
// Discover" deep-link per row. Independent of the Coremetry log
// backend (which can still be CH or ES); this is purely a UI
// shortcut for operators who prefer Kibana's deeper Discover
// affordances for ad-hoc log exploration.
type KibanaSettings struct {
	// Enabled — disabled by default. When false the UI hides the
	// per-row "Kibana" link even if BaseURL is set.
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseUrl,omitempty"`
	// DataView — optional Kibana data-view / index-pattern id.
	// When set, the deep-link pins the view so the operator
	// lands on the same indexes Coremetry is reading from
	// instead of Kibana's default. Empty = Kibana picks the
	// default data view, fine for single-pattern installs.
	DataView string `json:"dataView,omitempty"`
}

const kibanaKey = "kibana"

// GetKibana returns the saved settings (or empty struct when
// unconfigured — Enabled stays false so the UI hides the link).
func (s *Store) GetKibana(ctx context.Context) (KibanaSettings, error) {
	var k KibanaSettings
	raw, err := s.GetSetting(ctx, kibanaKey)
	if err != nil {
		return k, err
	}
	if len(raw) == 0 {
		return k, nil
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return k, err
	}
	return k, nil
}

// PutKibana overwrites the saved settings. Admin-gated at the
// HTTP layer.
func (s *Store) PutKibana(ctx context.Context, k KibanaSettings) error {
	raw, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, kibanaKey, raw)
}
