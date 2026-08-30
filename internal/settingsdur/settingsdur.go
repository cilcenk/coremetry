// Package settingsdur — ayar bloblarındaki süre dizelerinin TEK yazımı
// (v0.10.199 inceleme: internal/entity/settings.go ile internal/rollout/
// settings.go bayt-bayt aynı parseDur/clampDur taşıyordu —
// [[feedback-gate-single-spelling]] sınıfı: biri düzelir öteki kalır).
// Bağımlılığı yok; her ayar servisi buradan çağırır.
package settingsdur

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// maxDays — "d" yolunda çarpma taşmasın (taşan float→Duration dönüşümü
// mimariye göre işaret değiştirir; inceleme).
const maxDays = float64(math.MaxInt64) / float64(24*time.Hour)

// Parse — "90s" / "5m" / "6h" (time.ParseDuration) + "2d" (gün) kabul eder;
// boş/bozuk/≤0 → def. Birim testleri EVERY unit kuralı (v0.6.36).
func Parse(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64); err == nil && n > 0 && n <= maxDays {
			return time.Duration(n * float64(24*time.Hour))
		}
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// Clamp — [lo, hi] kelepçesi.
func Clamp(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}
