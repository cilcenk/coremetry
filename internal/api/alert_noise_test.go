// v0.9.1080 (F3.3) — alert gürültüsü kanıt paketinin testleri.
// Model yalnız bu metindeki rakamları anlatır; paketin dürüstlüğü
// (tavan ifşası, "okunamadı ≠ sıfır") burada çivili.
package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestAlertNoiseWindow(t *testing.T) {
	// Rung disiplini: her izinli birim + bilinmeyenin 24h'a düşmesi.
	cases := []struct {
		raw       string
		wantDur   time.Duration
		wantLabel string
	}{
		{"6h", 6 * time.Hour, "6 saat"},
		{"24h", 24 * time.Hour, "24 saat"},
		{"168h", 7 * 24 * time.Hour, "7 gün"},
		{"", 24 * time.Hour, "24 saat"},
		{"37m", 24 * time.Hour, "24 saat"}, // serbest pencere YOK
	}
	for _, c := range cases {
		d, label := alertNoiseWindow(c.raw)
		if d != c.wantDur || label != c.wantLabel {
			t.Errorf("alertNoiseWindow(%q) = (%v, %q); beklenen (%v, %q)",
				c.raw, d, label, c.wantDur, c.wantLabel)
		}
	}
}

func TestRenderAlertNoiseEvidence(t *testing.T) {
	rules := []NoisyRuleWithSuggestion{{
		NoisyRule: chstore.NoisyRule{RuleName: "CPU high", Severity: "critical",
			OpenCount: 47, MedianDurSec: 38},
		Suggestion: "Median open 38s — 47 times. Add for=120s.",
		CurrentFor: 0, CurrentMin: 3, CurrentCD: 0,
	}}

	t.Run("kural + öneri + mevcut ayarlar pakette", func(t *testing.T) {
		got := renderAlertNoiseEvidence("24 saat", rules, []chstore.NotificationLog{
			{ChannelKind: "email", OK: true},
			{ChannelKind: "email", OK: false},
			{ChannelKind: "slack", OK: true},
		}, true)
		for _, want := range []string{
			"Pencere: son 24 saat",
			`"CPU high" (critical) — 47 açılış, medyan süre 38s`,
			"for=0s min_samples=3 cooldown=0s",
			"Add for=120s",
			"email 2, slack 1",
			"1 gönderim BAŞARISIZ",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("pakette %q yok:\n%s", want, got)
			}
		}
	})

	t.Run("okunamadı ≠ sıfır — dürüst itiraf", func(t *testing.T) {
		got := renderAlertNoiseEvidence("24 saat", rules, nil, false)
		if !strings.Contains(got, "okunamadı") || strings.Contains(got, "hiç bildirim") {
			t.Errorf("okuma hatası sıfırla karışmamalı:\n%s", got)
		}
	})

	t.Run("tavan ifşası — no-silent-caps", func(t *testing.T) {
		logs := make([]chstore.NotificationLog, alertNoiseLogCap)
		for i := range logs {
			logs[i] = chstore.NotificationLog{ChannelKind: "email", OK: true}
		}
		got := renderAlertNoiseEvidence("7 gün", rules, logs, true)
		if !strings.Contains(got, "en az 1000 (okuma tavanı)") {
			t.Errorf("tavana dayanınca 'en az N' ifşası şart:\n%s", got)
		}
	})

	t.Run("boş pencere de geçerli cevap", func(t *testing.T) {
		got := renderAlertNoiseEvidence("6 saat", nil, nil, true)
		if !strings.Contains(got, "problem açan kural yok") ||
			!strings.Contains(got, "hiç bildirim gönderilmemiş") {
			t.Errorf("boş pencere dürüstçe anlatılmalı:\n%s", got)
		}
	})
}
