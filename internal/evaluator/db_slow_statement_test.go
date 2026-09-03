package evaluator

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.325 — yavaş SQL kararı: ardışık kova sayısı, şiddet, son kova ölçüsü,
// deterministik sıra, taban/eşik süzgeci, gerekçe metni, özne türü.
func TestSlowStmtDecide(t *testing.T) {
	cfg := chstore.DBSlowQueryConfig{Enabled: true, ThresholdMs: 1000, CriticalMs: 5000, MinExecutions: 20, ForBuckets: 2}
	b0 := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	b1 := b0.Add(5 * time.Minute)
	row := func(hash uint64, bucket time.Time, p95 float64, n uint64) chstore.SlowStatementBucket {
		return chstore.SlowStatementBucket{DBSystem: "oracle", DBName: "CRD", Service: "svc", StmtHash: hash, Bucket: bucket, Sample: "SELECT * FROM t WHERE id = ?", Count: n, P95Ms: p95, P99Ms: p95 * 1.5, Exemplar: "tr1"}
	}
	rows := []chstore.SlowStatementBucket{
		row(1, b0, 1200, 30), row(1, b1, 6000, 40), // iki kova, sonu critical
		row(2, b1, 1500, 25),                      // tek kova → henüz değil
		row(3, b0, 1300, 5), row(3, b1, 1300, 50), // ilk kova taban altı → tek kova
		row(4, b0, 900, 100), row(4, b1, 1100, 100), // ilk kova eşik altı → tek kova
	}
	got := slowStmtDecide(rows, cfg)
	if len(got) != 1 {
		t.Fatalf("1 bulgu bekleniyor, %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Key.Hash != 1 || f.Severity != "critical" || f.P95Ms != 6000 || f.Count != 40 || f.Buckets != 2 || !f.Latest.Equal(b1) {
		t.Errorf("bulgu: %+v", f)
	}
	r := slowStmtReason(f, cfg)
	for _, w := range []string{"p95 6.00 s", "p99 9.00 s", "40 executions", "2 consecutive", "≥ 1.00 s", "oracle/CRD via svc", "SELECT * FROM t"} {
		if !strings.Contains(r, w) {
			t.Errorf("gerekçede %q yok: %s", w, r)
		}
	}
	if svc, kind := slowStmtSubject(f.Key); kind != chstore.ProblemKindDB || svc == "" {
		t.Errorf("özne db olmalı: %s %s", svc, kind)
	}
	if svc, kind := slowStmtSubject(slowStmtKey{Service: "svc"}); kind != chstore.ProblemKindService || svc != "svc" {
		t.Errorf("db sistemi yokken servis öznesi: %s %s", svc, kind)
	}
	// ForBuckets=1 → 2, 3 ve 4 de bulgu (tek geçerli kova yeter); sıra
	// deterministik (id'ye göre); 1.1-1.5 s → warning.
	cfg1 := cfg
	cfg1.ForBuckets = 1
	g1 := slowStmtDecide(rows, cfg1)
	if len(g1) != 4 {
		t.Fatalf("ForBuckets=1: 4 bulgu bekleniyor, %d", len(g1))
	}
	for i := 1; i < len(g1); i++ {
		if slowStmtProblemID(g1[i-1].Key) > slowStmtProblemID(g1[i].Key) {
			t.Errorf("sıra deterministik değil")
		}
	}
	for _, g := range g1 {
		if g.Key.Hash != 1 && (g.Severity != "warning" || g.Buckets != 1) {
			t.Errorf("hash %d: %+v", g.Key.Hash, g)
		}
	}
}

func TestSlowStmtProblemIDStable(t *testing.T) {
	k := slowStmtKey{DBSystem: "Oracle", DBName: "CRD", Service: "a", Hash: 42}
	if slowStmtProblemID(k) != "db-slow-stmt:oracle:CRD:42" {
		t.Errorf("id: %s", slowStmtProblemID(k))
	}
}
