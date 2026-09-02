package anomaly

// rollout_causes_test.go — v0.10.242 Problem↔Rollout korelasyonu D2.
// Sözleşme: ns→time sınırı (StartedAt ns, rollout started_at time.Time:
// 10 dk önceki rollout AgeMin=10), küme çevirisi (span değeri →
// EffectiveID; harita doluyken bilinmeyen değer düşer, boşken geçer),
// servis eşlemesi revizyondan bağımsız, pod eşlemesi revizyon-bağımlı,
// rollout yoksa boş.

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/rollout"
)

func TestRolloutWindowNs(t *testing.T) {
	onset := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	from, to := rolloutWindow(onset.UnixNano())
	if from != onset.Add(-120*time.Minute) || to != onset.Add(5*time.Minute) {
		t.Fatalf("pencere [%s, %s]", from, to)
	}
	// ns girdisi ms gibi okunsaydı 1970'e düşerdi — 10 dk önceki rollout
	// AgeMin=10 verir, birim karışımı yok.
	r := rollout.Rollout{ClusterID: "c", Namespace: "ns", Workload: "api", Revision: "r2",
		StartedAt: onset.Add(-10 * time.Minute), Status: rollout.StatusCompleted}
	sc := rollout.Rank(time.Unix(0, onset.UnixNano()).UTC(), []rollout.Candidate{{Rollout: r, MatchedBy: rollout.MatchService}})
	if len(sc) != 1 || sc[0].AgeMin != 10 {
		t.Fatalf("AgeMin: %+v", sc)
	}
}

func TestClusterIDMapAndResolve(t *testing.T) {
	m := clusterIDMap([]rollout.ClusterRef{
		{ID: "prod-eu", SpanClusterValue: "eu1", SpanClusterValues: []string{"eu1", "ocp-eu"}},
		{ID: "prod-us", SpanClusterValue: "us1"},
	})
	for v, want := range map[string]string{"eu1": "prod-eu", "ocp-eu": "prod-eu", "us1": "prod-us"} {
		if id, ok := resolveClusterID(m, v); !ok || id != want {
			t.Errorf("%s → %s/%v, istenen %s", v, id, ok, want)
		}
	}
	if _, ok := resolveClusterID(m, "unknown"); ok {
		t.Error("harita doluyken bilinmeyen değer eşleşmemeli")
	}
	if id, ok := resolveClusterID(map[string]string{}, "raw"); !ok || id != "raw" {
		t.Error("harita boşken değer olduğu gibi geçmeli")
	}
}

func TestBuildRolloutCandidates(t *testing.T) {
	m := map[string]string{"eu1": "prod-eu"}
	refs := []chstore.WorkloadRevisionRef{
		{Cluster: "eu1", Namespace: "pay", Workload: "api", Revision: "api-old"},
		{Cluster: "zz", Namespace: "pay", Workload: "ghost", Revision: "x"}, // bilinmeyen küme → düşer
	}
	pod := &chstore.WorkloadRevisionRef{Cluster: "eu1", Namespace: "pay", Workload: "worker", Revision: "worker-new"}
	rows := []chstore.RolloutRow{
		{Rollout: rollout.Rollout{ClusterID: "prod-eu", Namespace: "pay", Workload: "api", Revision: "api-new"}},       // servis: revizyon farklı olsa da eşleşir
		{Rollout: rollout.Rollout{ClusterID: "prod-eu", Namespace: "pay", Workload: "worker", Revision: "worker-new"}}, // pod: revizyon aynı
		{Rollout: rollout.Rollout{ClusterID: "prod-eu", Namespace: "pay", Workload: "worker", Revision: "worker-old"}}, // pod iş yükü, eski revizyon → servis eşlemesi
		{Rollout: rollout.Rollout{ClusterID: "prod-eu", Namespace: "other", Workload: "api", Revision: "n"}},           // başka ns → yok
	}
	keys := workloadKeys(refs, pod, m)
	if len(keys) != 2 {
		t.Fatalf("anahtar sayısı %d: %+v", len(keys), keys)
	}
	got := buildRolloutCandidates(refs, pod, rows, m)
	if len(got) != 3 {
		t.Fatalf("aday sayısı %d: %+v", len(got), got)
	}
	want := map[string]string{"api-new": rollout.MatchService, "worker-new": rollout.MatchPod, "worker-old": rollout.MatchService}
	for _, c := range got {
		if want[c.Rollout.Revision] != c.MatchedBy {
			t.Errorf("%s: %s, istenen %s", c.Rollout.Revision, c.MatchedBy, want[c.Rollout.Revision])
		}
	}
	if got := buildRolloutCandidates(refs, pod, nil, m); len(got) != 0 {
		t.Error("rollout yokken aday olmamalı")
	}
}

func TestRolloutEvidenceFrom(t *testing.T) {
	st := time.Date(2026, 9, 2, 11, 50, 0, 0, time.UTC)
	ev := rolloutEvidenceFrom(rollout.Scored{
		Rollout:   rollout.Rollout{ClusterID: "c", Namespace: "ns", Workload: "api", Kind: "Deployment", Revision: "r", StartedAt: st, Status: rollout.StatusStalled, ImageTag: "2.0", PrevImageTag: "1.9", DetectedBy: "spans+ksm"},
		MatchedBy: rollout.MatchPod, AgeMin: 10, Band: rollout.BandHigh, Score: 0.98, Reason: "x",
	})
	if ev.StartedAtNs != st.UnixNano() || ev.MatchedBy != "pod" || ev.Score != 0.98 || ev.ImageTag != "2.0" || ev.Kind != "Deployment" {
		t.Errorf("kanıt satırı: %+v", ev)
	}
}
