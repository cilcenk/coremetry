package chstore

import (
	"strings"
	"testing"
)

// v0.10.553 — messaging Top-ops: operation kırılımı okuma-anında coalesce
// (yeni semconv → eski): messaging.operation.type → messaging.operation.name →
// messaging.operation; ingest'te ad dayatması yok. Sınırlar: ham spans ama
// zaman-sınırlı + LIMIT + max_execution_time (mevcut sorgunun aynısı).
func TestMsgTopOpsSQL(t *testing.T) {
	sql := msgTopOpsSQL("coalesce(x, 'unknown')")
	i1 := strings.Index(sql, "'messaging.operation.type'")
	i2 := strings.Index(sql, "'messaging.operation.name'")
	i3 := strings.Index(sql, "'messaging.operation')")
	if i1 < 0 || i2 < 0 || i3 < 0 || !(i1 < i2 && i2 < i3) {
		t.Fatalf("coalesce sırası yeni→eski olmalı: %d %d %d\n%s", i1, i2, i3, sql)
	}
	for _, want := range []string{"FROM spans", "time >= ? AND time <= ?", "msg_system = ?", "coalesce(x, 'unknown') = ?",
		"GROUP BY stmt, operation", "LIMIT 20", "max_execution_time = 15"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL %q taşımalı:\n%s", want, sql)
		}
	}
}
