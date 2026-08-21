package evaluator

// v0.9.1213 — OPT-IN canlı VM testi (VM_LIVE_URL ile koşar; CI'da skip).
// Neyi kanıtlar: vmGCSamples'ın kullandığı çeviri zinciri — verbatim
// `_sum`/`_count` adları + 4-anahtarlı GroupBy + `increase` — GERÇEK bir
// VictoriaMetrics'e karşı beklenen pencere toplamlarını üretir. Docker
// örneği: docker run --rm -p 18430:8428 victoriametrics/victoria-metrics
//   -search.latencyOffset=0s   (offset'siz taze yazım 30 sn görünmez)
// sonra: VM_LIVE_URL=http://127.0.0.1:18430 go test -run TestVMGCLive ./internal/evaluator/

import (
	"context"
	"os"
	"testing"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

func TestVMGCLiveTranslation(t *testing.T) {
	url := os.Getenv("VM_LIVE_URL")
	if url == "" {
		t.Skip("VM_LIVE_URL yok — canlı VM testi opt-in")
	}
	svc := vmetrics.New()
	svc.Configure(vmetrics.Settings{Enabled: true, BaseURL: url})
	e := &Evaluator{vmetrics: svc}
	pauses, acts, err := e.vmGCSamples(context.Background())
	if err != nil {
		t.Fatalf("vmGCSamples: %v", err)
	}
	t.Logf("pauses=%d acts=%d", len(pauses), len(acts))
	for _, p := range pauses {
		t.Logf("pause: %s/%s = %.1fms", p.Instance, p.Subkey, p.Usage)
	}
	for _, a := range acts {
		t.Logf("act: %s/%s share=%.2f%% rate=%.1f/dk", a.Service, a.Pod, a.SharePct, a.RatePerMin)
	}
	if len(pauses) == 0 {
		t.Fatal("canlı VM'de jvm_gc serisi bulunamadı — test verisini import ettin mi?")
	}
}
