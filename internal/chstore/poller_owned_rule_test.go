package chstore

import "testing"

// v0.10.592 — bayat süpürme dışı kurallar. Seri kuralı (anomaly:ext:…)
// BİLEREK dışarıda: kaynak susunca onun "source silent" kapanışı dürüst
// sinyaldir; poller'ın kendi Problem'leri ise poll'da touch edilir ve
// süpürme onları yalnız yanlış kapatabilir.
func TestPollerOwnedRule(t *testing.T) {
	cases := map[string]bool{
		RuleExtDownPrefix + "ext:oracle-errlog":        true,
		RuleExtCapPrefix + "ext:ggfail:ext:tfail_adet": true,
		"anomaly:ext:ggfail/OP1/E1:ext:tfail_adet":     false, // seri Problem'i — süpürülür
		"anomaly:shop-payment:p99_ms":                  false,
		"rule-42":                                      false,
		"":                                             false,
	}
	for id, want := range cases {
		if got := PollerOwnedRule(id); got != want {
			t.Errorf("%q → %v, beklenen %v", id, got, want)
		}
	}
}
