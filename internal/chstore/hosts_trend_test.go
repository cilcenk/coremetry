package chstore

// v0.8.449 review-fix regression tests — sumHostTrend: gauge series
// sum with per-service forward-fill. The pre-fix SQL sum counted a
// (service, minute) bucket that merely missed a sample as 0, so
// 60s-export jitter drew false sawtooth dips in the host drawer.

import (
	"math"
	"testing"
)

func TestSumHostTrend(t *testing.T) {
	s := func(svc string, min int64, cpu float64, hasCPU bool, mem float64, hasMem bool) hostTrendSample {
		return hostTrendSample{Service: svc, Minute: min, CPU: cpu, HasCPU: hasCPU, Mem: mem, HasMem: hasMem}
	}

	t.Run("empty input → empty slice, not nil", func(t *testing.T) {
		out := sumHostTrend(nil)
		if out == nil || len(out) != 0 {
			t.Fatalf("want empty slice, got %#v", out)
		}
	})

	t.Run("two services sum per minute", func(t *testing.T) {
		out := sumHostTrend([]hostTrendSample{
			s("a", 100, 0.10, true, 1000, true),
			s("b", 100, 0.30, true, 3000, true),
		})
		if len(out) != 1 {
			t.Fatalf("want 1 point, got %d", len(out))
		}
		if math.Abs(out[0].CPUPct-40) > 1e-9 || out[0].MemBytes != 4000 {
			t.Fatalf("want cpu=40 mem=4000, got cpu=%v mem=%v", out[0].CPUPct, out[0].MemBytes)
		}
		if out[0].Bucket != 100*60 {
			t.Fatalf("bucket unix-seconds, got %d", out[0].Bucket)
		}
	})

	t.Run("sample gap ≤3 dk forward-fill edilir — testere dişi yok", func(t *testing.T) {
		// b servisi 101. dakikada örnek KAÇIRIYOR (jitter); pre-fix
		// davranış o dakikayı cpu=10 mem=1000'e düşürürdü.
		out := sumHostTrend([]hostTrendSample{
			s("a", 100, 0.10, true, 1000, true),
			s("b", 100, 0.30, true, 3000, true),
			s("a", 101, 0.10, true, 1000, true),
			s("a", 102, 0.10, true, 1000, true),
			s("b", 102, 0.30, true, 3000, true),
		})
		if len(out) != 3 {
			t.Fatalf("want 3 points, got %d", len(out))
		}
		mid := out[1]
		if math.Abs(mid.CPUPct-40) > 1e-9 || mid.MemBytes != 4000 {
			t.Fatalf("gap minute must carry b forward: cpu=%v mem=%v", mid.CPUPct, mid.MemBytes)
		}
	})

	t.Run("staleness cap: >3 dk sessiz servis düşer", func(t *testing.T) {
		samples := []hostTrendSample{
			s("a", 100, 0.10, true, 1000, true),
			s("b", 100, 0.30, true, 3000, true),
		}
		for m := int64(101); m <= 106; m++ {
			samples = append(samples, s("a", m, 0.10, true, 1000, true))
		}
		out := sumHostTrend(samples)
		// dk 100..103: b taşınır (cap dahil); 104+: yalnız a.
		if len(out) != 7 {
			t.Fatalf("want 7 points, got %d", len(out))
		}
		if math.Abs(out[3].CPUPct-40) > 1e-9 { // dk 103 = son taşıma
			t.Fatalf("minute 103 should still carry b: %v", out[3].CPUPct)
		}
		if math.Abs(out[4].CPUPct-10) > 1e-9 { // dk 104 = b düştü
			t.Fatalf("minute 104 should drop b: %v", out[4].CPUPct)
		}
	})

	t.Run("cpu'suz mem örneği: mem toplanır, cpu 0 sayılmaz", func(t *testing.T) {
		// avgIf no-match nan üretir; HasCPU=false + nan CPU asla toplama girmez.
		out := sumHostTrend([]hostTrendSample{
			s("a", 100, math.NaN(), false, 2000, true),
		})
		if len(out) != 1 || out[0].CPUPct != 0 || out[0].MemBytes != 2000 {
			t.Fatalf("got %#v", out)
		}
	})

	t.Run("tam boş dakika atlanır — 0 çizilmez", func(t *testing.T) {
		out := sumHostTrend([]hostTrendSample{
			s("a", 100, 0.10, true, 1000, true),
			// 101-104 tamamen boş (cap aşımı sonrası nokta yok)
			s("a", 105, 0.10, true, 1000, true),
		})
		// 100..103 taşınır, 104 hiç canlı seri yok → atlanır, 105 gerçek.
		for _, p := range out {
			if p.Bucket == 104*60 {
				t.Fatalf("minute 104 must be skipped, got point %v", p)
			}
		}
	})
}
