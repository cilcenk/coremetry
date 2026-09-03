package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.325 — ayar doğrulama sınırları.
func TestValidateDBSlowQuery(t *testing.T) {
	ok := chstore.DefaultDBSlowQuery()
	if err := validateDBSlowQuery(ok); err != nil {
		t.Fatalf("varsayılan geçmeli: %v", err)
	}
	for _, tc := range []struct {
		n string
		c chstore.DBSlowQueryConfig
	}{
		{"eşik küçük", chstore.DBSlowQueryConfig{ThresholdMs: 50, CriticalMs: 5000, MinExecutions: 20, ForBuckets: 2}},
		{"critical < eşik", chstore.DBSlowQueryConfig{ThresholdMs: 1000, CriticalMs: 500, MinExecutions: 20, ForBuckets: 2}},
		{"taban 0", chstore.DBSlowQueryConfig{ThresholdMs: 1000, CriticalMs: 5000, MinExecutions: 0, ForBuckets: 2}},
		{"kova 13", chstore.DBSlowQueryConfig{ThresholdMs: 1000, CriticalMs: 5000, MinExecutions: 20, ForBuckets: 13}},
		{"cooldown negatif", chstore.DBSlowQueryConfig{ThresholdMs: 1000, CriticalMs: 5000, MinExecutions: 20, ForBuckets: 2, CooldownSec: -1}},
	} {
		if err := validateDBSlowQuery(tc.c); err == nil {
			t.Errorf("%s: hata bekleniyordu", tc.n)
		}
	}
}
