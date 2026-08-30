package chstore

// anomaly_verdict_test.go — v0.10.181 saf sözleşme: iki karar değeri; IN
// listesi yer tutucuyla (kullanıcı kimliği SQL'e dizgiyle girmez).

import "testing"

func TestValidVerdict(t *testing.T) {
	for v, want := range map[string]bool{"anomaly": true, "not_anomaly": true, "": false, "yes": false, "Anomaly": false} {
		if got := ValidVerdict(v); got != want {
			t.Errorf("ValidVerdict(%q)=%v want %v", v, got, want)
		}
	}
}

func TestVerdictInPlaceholders(t *testing.T) {
	for n, want := range map[int]string{0: "", 1: "?", 3: "?,?,?"} {
		if got := verdictInPlaceholders(n); got != want {
			t.Errorf("verdictInPlaceholders(%d)=%q want %q", n, got, want)
		}
	}
}
