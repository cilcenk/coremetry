package chstore

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// v0.10.138 — adım 4 (exceptions dağılımı): tarama SQL'i iki kolon kipinde de
// (ex_* terfi kolonu var/yok) samples ile AYNI eşleşme yüklemini taşır, mesaj +
// stack'i seçer (üyelik Go'da), en yeni N satır, zaman sınırlı + LIMIT +
// max_execution_time.
func TestExceptionPodsSQLShape(t *testing.T) {
	for _, hasCols := range []bool{true, false} {
		sql := exceptionPodsSQL(exFragments(hasCols))
		for _, want := range []string{"FROM spans", "service_name = ?", "time >= ? AND time <= ?", "k8s_namespace, k8s_pod, k8s_node",
			"AS message", "AS stacktrace", "ORDER BY time DESC", "LIMIT 3001", "max_execution_time"} {
			if !strings.Contains(sql, want) {
				t.Fatalf("hasCols=%v: SQL %q içermeli:\n%s", hasCols, want, sql)
			}
		}
		if hasCols != strings.Contains(sql, "ex_match = 1") {
			t.Fatalf("hasCols=%v: eşleşme yüklemi kolon kipine uymalı:\n%s", hasCols, sql)
		}
		if strings.Contains(sql, "GROUP BY") {
			t.Fatalf("üyelik SQL'de ifade edilemez; kırılım Go'da — GROUP BY olmamalı:\n%s", sql)
		}
		if strings.Count(sql, "?") != 4 {
			t.Fatalf("hasCols=%v: 4 bind arg (service, from, to, type) beklenir, %d var", hasCols, strings.Count(sql, "?"))
		}
	}
}

// Üyelik satır satır; bağlamsız ve host-only ayrı; sıralama oluşum ↓ ad ↑;
// node ilk görülen, son görülme en yeni; pod tavanı → Truncated.
func TestAggregateExceptionPods(t *testing.T) {
	t1 := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	rows := []exceptionPodScanRow{
		{Cluster: "c1", Namespace: "pay", Pod: "api-1", Node: "w1", Time: t1, Message: "A"},
		{Cluster: "c1", Namespace: "pay", Pod: "api-1", Time: t2, Message: "A"},
		{Cluster: "c1", Namespace: "pay", Pod: "api-2", Time: t1, Message: "A"},
		{Cluster: "c1", Namespace: "", Pod: "vm-7", Time: t1, Message: "A"},         // host.name yedeği → HostOnly
		{Pod: "", Time: t1, Message: "A"},                                           // bağlamsız
		{Cluster: "c1", Namespace: "pay", Pod: "api-3", Time: t1, Message: "OTHER"}, // başka grup → dışarıda
	}
	out := aggregateExceptionPods(rows, func(msg, _ string) bool { return msg == "A" })
	if out.Total != 5 || out.NoContext != 1 || out.Truncated {
		t.Fatalf("total=%d noContext=%d truncated=%v", out.Total, out.NoContext, out.Truncated)
	}
	if len(out.Rows) != 3 || out.Rows[0].Pod != "api-1" || out.Rows[0].Occurrences != 2 || out.Rows[0].Node != "w1" || !out.Rows[0].LastSeen.Equal(t2) {
		t.Fatalf("ilk satır api-1 (2, w1, t2) olmalı: %+v", out.Rows)
	}
	if out.Rows[1].Pod != "api-2" || out.Rows[2].Pod != "vm-7" || !out.Rows[2].HostOnly || out.Rows[1].HostOnly {
		t.Fatalf("sıralama/host-only: %+v", out.Rows)
	}
	many := make([]exceptionPodScanRow, 0, 60)
	for i := 0; i < 60; i++ {
		many = append(many, exceptionPodScanRow{Cluster: "c", Namespace: "n", Pod: fmt.Sprintf("p%02d", i), Time: t1, Message: "A"})
	}
	if o := aggregateExceptionPods(many, func(string, string) bool { return true }); !o.Truncated || len(o.Rows) != ExceptionPodsLimit || o.Total != 60 {
		t.Fatalf("tavan: truncated=%v rows=%d total=%d", o.Truncated, len(o.Rows), o.Total)
	}
	if isUnknownIdentifier(fmt.Errorf("code: 47, message: Unknown identifier k8s_pod")) != true || isUnknownIdentifier(fmt.Errorf("code: 159")) {
		t.Fatal("isUnknownIdentifier yalnız 47")
	}
}
