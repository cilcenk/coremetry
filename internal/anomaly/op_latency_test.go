package anomaly

import "testing"

// v0.9.1064 (Faz 2.3 / G5) regresyon pini — operasyon-gecikme
// sınıflayıcısı. Eşik sözleşmesi: iki pencerede de hacim tabanı (30),
// oran ≥3×, mutlak taban 200ms; sıralama oran desc; tavan 50.
func TestClassifyOpLatency(t *testing.T) {
	mk := func(svc, op string, cur, base float64, curCalls, baseCalls uint64) opLatencyBucket {
		return opLatencyBucket{Service: svc, Operation: op, CurP99Ms: cur, BaseP99Ms: base, CurCalls: curCalls, BaseCalls: baseCalls}
	}

	t.Run("gerçek sıçrama kalifiye + oran doğru", func(t *testing.T) {
		out := classifyOpLatency([]opLatencyBucket{mk("s", "GET /x", 900, 100, 500, 5000)})
		if len(out) != 1 || out[0].Ratio != 9 || out[0].Operation != "GET /x" {
			t.Fatalf("out=%+v", out)
		}
	})

	t.Run("hacim tabanı: cari VEYA baseline < 30 çağrı → elenir", func(t *testing.T) {
		if out := classifyOpLatency([]opLatencyBucket{
			mk("s", "az-cari", 900, 100, 20, 5000),
			mk("s", "az-base", 900, 100, 500, 20),
		}); len(out) != 0 {
			t.Fatalf("düşük hacim geçti: %+v", out)
		}
	})

	t.Run("mutlak taban: 60ms→180ms (3×) ama <200ms → olay değil", func(t *testing.T) {
		if out := classifyOpLatency([]opLatencyBucket{mk("s", "hızlı-op", 180, 60, 500, 5000)}); len(out) != 0 {
			t.Fatalf("200ms tabanı delindi: %+v", out)
		}
	})

	t.Run("oran tabanı: 2.5× → elenir", func(t *testing.T) {
		if out := classifyOpLatency([]opLatencyBucket{mk("s", "op", 500, 200, 500, 5000)}); len(out) != 0 {
			t.Fatalf("3× altı geçti: %+v", out)
		}
	})

	t.Run("sıfır baseline p99 → oran kurulamaz, elenir", func(t *testing.T) {
		if out := classifyOpLatency([]opLatencyBucket{mk("s", "op", 500, 0, 500, 5000)}); len(out) != 0 {
			t.Fatalf("sıfır baseline geçti: %+v", out)
		}
	})

	t.Run("sıralama oran desc + tavan 50", func(t *testing.T) {
		var in []opLatencyBucket
		for i := 0; i < 60; i++ {
			in = append(in, mk("s", "op", 200+float64(i)*100, 50, 500, 5000))
		}
		out := classifyOpLatency(in)
		if len(out) != 50 || out[0].Ratio < out[1].Ratio {
			t.Fatalf("len=%d first=%.1f second=%.1f", len(out), out[0].Ratio, out[1].Ratio)
		}
	})
}
