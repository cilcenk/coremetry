package correlator

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1059 (Faz 1.4 / K8) regresyon pini — deploy adayı ölçülmüş etki
// taşır. ComputeDeployImpact yazılalı beri hiçbir kök-neden yolu onu
// çağırmıyordu; "deploy şüpheli" iddiası rakamsız gidiyordu. Sözleşme:
// impact varsa gerekçe önce/sonra p99 (+%) taşır ve RecentDeploy.Impact
// hipoteze iliştirilir; impact nil'ken çıktı BAYT-BAYT eski (girdiye
// dokunulmaz — in.Deploy kopyalanmadan paylaşılır).
func TestSynthesizeDeployImpact(t *testing.T) {
	dep := &chstore.RecentDeploy{Version: "v2.3.1", TimeUnixNs: 1000, AgeSeconds: 240}
	imp := &chstore.DeployImpact{
		Before:            chstore.DeployImpactStats{Count: 100, P99Ms: 42, ErrorRate: 0.002},
		After:             chstore.DeployImpactStats{Count: 100, P99Ms: 380, ErrorRate: 0.041},
		P99DeltaPct:       805,
		ErrorRateDeltaPct: 3.9,
	}

	h := Synthesize("problem", "p1", "checkout", 42, SynthesisInput{
		Deploy: dep, FreshnessFrac: 0.9, DeployImpact: imp,
	})
	if len(h.Candidates) != 1 {
		t.Fatalf("candidates=%d", len(h.Candidates))
	}
	reason := h.Candidates[0].Reason
	if !strings.Contains(reason, "p99 42→380ms (+805%)") {
		t.Fatalf("gerekçede ölçülmüş etki yok: %q", reason)
	}
	if !strings.Contains(reason, "err 0.2%→4.1%") {
		t.Fatalf("hata oranı geçişi yok: %q", reason)
	}
	if h.RecentDeploy == nil || h.RecentDeploy.Impact != imp {
		t.Fatal("RecentDeploy.Impact hipoteze iliştirilmedi")
	}
	// Girdi deploy'u MUTATE edilmez (paylaşılan pointer güvenliği).
	if dep.Impact != nil {
		t.Fatal("in.Deploy mutate edildi — kopya sözleşmesi bozuldu")
	}

	// impact nil → eski davranış bayt-bayt.
	h2 := Synthesize("problem", "p1", "checkout", 42, SynthesisInput{
		Deploy: dep, FreshnessFrac: 0.9,
	})
	if strings.Contains(h2.Candidates[0].Reason, "after it") || h2.RecentDeploy.Impact != nil {
		t.Fatalf("impact yokken etki sızdı: %q", h2.Candidates[0].Reason)
	}
}

// Hata oranı kıpırdamadıysa (|Δ| < 0.1 puan) err bölümü basılmaz.
func TestDeployImpactSummaryQuietError(t *testing.T) {
	s := deployImpactSummary(&chstore.DeployImpact{
		Before: chstore.DeployImpactStats{P99Ms: 40}, After: chstore.DeployImpactStats{P99Ms: 60},
		P99DeltaPct: 50, ErrorRateDeltaPct: 0.05,
	})
	if strings.Contains(s, "err") {
		t.Fatalf("sessiz hata oranı yazıldı: %q", s)
	}
}
