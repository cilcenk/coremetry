package chstore

import (
	"context"
)

// Thanos multi-cluster config lives under the "thanos_clusters"
// key in system_settings (v0.8.575, audit: docs/audit/
// thanos-multicluster-metrics-audit.md §2). The thanos.Service
// owns marshal/unmarshal — chstore only stores the bytes (tempo.go
// precedent), so the column shape stays stable as the cluster
// list schema evolves.
const thanosKey = "thanos_clusters"

// GetThanosSettingsRaw returns the saved JSON blob for the remote
// cluster list, or nil if none persisted yet.
func (s *Store) GetThanosSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, thanosKey)
}

// PutThanosSettingsRaw overwrites the saved JSON blob. Caller
// marshals the typed Settings struct so chstore stays untyped.
func (s *Store) PutThanosSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, thanosKey, raw)
}
