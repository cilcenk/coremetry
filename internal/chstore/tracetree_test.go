package chstore

// tracetree_test.go — v0.10.275 sözleşmesi (docs/audit/trace-view.md §7.1):
// lineer / paralel / async (ikinci kök) / hatalı / yetim / sıfır-negatif süre /
// derin 10k (özyineleme yok) / kesik. Kritik yol lib/criticalPath.ts, öz süre
// lib/selfTime.ts ile AYNI sayıları vermeli.

import (
	"fmt"
	"testing"
)

func sp(id, parent, svc string, start, end int64, status string) SpanRow {
	return SpanRow{TraceID: "t", SpanID: id, ParentSpanID: parent, ServiceName: svc, StartTime: start, EndTime: end, StatusCode: status}
}

func nodeByID(a TraceAnalysis, id string) TraceNode {
	for _, n := range a.Nodes {
		if n.SpanID == id {
			return n
		}
	}
	return TraceNode{}
}

func TestTraceAnalysisLinear(t *testing.T) {
	a := BuildTraceAnalysis([]SpanRow{
		sp("a", "", "api", 0, 100, "ok"),
		sp("b", "a", "orders", 10, 90, "ok"),
		sp("c", "b", "db", 20, 50, "ok"),
	}, false)
	if len(a.Nodes) != 3 || a.RootSpanID != "a" || a.OrphanCount != 0 {
		t.Fatalf("ağaç: %+v", a)
	}
	if fmt.Sprint(a.CriticalIDs) != "[a b c]" || a.CriticalNs != 100+80+30 {
		t.Errorf("kritik yol: %v %d", a.CriticalIDs, a.CriticalNs)
	}
	if nodeByID(a, "a").SelfNs != 20 || nodeByID(a, "b").SelfNs != 50 || nodeByID(a, "c").SelfNs != 30 {
		t.Errorf("öz süre: a=%d b=%d c=%d", nodeByID(a, "a").SelfNs, nodeByID(a, "b").SelfNs, nodeByID(a, "c").SelfNs)
	}
	if nodeByID(a, "a").SubtreeCount != 3 || nodeByID(a, "b").Depth != 1 || nodeByID(a, "c").Order != 2 {
		t.Errorf("sıra/derinlik/alt ağaç: %+v", a.Nodes)
	}
	if a.Services[0].Service != "orders" || a.Services[0].SelfPct < 49 || a.Services[0].SelfPct > 51 {
		t.Errorf("servis özeti öz süreye göre sıralı: %+v", a.Services)
	}
	if nodeByID(a, "a").SubtreeNs != 100 {
		t.Errorf("subtreeNs duvar saati: %d", nodeByID(a, "a").SubtreeNs)
	}
}

func TestTraceAnalysisParallelChildrenUseIntervalUnion(t *testing.T) {
	// a[0,100] → b[10,50], c[20,60] (çakışan): birleşim 10..60 = 50 → self 50.
	// Naif toplam 40+40 = 80 → self 20 olurdu (yanlış tanım).
	a := BuildTraceAnalysis([]SpanRow{
		sp("a", "", "api", 0, 100, "ok"),
		sp("b", "a", "x", 10, 50, "ok"),
		sp("c", "a", "y", 20, 60, "ok"),
	}, false)
	if got := nodeByID(a, "a").SelfNs; got != 50 {
		t.Fatalf("aralık birleşimi: self=%d, istenen 50", got)
	}
	// çocuklar ebeveynin dışına taşarsa kırpılır ve self negatif OLMAZ
	b := BuildTraceAnalysis([]SpanRow{
		sp("a", "", "api", 0, 100, "ok"),
		sp("b", "a", "x", -50, 200, "ok"),
	}, false)
	if got := nodeByID(b, "a").SelfNs; got != 0 {
		t.Fatalf("taşan çocuk: self=%d, istenen 0", got)
	}
	// taşan çocuk kritik zincire GİREMEZ (ebeveyn bitişini aşıyor)
	if fmt.Sprint(b.CriticalIDs) != "[a]" {
		t.Errorf("taşan çocuk zincirde: %v", b.CriticalIDs)
	}
}

