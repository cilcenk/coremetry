package chstore

import (
	"strings"
	"testing"
)

// v0.10.550 — caller MV'den topic'in üretici/tüketici servis listesi (Kafka client
// metrikleri VM sorgusunun kapsamı). Sınır sözleşmesi: MV (ham spans DEĞİL),
// zaman-sınırlı WHERE, LIMIT, max_execution_time (CLAUDE.md CH bounds).
func TestMsgCallerServicesSQLBounds(t *testing.T) {
	sql := msgCallerServicesSQL
	for _, want := range []string{"FROM messaging_caller_summary_5m", "time_bucket >= ? AND time_bucket < ?",
		"msg_system = ? AND cluster = ? AND destination = ?", "GROUP BY service_name, role", "LIMIT", "max_execution_time"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL %q taşımalı:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "FROM spans") {
		t.Fatal("ham spans yasak")
	}
}
