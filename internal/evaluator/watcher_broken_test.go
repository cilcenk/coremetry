package evaluator

import (
	"os"
	"strings"
	"testing"
)

// v0.9.447 (hacim denetimi #4) — bozuk kaynaklı watch (silinmiş index /
// ES yetkisi / yanlış agg yolu) her cadence'ta error-return üretir;
// resolve dalı hiç koşmadığı ve not-due keep-alive satırı stale
// sweep'ten koruduğu için problemi ölümsüzdü.
func TestWatcherSourceBroken(t *testing.T) {
	cases := []struct {
		fails int
		want  bool
	}{
		{0, false},
		{1, false}, // tek ES hıçkırığı ASLA kapatmaz
		{2, false},
		{3, true}, // eşik: 3 ardışık cadence (~15dk tipik watch'ta)
		{7, true},
	}
	for _, tc := range cases {
		if got := watcherSourceBroken(tc.fails); got != tc.want {
			t.Errorf("watcherSourceBroken(%d) = %v, want %v", tc.fails, got, tc.want)
		}
	}
}

// Kaynak-şekli pinleri: her ölçüm/çıkarım hata sitesi sayacı artırır,
// her başarılı ölçüm sıfırlar. Yeni bir condition dalı eklenirse ve bu
// çağrılar unutulursa pin kırılır.
func TestWatcherFailurePathsPinned(t *testing.T) {
	b, err := os.ReadFile("watcher_eval.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "e.watcherMeasureFailed(ctx, r, err)"); n < 5 {
		t.Errorf("watcherMeasureFailed call sites = %d, want ≥5 (measure+extract errors across all condition branches)", n)
	}
	if n := strings.Count(src, "e.watcherMeasureOK(r.ID)"); n < 3 {
		t.Errorf("watcherMeasureOK call sites = %d, want ≥3 (every successful measure resets the counter)", n)
	}
	if !strings.Contains(src, `appendResolveSuffix(open.Description, "watch source broken")`) {
		t.Error("broken-source close lost its honest reason stamp")
	}
}
