package anomaly

// v0.9.1194 — fırtına dedektörünün saf parçaları. Servis adları SENTETİK.

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func stormCands(n int) []chstore.ExceptionStormCandidate {
	out := make([]chstore.ExceptionStormCandidate, 0, n)
	names := []string{"checkout-bff", "payments-core", "login-gw", "cards-api",
		"notify-svc", "ledger", "kyc-api", "fx-rates", "search", "docs-api"}
	for i := 0; i < n; i++ {
		out = append(out, chstore.ExceptionStormCandidate{
			Service: names[i%len(names)], Groups: uint64(3 - i%3), Occurrences: uint64(10 * (i + 1)),
		})
	}
	return out
}

func TestStormDescription(t *testing.T) {
	t.Run("bildirilen olayın şekli — 9 servis", func(t *testing.T) {
		d := stormDescription(stormCands(9), 10*time.Minute, 5)
		for _, want := range []string{"9 farklı servis", "eşik 5", "10dk", "checkout-bff (3 grup, 10 olay)", "+1 servis daha"} {
			if !strings.Contains(d, want) {
				t.Errorf("açıklama %q içermeli:\n%s", want, d)
			}
		}
	})
	t.Run("8 ve altı tam listelenir, kırpma notu YOK", func(t *testing.T) {
		d := stormDescription(stormCands(5), 10*time.Minute, 5)
		if strings.Contains(d, "daha") {
			t.Errorf("5 serviste kırpma notu olmamalı: %s", d)
		}
	})
	t.Run("pencere metni vidayı SÖYLER, sabitlemez", func(t *testing.T) {
		// Vida 90 dakikaya çekilirse cümle onu söylemeli (v0.9.775 kuralı:
		// öncelik düşebilir, cümle yalan olamaz).
		d := stormDescription(stormCands(6), 90*time.Minute, 5)
		if !strings.Contains(d, "1sa 30dk") {
			t.Errorf("pencere metni 1sa 30dk olmalı: %s", d)
		}
	})
}

func TestShortWindow(t *testing.T) {
	// Birim-karışması sınıfı: her dal ayrı denenir.
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Minute, "10dk"}, {60 * time.Minute, "1sa"},
		{90 * time.Minute, "1sa 30dk"}, {2 * time.Hour, "2sa"},
	} {
		if got := shortWindow(c.in); got != c.want {
			t.Errorf("shortWindow(%v) = %q, beklenen %q", c.in, got, c.want)
		}
	}
}
