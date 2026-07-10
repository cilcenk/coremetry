package chstore

// v0.8.454 regresyon — boundWindow: sıfır bırakılmış from/to güvenli
// varsayılana iner. Sınırsız raw taramalar (GetMetricPoints /
// GetExceptions penceresiz çağrı) hard-constraint ihlaliydi.

import (
	"testing"
	"time"
)

func TestBoundWindow(t *testing.T) {
	ref := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	t.Run("ikisi de dolu → dokunulmaz", func(t *testing.T) {
		f, to := boundWindow(ref.Add(-2*time.Hour), ref, time.Hour)
		if !f.Equal(ref.Add(-2*time.Hour)) || !to.Equal(ref) {
			t.Fatalf("pencere değişti: %v..%v", f, to)
		}
	})

	t.Run("from sıfır → to-def", func(t *testing.T) {
		f, to := boundWindow(time.Time{}, ref, 30*time.Minute)
		if !to.Equal(ref) || !f.Equal(ref.Add(-30*time.Minute)) {
			t.Fatalf("got %v..%v", f, to)
		}
	})

	t.Run("to sıfır → now'a iner, from korunur", func(t *testing.T) {
		start := time.Now()
		f, to := boundWindow(ref, time.Time{}, time.Hour)
		if !f.Equal(ref) {
			t.Fatalf("from değişti: %v", f)
		}
		if to.Before(start) || to.After(time.Now().Add(time.Second)) {
			t.Fatalf("to now değil: %v", to)
		}
	})

	t.Run("ikisi de sıfır → [now-def, now]", func(t *testing.T) {
		f, to := boundWindow(time.Time{}, time.Time{}, time.Hour)
		if got := to.Sub(f); got != time.Hour {
			t.Fatalf("pencere genişliği %v != 1h", got)
		}
		if to.After(time.Now().Add(time.Second)) {
			t.Fatalf("to gelecekte: %v", to)
		}
	})
}