func TestTraceAnalysisAsyncSecondRootAndOrphan(t *testing.T) {
	// İki kök (producer + consumer link'li ayrı alt ağaç) + bir yetim.
	a := BuildTraceAnalysis([]SpanRow{
		sp("p", "", "producer", 0, 100, "ok"),
		sp("c", "", "consumer", 500, 900, "ok"),
		sp("cc", "c", "consumer", 520, 880, "error"),
		sp("o", "missing", "svc", 50, 60, "ok"),
	}, false)
	if a.OrphanCount != 1 {
		t.Errorf("yetim sayısı %d", a.OrphanCount)
	}
	if a.RootSpanID != "c" || fmt.Sprint(a.CriticalIDs) != "[c cc]" {
		t.Errorf("kritik yol en uzun kökten: root=%s ids=%v", a.RootSpanID, a.CriticalIDs)
	}
	if nodeByID(a, "c").SubtreeErrors != 1 || nodeByID(a, "p").SubtreeErrors != 0 {
		t.Errorf("hata yukarı toplanır: %+v", a.Nodes)
	}
	// kök sırası başlangıç zamanına göre: p, o, c
	if a.Nodes[0].SpanID != "p" || a.Nodes[1].SpanID != "o" || a.Nodes[2].SpanID != "c" {
		t.Errorf("kök sırası: %v", []string{a.Nodes[0].SpanID, a.Nodes[1].SpanID, a.Nodes[2].SpanID})
	}
	// servis giriş sayısı: consumer'a giriş 1 (c kök), cc aynı servis → giriş değil
	for _, s := range a.Services {
		if s.Service == "consumer" && (s.EntryCount != 1 || s.SpanCount != 2 || s.ErrorCount != 1) {
			t.Errorf("consumer özeti: %+v", s)
		}
	}
}

func TestTraceAnalysisZeroAndNegativeDurations(t *testing.T) {
	a := BuildTraceAnalysis([]SpanRow{
		sp("a", "", "api", 100, 100, "ok"),
		sp("b", "a", "x", 120, 110, "ok"), // end < start
	}, false)
	for _, n := range a.Nodes {
		if n.SelfNs < 0 || n.SubtreeNs < 0 {
			t.Fatalf("negatif süre: %+v", n)
		}
	}
	if a.CriticalNs != 0 {
		t.Errorf("sıfır sürelerde kritik toplam 0, %d", a.CriticalNs)
	}
}

func TestTraceAnalysisDeepChainNoRecursion(t *testing.T) {
	n := 10_000
	spans := make([]SpanRow, 0, n)
	spans = append(spans, sp("s0", "", "svc", 0, int64(n)*10, "ok"))
	for i := 1; i < n; i++ {
		spans = append(spans, sp(fmt.Sprintf("s%d", i), fmt.Sprintf("s%d", i-1), "svc", int64(i), int64(n)*10-int64(i), "ok"))
	}
	a := BuildTraceAnalysis(spans, true)
	if len(a.Nodes) != n || len(a.CriticalIDs) != n || !a.Truncated {
		t.Fatalf("derin zincir: nodes=%d critical=%d truncated=%v", len(a.Nodes), len(a.CriticalIDs), a.Truncated)
	}
	if a.Nodes[n-1].Depth != uint16(n-1) {
		t.Errorf("derinlik %d", a.Nodes[n-1].Depth)
	}
}

func TestTraceAnalysisEmptyAndDuplicateIDs(t *testing.T) {
	if a := BuildTraceAnalysis(nil, false); len(a.Nodes) != 0 || a.Nodes == nil || a.CriticalIDs == nil || a.Services == nil {
		t.Fatalf("boş: JSON'da null değil boş dizi: %+v", a)
	}
	a := BuildTraceAnalysis([]SpanRow{sp("a", "", "x", 0, 10, "ok"), sp("a", "", "x", 0, 10, "ok"), sp("", "", "x", 0, 1, "ok")}, false)
	if len(a.Nodes) != 1 {
		t.Errorf("kopya/boş id ağaca girmez: %d düğüm", len(a.Nodes))
	}
}

func BenchmarkBuildTraceAnalysis5000(b *testing.B) {
	spans := make([]SpanRow, 0, 5000)
	spans = append(spans, sp("root", "", "api", 0, 5_000_000, "ok"))
	for i := 1; i < 5000; i++ {
		parent := fmt.Sprintf("s%d", (i-1)/8)
		if i <= 8 {
			parent = "root"
		}
		spans = append(spans, sp(fmt.Sprintf("s%d", i), parent, fmt.Sprintf("svc%d", i%20), int64(i)*100, int64(i)*100+50_000, map[bool]string{true: "error", false: "ok"}[i%97 == 0]))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildTraceAnalysis(spans, false)
	}
}
