package config

import (
	"os"
	"testing"
)

// v0.10.695 — BatchSize varsayılanı 50_000 (operatör kararı; CH denetimi 646
// öneri 1). Env yine kazanır; geçersiz env varsayılanı bozmaz.
func TestIngestBatchSizeDefault(t *testing.T) {
	_ = os.Unsetenv("COREMETRY_INGEST_BATCH_SIZE")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ingestion.BatchSize != 50_000 {
		t.Fatalf("varsayılan BatchSize 50000 olmalı: %d", cfg.Ingestion.BatchSize)
	}
	t.Setenv("COREMETRY_INGEST_BATCH_SIZE", "20000")
	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ingestion.BatchSize != 20_000 {
		t.Fatalf("env geçersiz kılmalı: %d", cfg.Ingestion.BatchSize)
	}
}
