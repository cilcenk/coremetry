// rollupselect_test.go — v0.9.385 (rollup Aşama 2). docs/rollup-design.md
// §5'teki karar tablosunun pinleri: aile seçimi, kademe seçimi, retention
// zorlaması, step yuvarlama, hata yolları. Saf — CH gerekmez.
package chstore

import (
	"strings"
	"testing"
	"time"
)

func TestPickRollup(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	win := func(d time.Duration) RollupQuery {
		return RollupQuery{From: now.Add(-d), To: now, MaxDataPoints: 1500}
	}

	cases := []struct {
		name      string
		q         RollupQuery
		wantTable string
		wantStep  int64
		wantMode  string
		wantPart  bool
	}{
		// Karar tablosu (mdp=1500): pencere → kademe.
		{"4h dar → 10s", win(4 * time.Hour), "rollup_spans_narrow_10s", 10, "tdigest", false},
		{"24h dar → 1m", win(24 * time.Hour), "rollup_spans_narrow_1m", 60, "tdigest", false},
		{"5g dar → 5m", win(5 * 24 * time.Hour), "rollup_spans_narrow_5m", 300, "tdigest", false},
		// 60g: idealStep=3456 → 5m'de 3600'e yuvarlanır → yükseltme geçişi
		// 1h tablosuna taşır (3600 tam kat, retention kapsar).
		{"60g dar → 1h", win(60 * 24 * time.Hour), "rollup_spans_narrow_1h", 3600, "tdigest", false},
		{"11ay dar → 1h (19008s → 21600'e yuvarlanır)", win(330 * 24 * time.Hour), "rollup_spans_narrow_1h", 21600, "tdigest", false},

		// Retention zorlaması: 30 günlük pencere idealStep=1728s → 5m tabanı
		// yeterli VE 90g retention kapsar → 5m. Ama pencere başı 100 gün
		// eskiyse 5m kapsamaz → 1h'ye zorlanır.
		{"100g başlangıç dar → 1h (retention zorlaması)",
			RollupQuery{From: now.Add(-100 * 24 * time.Hour), To: now.Add(-70 * 24 * time.Hour), MaxDataPoints: 1500},
			"rollup_spans_narrow_1h", 3600, "tdigest", false},
		{"14ay başlangıç → en kaba + partial (24192s → 25200)",
			RollupQuery{From: now.Add(-14 * 30 * 24 * time.Hour), To: now, MaxDataPoints: 1500},
			"rollup_spans_narrow_1h", 25200, "tdigest", true},

		// Geniş aile: endpoint/channel/function boyutu → bucket modu, taban 1m.
		{"4h endpoint → wide_1m buckets",
			RollupQuery{From: now.Add(-4 * time.Hour), To: now, MaxDataPoints: 1500, Dims: []string{"endpoint"}},
			"rollup_spans_wide_1m", 60, "buckets", false},
		// 5g: idealStep=288 → 1m'de 300'e yuvarlanır → yükseltme 5m'e taşır.
		{"5g channel+status → wide_5m",
			RollupQuery{From: now.Add(-5 * 24 * time.Hour), To: now, MaxDataPoints: 1500,
				Dims: []string{"channel_code", "status_code"}},
			"rollup_spans_wide_5m", 300, "buckets", false},

		// mdp=0 → 1500 varsayılanı; küçük mdp step'i büyütür ve taban katına yuvarlanır.
		{"mdp 0 varsayılanı", RollupQuery{From: now.Add(-4 * time.Hour), To: now}, "rollup_spans_narrow_10s", 10, "tdigest", false},
		{"mdp 100 → step yuvarlama (24h/100=864s → 1m tabanının katı 900s)",
			RollupQuery{From: now.Add(-24 * time.Hour), To: now, MaxDataPoints: 100},
			"rollup_spans_narrow_5m", 900, "tdigest", false},
	}
	for _, c := range cases {
		got, err := PickRollup(c.q, now)
		if err != nil {
			t.Errorf("%s: beklenmeyen hata: %v", c.name, err)
			continue
		}
		if got.Table != c.wantTable || got.StepSeconds != c.wantStep ||
			got.QuantileMode != c.wantMode || got.PartialWindow != c.wantPart {
			t.Errorf("%s:\n got  table=%s step=%d mode=%s partial=%v\n want table=%s step=%d mode=%s partial=%v\n reason=%s",
				c.name, got.Table, got.StepSeconds, got.QuantileMode, got.PartialWindow,
				c.wantTable, c.wantStep, c.wantMode, c.wantPart, got.Reason)
		}
		if got.StepSeconds%tierBaseOf(got.Table) != 0 {
			t.Errorf("%s: step %d taban katı değil", c.name, got.StepSeconds)
		}
	}
}

func tierBaseOf(table string) int64 {
	for _, t := range append(append([]rollupTier{}, narrowTiers...), wideTiers...) {
		if t.table == table {
			return t.baseSec
		}
	}
	return 1
}

func TestPickRollupErrors(t *testing.T) {
	now := time.Now()
	// Bilinmeyen boyut sessizce yutulmaz.
	if _, err := PickRollup(RollupQuery{From: now.Add(-time.Hour), To: now, Dims: []string{"pod"}}, now); err == nil || !strings.Contains(err.Error(), "bilinmeyen boyut") {
		t.Errorf("bilinmeyen boyut hata vermeli, err=%v", err)
	}
	// Geniş boyut + tam-quantile şartı çelişir — bucket'a SESSİZCE düşülmez.
	if _, err := PickRollup(RollupQuery{From: now.Add(-time.Hour), To: now,
		Dims: []string{"endpoint"}, NeedExactQuant: true}, now); err == nil || !strings.Contains(err.Error(), "tam-quantile") {
		t.Errorf("wide+exact çelişkisi hata vermeli, err=%v", err)
	}
	// Ters pencere.
	if _, err := PickRollup(RollupQuery{From: now, To: now.Add(-time.Hour)}, now); err == nil {
		t.Errorf("ters pencere hata vermeli")
	}
}
