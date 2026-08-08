package chstore

import (
	"context"
)

// Azure DevOps / TFS connection config lives under the
// "devops_connection" key in system_settings (v0.9.829). Same
// split as tempo.go: devops.Service owns marshal / unmarshal,
// chstore only moves the bytes, so the stored column shape stays
// stable as the config struct grows.
const devopsKey = "devops_connection"

// GetDevOpsSettingsRaw returns the saved JSON blob, or nil when
// nothing has been persisted yet. devops.Service.LoadPersisted
// does the decode.
func (s *Store) GetDevOpsSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, devopsKey)
}

// PutDevOpsSettingsRaw overwrites the saved JSON blob.
func (s *Store) PutDevOpsSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, devopsKey, raw)
}
