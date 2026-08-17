package chstore

import (
	"context"
)

// VictoriaMetrics read-backend config lives under the
// "victoria_metrics" key in system_settings (v0.9.1150). Same posture as
// tempoKey: the vmetrics.Service owns marshal / unmarshal, chstore only
// stores the bytes, so the column shape stays stable however the config
// struct evolves.
const vmetricsKey = "victoria_metrics"

// GetVMetricsSettingsRaw returns the saved JSON blob for the
// VictoriaMetrics read backend, or nil when nothing has been persisted
// yet. The caller (vmetrics.Service.LoadPersisted) does the decode.
func (s *Store) GetVMetricsSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, vmetricsKey)
}

// PutVMetricsSettingsRaw overwrites the saved JSON blob. Caller marshals
// the typed Settings struct so chstore stays untyped.
func (s *Store) PutVMetricsSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, vmetricsKey, raw)
}
