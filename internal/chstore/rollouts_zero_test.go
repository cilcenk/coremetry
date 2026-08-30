package chstore

import (
	"testing"
	"time"
)

// v0.10.199 inceleme 3. tur BLOCKER: CH DateTime64 0 → Go 1970; sıfır kontrolü
// (CompletedAt.IsZero) bozuluyor, her satır sonsuza dek in_progress kalıyordu.
func TestZeroEpochRoundTrip(t *testing.T) {
	if !zeroIfEpoch(time.Unix(0, 0)).IsZero() || !zeroIfEpoch(time.Time{}).IsZero() {
		t.Fatal("1970/sıfır → sıfır")
	}
	real := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	if !zeroIfEpoch(real).Equal(real) {
		t.Fatal("gerçek damga dokunulmaz")
	}
	if e := epochIfZero(time.Time{}); e.Unix() != 0 {
		t.Fatalf("sıfır → 1970: %v", e)
	}
	if !epochIfZero(real).Equal(real) {
		t.Fatal("gerçek damga dokunulmaz (yazım)")
	}
	if !zeroIfEpoch(epochIfZero(time.Time{})).IsZero() {
		t.Fatal("gidiş-dönüş sıfır")
	}
}
