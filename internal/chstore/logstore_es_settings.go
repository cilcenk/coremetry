package chstore

import (
	"context"
)

// Logstore (ES) backend config lives under the "logstore_es" key in
// system_settings (v0.8.232). The logstore.ESManager owns marshal /
// unmarshal — chstore only stores the bytes so the column shape stays
// stable regardless of how the config struct evolves. Mirrors the
// tempo.go pattern.
const logstoreESKey = "logstore_es"

// GetLogstoreESSettingsRaw returns the saved JSON blob for the
// UI-managed logstore config, or nil if none has been persisted yet
// (env/YAML config stays authoritative until the first admin save).
func (s *Store) GetLogstoreESSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, logstoreESKey)
}

// PutLogstoreESSettingsRaw overwrites the saved JSON blob. Caller
// marshals the typed ESSettings struct so chstore stays untyped.
func (s *Store) PutLogstoreESSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, logstoreESKey, raw)
}
