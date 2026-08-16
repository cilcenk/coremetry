package anomaly

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1069 (F1.6-R2) regresyon pinleri — küme tespiti (spec kararları:
// ≥3 üye + skorlu kaynak; kaynak aday kümesinin İÇİNDEN). Determinizm
// ve "kümelenemeyen bağımsız kalır" sözleşmesi burada mühürlü.
func TestDetectAnomalyClusters(t *testing.T) {
	oc := anomalyOutcome{Action: "open", Severity: "critical", Direction: "spiked"}
	cand := func(svc, metric string) openCandidate {
		return openCandidate{Service: svc, Metric: metric, Outcome: oc}
	}
	// Yıldız topolojisi: a, b, c → db (hata payı taşıyan kenarlar).
	edge := func(caller, callee string, calls, errs uint64) chstore.ServiceEdgePair {
		return chstore.ServiceEdgePair{Caller: caller, Callee: callee, Calls: calls, Errors: errs}
	}
	adj := []chstore.ServiceEdgePair{
		edge("a", "db", 1000, 200),
		edge("b", "db", 800, 150),
		edge("c", "db", 600, 90),
	}

	t.Run("yıldız: 3 suçlayan + kaynak aday → tek küme", func(t *testing.T) {
		cands := []openCandidate{
			cand("a", "error_rate"), cand("b", "error_rate"),
			cand("c", "p99_ms"), cand("db", "error_rate"),
		}
		cl := detectAnomalyClusters(cands, adj, 3)
		if len(cl) != 1 || cl[0].Source != "db" {
			t.Fatalf("cl=%+v", cl)
		}
		if len(cl[0].Members) != 3 {
			t.Fatalf("members=%d, want 3 aday satırı", len(cl[0].Members))
		}
	})

	t.Run("kaynak aday DEĞİLSE küme yok (spec: skorlu kaynak şartı)", func(t *testing.T) {
		cands := []openCandidate{
			cand("a", "error_rate"), cand("b", "error_rate"), cand("c", "p99_ms"),
		}
		if cl := detectAnomalyClusters(cands, adj, 3); len(cl) != 0 {
			t.Fatalf("kaynaksız küme açıldı: %+v", cl)
		}
	})

	t.Run("ikili vaka kümelenmez (minMembers=3)", func(t *testing.T) {
		cands := []openCandidate{cand("a", "error_rate"), cand("db", "error_rate")}
		if cl := detectAnomalyClusters(cands, adj, 3); len(cl) != 0 {
			t.Fatalf("ikili küme açıldı: %+v", cl)
		}
	})

	t.Run("topoloji-bağımsız aday kümeye SIZMAZ", func(t *testing.T) {
		cands := []openCandidate{
			cand("a", "error_rate"), cand("b", "error_rate"),
			cand("db", "error_rate"), cand("lonely-svc", "p99_ms"),
		}
		cl := detectAnomalyClusters(cands, adj, 3)
		if len(cl) != 1 {
			t.Fatalf("cl=%+v", cl)
		}
		for _, m := range cl[0].Members {
			if m.Service == "lonely-svc" {
				t.Fatal("bağlantısız servis kümeye girdi")
			}
		}
	})

	t.Run("determinizm: aynı girdi aynı çıktı (üyeler sıralı)", func(t *testing.T) {
		cands := []openCandidate{
			cand("c", "p99_ms"), cand("db", "error_rate"),
			cand("a", "error_rate"), cand("b", "error_rate"),
		}
		c1 := detectAnomalyClusters(cands, adj, 3)
		c2 := detectAnomalyClusters(cands, adj, 3)
		if len(c1) != 1 || len(c2) != 1 || c1[0].Source != c2[0].Source {
			t.Fatalf("determinizm bozuk: %+v vs %+v", c1, c2)
		}
		for i := range c1[0].Members {
			if c1[0].Members[i] != c2[0].Members[i] {
				t.Fatalf("üye sırası değişken: %+v vs %+v", c1[0].Members, c2[0].Members)
			}
		}
	})
}
