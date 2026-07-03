package chstore

import (
	"context"
)

// Topology hidden-pattern config lives under the "topology_hidden" key
// in system_settings (v0.8.241, operator-requested: kafka:log* /
// kafka:bsa* nodes must never render). The API layer owns marshal /
// unmarshal — chstore only stores the bytes (tempo.go pattern).
const topologyHiddenKey = "topology_hidden"

// GetTopologyHiddenRaw returns the saved JSON blob for the hidden
// pattern list, or nil when none has been persisted yet (the API layer
// falls back to its seeded defaults).
func (s *Store) GetTopologyHiddenRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, topologyHiddenKey)
}

// PutTopologyHiddenRaw overwrites the saved JSON blob.
func (s *Store) PutTopologyHiddenRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, topologyHiddenKey, raw)
}
