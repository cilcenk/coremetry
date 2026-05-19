package chstore

import "context"

// Pipeline (v0.5.263) — operator-defined ingest-time drop /
// enrich rules. Stored as a single JSON blob in system_settings
// under "pipeline_rules"; the pipeline package owns marshal /
// unmarshal so chstore stays untyped here.
const pipelineRulesKey = "pipeline_rules"

func (s *Store) GetPipelineRulesRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, pipelineRulesKey)
}

func (s *Store) PutPipelineRulesRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, pipelineRulesKey, raw)
}
