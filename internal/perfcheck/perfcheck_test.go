package perfcheck

import (
	"strings"
	"testing"
)

// perfcheck_test.go — v0.10.116. Karar çekirdeği tablo-testli: yüzdelik,
// eşik, tolerans, veri-seti sapması, geçersiz soğuk koşu. Ölçüm gürültülü
// olabilir; KARAR olamaz.

func TestPercentileNearestRank(t *testing.T) {
	s := []float64{10, 20, 30, 40, 50}
	cases := []struct {
		p    float64
		want float64
	}{{50, 30}, {95, 50}, {100, 50}, {0, 10}, {1, 10}, {60, 30}, {61, 40}}
	for _, c := range cases {
		if got := Percentile(s, c.p); got != c.want {
			t.Errorf("p%v=%v, istenen %v", c.p, got, c.want)
		}
	}
	if Percentile(nil, 50) != 0 {
		t.Error("boş dilim 0 dönmeli")
	}
	st := Summarize([]float64{300, 100, 200})
	if st.N != 3 || st.P50Ms != 200 || st.MinMs != 100 || st.MaxMs != 300 || st.P95Ms != 300 {
		t.Errorf("Summarize: %+v", st)
	}
}

func res(p50 float64, budget float64) Result {
	return Result{Name: "GET /api/traces", Scenario: "mv-24h", Cold: Stats{N: 5, P50Ms: p50, P95Ms: p50 * 1.3}, Budget: Budget{AchievableMs: budget}}
}

func TestEvaluateDecisionTable(t *testing.T) {
	rules := Rules{TolerancePct: 25, DatasetDriftWarnPct: 20}
	prevOK := res(500, 900)
	prevSlow := res(3000, 900)
	cases := []struct {
		name  string
		cur   Result
		prev  *Result
		drift bool
		want  string
	}{
		{"ilk koşu bütçede → pass", res(500, 900), nil, false, "pass"},
		{"ilk koşu bütçe dışı → fail (kıyas yok)", res(1200, 900), nil, false, "fail"},
		{"bütçede, önceki koşudan %60 yavaş → warn", res(800, 900), &prevOK, false, "warn"},
		{"bütçede, önceki koşudan %20 yavaş (tolerans içi) → pass", res(600, 900), &prevOK, false, "pass"},
		{"bütçe dışı ama öncekiyle aynı (kronik) → warn", res(3000, 900), &prevSlow, false, "warn"},
		{"bütçe dışı ve %40 yavaşladı → fail", res(4200, 900), &prevSlow, false, "fail"},
		{"fail ama veri-seti sapmış → warn", res(4200, 900), &prevSlow, true, "warn"},
		{"eşik 0 = eşik yok → pass", res(9999, 0), nil, false, "pass"},
		{"hızlandı → pass (negatif Δ)", res(200, 900), &prevOK, false, "pass"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(c.cur, c.prev, rules, c.drift)
			if got.Status != c.want {
				t.Fatalf("status=%s (%s), istenen %s", got.Status, got.Reason, c.want)
			}
			if c.prev != nil && got.DeltaPct == nil {
				t.Error("önceki koşu varken Δ yazılmadı")
			}
		})
	}
}

func TestEvaluateBytesAndAspirational(t *testing.T) {
	r := res(500, 900)
	r.Budget.MaxBytes = 1000
	r.Bytes = 1_700_000
	got := Evaluate(r, nil, Rules{TolerancePct: 25}, false)
	if got.Status != "warn" || !strings.Contains(got.Reason, "gövde 1700000 B > tavan 1000 B") {
		t.Fatalf("gövde tavanı: %s (%s)", got.Status, got.Reason)
	}
	r.Cold.P50Ms = 2000 // hem yavaş hem şişman → fail
	if got := Evaluate(r, nil, Rules{TolerancePct: 25}, false); got.Status != "fail" {
		t.Fatalf("yavaş+şişman fail olmalı: %s", got.Status)
	}
	a := res(250, 900)
	a.Budget.AspirationalMs = 300
	if got := Evaluate(a, nil, Rules{}, false); got.Status != "pass" || !strings.Contains(got.Reason, "arzu edilen") {
		t.Fatalf("arzu notu: %s (%s)", got.Status, got.Reason)
	}
	inv := Result{Status: "invalid", Reason: "koşu 1 HTTP 401"}
	if got := Evaluate(inv, nil, Rules{}, false); got.Status != "invalid" {
		t.Error("invalid ezildi")
	}
}

func TestValidateColdContract(t *testing.T) {
	ok := []Sample{{Status: 200, XCache: "BYPASS"}, {Status: 200, XCache: "BYPASS"}}
	if st, _ := ValidateCold(ok, true); st != "" {
		t.Errorf("geçerli soğuk koşu reddedildi: %s", st)
	}
	noHeader := []Sample{{Status: 200}} // serveCached'siz uç (dashboards/data)
	if st, _ := ValidateCold(noHeader, true); st != "" {
		t.Errorf("X-Cache'siz uç reddedildi: %s", st)
	}
	hit := []Sample{{Status: 200, XCache: "BYPASS"}, {Status: 200, XCache: "HIT-L1"}}
	if st, why := ValidateCold(hit, true); st != "invalid" || !strings.Contains(why, "koşu 2 X-Cache=HIT-L1") {
		t.Errorf("sıcak isabet soğuk sayıldı: %s %s", st, why)
	}
	if st, why := ValidateCold([]Sample{{Status: 401}}, false); st != "invalid" || !strings.Contains(why, "HTTP 401") {
		t.Errorf("401 geçerli sayıldı: %s %s", st, why)
	}
}

func TestDatasetDriftAndTally(t *testing.T) {
	if d := DatasetDriftPct(Dataset{Spans24h: 1_200_000}, Dataset{Spans24h: 1_000_000}); d < 19.9 || d > 20.1 {
		t.Errorf("drift=%v, istenen 20", d)
	}
	if DatasetDriftPct(Dataset{Spans24h: 5}, Dataset{}) != 0 {
		t.Error("önceki yokken drift 0 olmalı")
	}
	s := Tally([]Result{{Status: "pass"}, {Status: "warn"}, {Status: "pass"}})
	if !s.OK || s.Pass != 2 || s.Warn != 1 {
		t.Errorf("tally: %+v", s)
	}
	if Tally([]Result{{Status: "pass"}, {Status: "fail"}}).OK {
		t.Error("fail varken OK")
	}
	if Tally([]Result{{Status: "invalid"}}).OK {
		t.Error("ölçülemeyen nokta geçti sayıldı")
	}
	prev := &Report{Points: []Result{{Name: "GET /api/traces", Scenario: "mv-24h", Cold: Stats{P50Ms: 500}}}}
	idx := IndexPrev(prev)
	if _, ok := idx["GET /api/traces · mv-24h"]; !ok || len(IndexPrev(nil)) != 0 {
		t.Error("IndexPrev eşlemesi bozuk")
	}
	lines := Lines(Report{Points: []Result{Evaluate(res(1200, 900), nil, Rules{}, false)}})
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "✗ GET /api/traces") || !strings.Contains(lines[0], "1200 ms > ulaşılabilir 900") {
		t.Errorf("satır: %v", lines)
	}
}
