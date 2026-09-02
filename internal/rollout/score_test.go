package rollout

// score_test.go — v0.10.241 (Problem↔Rollout korelasyonu D1).
// Sözleşme: audit §7 bantları (0–30 → 0.90, 30–120 → 0.50, >120 aday
// değil, problemden sonra aday değil), durum çarpanları, bonuslar, tavan,
// tekilleştirme + sıralama + kesme. Saf çekirdek; CH yok.

import (
	"strings"
	"testing"
	"time"
)

var onset = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func cand(minBefore float64, status, detectedBy, matched string) Candidate {
	return Candidate{
		Rollout: Rollout{
			ClusterID: "c1", Namespace: "ns", Workload: "api", Revision: "api-" + status,
			StartedAt:  onset.Add(-time.Duration(minBefore * float64(time.Minute))),
			Status:     status,
			DetectedBy: detectedBy,
		},
		MatchedBy: matched,
	}
}

func TestScoreBands(t *testing.T) {
	cases := []struct {
		name   string
		minAgo float64
		ok     bool
		band   string
		score  float64
	}{
		{"aynı dakika", 0, true, BandHigh, 0.90},
		{"29 dk", 29, true, BandHigh, 0.90},
		{"30 dk sınır dahil", 30, true, BandHigh, 0.90},
		{"31 dk düşük bant", 31, true, BandLow, 0.50},
		{"120 dk sınır dahil", 120, true, BandLow, 0.50},
		{"121 dk aday değil", 121, false, "", 0},
		{"problemden 4 dk SONRA tolerans", -4, true, BandHigh, 0.90},
		{"problemden 6 dk SONRA aday değil", -6, false, "", 0},
	}
	for _, c := range cases {
		s, ok := Score(onset, cand(c.minAgo, StatusCompleted, "spans", MatchService))
		if ok != c.ok {
			t.Fatalf("%s: ok=%v istenen %v", c.name, ok, c.ok)
		}
		if !ok {
			continue
		}
		if s.Band != c.band || s.Score != c.score {
			t.Errorf("%s: bant=%s puan=%.3f — istenen %s/%.2f", c.name, s.Band, s.Score, c.band, c.score)
		}
	}
}

func TestScoreMultipliersAndBonuses(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		detectedBy string
		matched    string
		want       float64
	}{
		{"stalled ×1.10", StatusStalled, "spans", MatchService, 0.99 /* tavan */},
		{"rolled_back düşük bantta ×1.10", StatusRolledBack, "spans", MatchService, 0.55},
		{"superseded ×0.70", StatusSuperseded, "spans", MatchService, 0.63},
		{"ksm bonusu", StatusCompleted, "spans+ksm", MatchService, 0.95},
		{"pod bonusu", StatusCompleted, "spans", MatchPod, 0.95},
		{"ksm + pod tavana çarpar", StatusCompleted, "spans+ksm", MatchPod, 0.98},
	}
	for _, c := range cases {
		age := 10.0
		if c.name == "rolled_back düşük bantta ×1.10" {
			age = 60
		}
		s, ok := Score(onset, cand(age, c.status, c.detectedBy, c.matched))
		if !ok {
			t.Fatalf("%s: aday reddedildi", c.name)
		}
		want := c.want
		if want > maxScore {
			want = maxScore
		}
		if s.Score != want {
			t.Errorf("%s: puan %.3f, istenen %.3f", c.name, s.Score, want)
		}
	}
}

func TestScoreZeroTimesRejected(t *testing.T) {
	c := cand(5, StatusCompleted, "spans", MatchService)
	c.Rollout.StartedAt = time.Time{}
	if _, ok := Score(onset, c); ok {
		t.Error("started_at sıfır → aday olmamalı")
	}
	if _, ok := Score(time.Time{}, cand(5, StatusCompleted, "spans", MatchService)); ok {
		t.Error("onset sıfır → aday olmamalı")
	}
}

func TestRankDedupSortCap(t *testing.T) {
	// Aynı revizyon iki eşlemeyle: pod (0.95) servis (0.90) → tek satır, 0.95.
	svc := cand(10, StatusCompleted, "spans", MatchService)
	pod := svc
	pod.MatchedBy = MatchPod
	// Farklı iş yükleri: düşük bant, yüksek bant, pencere dışı.
	low := cand(90, StatusCompleted, "spans", MatchService)
	low.Rollout.Workload = "worker"
	high := cand(20, StatusCompleted, "spans", MatchService)
	high.Rollout.Workload = "gateway"
	out := cand(200, StatusCompleted, "spans", MatchService)
	out.Rollout.Workload = "old"
	late := cand(-30, StatusCompleted, "spans", MatchService)
	late.Rollout.Workload = "after"
	extra := cand(25, StatusCompleted, "spans", MatchService)
	extra.Rollout.Workload = "fifth"

	got := Rank(onset, []Candidate{svc, low, out, pod, high, late, extra})
	if len(got) != MaxScored {
		t.Fatalf("%d satır, istenen %d (kesme)", len(got), MaxScored)
	}
	if got[0].Rollout.Workload != "api" || got[0].Score != 0.95 || got[0].MatchedBy != MatchPod {
		t.Errorf("ilk satır pod eşlemeli api 0.95 olmalı: %+v", got[0])
	}
	// 0.90'lık iki aday: yakın olan (gateway 20 dk) fifth'ten (25 dk) önce.
	if got[1].Rollout.Workload != "gateway" || got[2].Rollout.Workload != "fifth" {
		t.Errorf("eşit puanda yakın olan önce: %s, %s", got[1].Rollout.Workload, got[2].Rollout.Workload)
	}
	for _, s := range got {
		if s.Rollout.Workload == "old" || s.Rollout.Workload == "after" {
			t.Errorf("pencere dışı aday listede: %s", s.Rollout.Workload)
		}
	}
}

func TestReasonMentionsImagesAndStatus(t *testing.T) {
	c := cand(12, StatusStalled, "spans+ksm", MatchPod)
	c.Rollout.PrevImageTag, c.Rollout.ImageTag = "1.4.0", "1.5.0"
	s, _ := Score(onset, c)
	for _, want := range []string{"ns/api", "1.4.0 → 1.5.0", "12 dk önce", "takıldı", "pod'u"} {
		if !strings.Contains(s.Reason, want) {
			t.Errorf("reason %q içinde %q yok", s.Reason, want)
		}
	}
	if SubjectID(c.Rollout) != "rollout:c1/ns/api@api-stalled" {
		t.Errorf("SubjectID: %s", SubjectID(c.Rollout))
	}
}
