package logstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// clickhouse_fields_test.go — v0.10.280.

func TestCHKeySampleSQLIsBounded(t *testing.T) {
	for _, want := range []string{"time >= ?", "time < ?", "LIMIT 200000", "LIMIT ?", "max_execution_time", "arrayJoin("} {
		if !strings.Contains(chLogsKeySampleSQL, want) {
			t.Errorf("örnekleme SQL'i %q taşımalı (tam tarama yasağı)", want)
		}
	}
	if strings.Contains(chLogsKeySampleSQL, "coremetry.") {
		t.Error("telemetri tablosu ŞEMASIZ anılmalı (CLAUDE.md)")
	}
}

// TestCHFixedFieldsResolveToColumns — panelde gösterilen sabit adlar logql
// hedefinde KOLONA bağlanmalı (attr lookup'a düşen bir ad, panelden tıklanınca
// sessizce 0 satır verir).
func TestCHFixedFieldsResolveToColumns(t *testing.T) {
	for _, f := range chLogsFixedFields {
		ref := chstore.LogQueryTarget.Resolve(f.Name)
		if len(ref.Args) != 0 || strings.Contains(ref.Expr, "indexOf(attr_keys, ?)") {
			t.Errorf("%q sabit alan ama attr lookup'a düşüyor: %s", f.Name, ref.Expr)
		}
	}
}
