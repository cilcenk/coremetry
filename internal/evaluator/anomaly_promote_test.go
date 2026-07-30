package evaluator

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.444 (prod hacim denetimi #3) — anomali terfisinin refresh dalı her
// tick Status:"open" + boş Assignee/Pod yazıyordu: ack'lenmiş problem bir
// tick sonra geri açılıyor, atanan kişi siliniyordu (ReplacingMergeTree
// bütün-satır replace sözleşmesi, CLAUDE.md invariant #4).
func TestCarryProblemOperatorState(t *testing.T) {
	base := func() chstore.Problem {
		return chstore.Problem{
			ID: "anomaly-auto:fp1:svc", Status: "open",
			Severity: "critical", Value: 7.5,
		}
	}

	t.Run("ack hayatta kalır", func(t *testing.T) {
		p := base()
		open := &chstore.Problem{Status: "acknowledged", Assignee: "cenk", Pod: "pod-a"}
		carryProblemOperatorState(&p, open)
		if p.Status != "acknowledged" || p.Assignee != "cenk" || p.Pod != "pod-a" {
			t.Errorf("operator alanları taşınmadı: %+v", p)
		}
		// Taze ölçüm alanları open'dan GELMEZ.
		if p.Severity != "critical" || p.Value != 7.5 {
			t.Errorf("ölçüm alanları ezildi: %+v", p)
		}
	})

	t.Run("yeni satırda no-op", func(t *testing.T) {
		p := base()
		carryProblemOperatorState(&p, nil)
		if p.Status != "open" || p.Assignee != "" {
			t.Errorf("nil open değişiklik yaptı: %+v", p)
		}
	})
}

// v0.9.444 — kapanış gerekçesi damgası: idempotent, gerekçeli.
func TestAppendResolveSuffix(t *testing.T) {
	cases := []struct {
		name, desc, reason, want string
	}{
		{"temiz ekleme", "Auto-promoted from anomaly: logs / err", "anomaly cleared",
			"Auto-promoted from anomaly: logs / err · auto-resolved: anomaly cleared"},
		{"idempotent", "x · auto-resolved: anomaly cleared", "anomaly cleared",
			"x · auto-resolved: anomaly cleared"},
		{"farklı gerekçe eklenir", "x · auto-resolved: anomaly cleared", "anomaly muted",
			"x · auto-resolved: anomaly cleared · auto-resolved: anomaly muted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendResolveSuffix(tc.desc, tc.reason); got != tc.want {
				t.Errorf("appendResolveSuffix(%q, %q) = %q; want %q", tc.desc, tc.reason, got, tc.want)
			}
		})
	}
}
