package chstore

import (
	"context"
)

// Tempo backend config lives under the "tempo" key in
// system_settings. The tempo.Service owns marshal / unmarshal —
// chstore only stores the bytes so the column shape stays stable
// regardless of how the config struct evolves.
const tempoKey = "tempo"

// GetTempoSettingsRaw returns the saved JSON blob for the tempo
// backend config, or nil if no settings have been persisted yet.
// The caller (tempo.Service.LoadPersisted) does the decode.
func (s *Store) GetTempoSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, tempoKey)
}

// PutTempoSettingsRaw overwrites the saved JSON blob. Caller
// marshals the typed Settings struct so chstore stays untyped.
func (s *Store) PutTempoSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, tempoKey, raw)
}
