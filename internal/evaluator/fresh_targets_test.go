package evaluator

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.450 (hacim denetimi L4) — 24 saatlik wildcard listesi 20 saat
// önce susmuş servisi hâlâ değerlendiriyor, absentMeasure'ın 0'ı
// `request_rate <` eşiğini gün boyu ihlal ediyordu. Kapı YALNIZ
// susmayla ihlale dönen şekle uygulanır; diğer her şekil eski yolda.
func TestRuleNeedsFreshTargets(t *testing.T) {
	cases := []struct {
		name string
		r    chstore.AlertRule
		want bool
	}{
		{"request_rate < → kapılı", chstore.AlertRule{Metric: "request_rate", Comparator: "<"}, true},
		{"request_rate <= → kapılı", chstore.AlertRule{Metric: "request_rate", Comparator: "<="}, true},
		{"request_rate > → kapısız (yüksek trafik alarmı susунca kendi resolve olur)", chstore.AlertRule{Metric: "request_rate", Comparator: ">"}, false},
		{"error_rate < → kapısız (absent'te evaluate=false zaten)", chstore.AlertRule{Metric: "error_rate", Comparator: "<"}, false},
		{"p99 < → kapısız (NaN resolve dalı)", chstore.AlertRule{Metric: "p99_ms", Comparator: "<"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleNeedsFreshTargets(tc.r); got != tc.want {
				t.Errorf("ruleNeedsFreshTargets(%s %s) = %v, want %v", tc.r.Metric, tc.r.Comparator, got, tc.want)
			}
		})
	}
}

func TestFilterFreshTargets(t *testing.T) {
	targets := []string{"a", "b", "c"}
	t.Run("nil küme = kapı devre dışı (fail-open)", func(t *testing.T) {
		if got := filterFreshTargets(targets, nil); len(got) != 3 {
			t.Errorf("nil fresh ile hedefler kırpıldı: %v", got)
		}
	})
	t.Run("yalnız taze hedefler kalır", func(t *testing.T) {
		got := filterFreshTargets(targets, map[string]bool{"b": true})
		if len(got) != 1 || got[0] != "b" {
			t.Errorf("got %v, want [b]", got)
		}
	})
	t.Run("boş küme = hiç hedef (tüm filo sustuysa bile satırlar stale sweep'le dürüst kapanır)", func(t *testing.T) {
		if got := filterFreshTargets(targets, map[string]bool{}); len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})
}
