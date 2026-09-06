package config

import (
	"os"
	"testing"
)

// v0.10.511 — COREMETRY_CH_PARALLEL_VIEWS: 0/false/off kapatır, 1/true/on
// ve yokluk açık bırakır, geçersiz değer WARNING + açık (sessiz düşüş yok).
func TestParallelViewsEnv(t *testing.T) {
	cases := []struct {
		val     string
		set     bool
		wantOff bool
	}{
		{"", false, false}, {"0", true, true}, {"false", true, true}, {"OFF", true, true},
		{"1", true, false}, {"true", true, false}, {"maybe", true, false},
	}
	for _, c := range cases {
		os.Unsetenv("COREMETRY_CH_PARALLEL_VIEWS")
		if c.set {
			t.Setenv("COREMETRY_CH_PARALLEL_VIEWS", c.val)
		}
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.ClickHouse.DisableParallelViews != c.wantOff {
			t.Errorf("val=%q set=%v → DisableParallelViews=%v want %v", c.val, c.set, cfg.ClickHouse.DisableParallelViews, c.wantOff)
		}
	}
}
