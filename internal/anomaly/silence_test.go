package anomaly

import "testing"

// v0.9.1051 (Faz 0.3) regresyon pini — service_silent dedektörü.
// "Servis tamamen sustu" sınıfı varsayılan kurulumda tamamen kördü
// (request_rate izleme kapalı, gömülü kuralların hiçbiri rate değil) ve
// susan servisin açık anomalisi "recovered" gerekçesiyle kapanıyordu.
// Bu tablo iki sözleşmeyi mühürler: (1) istikrarlı-akışlı servisin 3
// sıfır kovası silent=true + baseline medyanı döner, (2) batch/cron
// (baseline'ı delikli) ve trafiği süren servisler ASLA silent olmaz.
func TestSilenceVerdict(t *testing.T) {
	steady := func(n int, v float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}

	t.Run("istikrarlı servis + 3 sıfır kuyruk → silent, baseline medyanı", func(t *testing.T) {
		rates := append(steady(20, 4.0), 0, 0, 0)
		silent, base := silenceVerdict(rates, 3, 0.9)
		if !silent || base != 4.0 {
			t.Fatalf("got silent=%v base=%.2f, want true/4.0", silent, base)
		}
	})

	t.Run("kuyrukta tek kova bile trafik varsa silent değil", func(t *testing.T) {
		rates := append(steady(20, 4.0), 0, 0.2, 0)
		if silent, _ := silenceVerdict(rates, 3, 0.9); silent {
			t.Fatal("trafik taşıyan kuyruk silent sayıldı")
		}
	})

	t.Run("batch/cron: delikli baseline (aktif pay < %90) → asla silent", func(t *testing.T) {
		base := steady(20, 3.0)
		for i := 0; i < 6; i++ { // 20 kovanın 6'sı sıfır → pay 0.7
			base[i*3] = 0
		}
		rates := append(base, 0, 0, 0)
		if silent, _ := silenceVerdict(rates, 3, 0.9); silent {
			t.Fatal("delikli baseline'lı servis silent sayıldı — batch/cron FP sınıfı")
		}
	})

	t.Run("kısa geçmiş (< trailing+minSamples) → silent değil", func(t *testing.T) {
		rates := append(steady(5, 4.0), 0, 0, 0)
		if silent, _ := silenceVerdict(rates, 3, 0.9); silent {
			t.Fatal("yetersiz geçmişle silent kararı verildi")
		}
	})

	t.Run("hiç trafik görmemiş seri (hep sıfır) → silent değil", func(t *testing.T) {
		if silent, _ := silenceVerdict(steady(30, 0), 3, 0.9); silent {
			t.Fatal("hiç trafiksiz servis silent sayıldı")
		}
	})
}
