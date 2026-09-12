package anomaly

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// clustering_join_test.go — v0.10.699 (Dynatrace paritesi #1, dilim A):
// join-on-open. Kaskad dakikalara yayıldığında ERKEN açılan bireysel
// problemler kümeye katılır. Saf üçlü (recentOpenCandidates →
// detectAnomalyClusters → mergeTargets) burada mühürlü; kayan-pencere
// simülasyonu (v0.10.199 dersi) tikler arası flip olmadığını gösterir.

var joinTracked = map[string]bool{"error_rate": true, "p99_ms": true}

func joinProblem(id, svc, metric string, startedAt time.Time) *chstore.Problem {
	return &chstore.Problem{
		ID: id, RuleID: "anomaly:" + svc + ":" + metric, Service: svc, Metric: metric,
		Severity: "warning", Status: "open", Comparator: ">", Value: 9, Threshold: 3,
		StartedAt: startedAt.UnixNano(), Description: "x",
	}
}

func TestRecentOpenCandidates(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	fresh := joinProblem("p-a", "shop-cart", "error_rate", now.Add(-5*time.Minute))
	old := joinProblem("p-old", "shop-db", "error_rate", now.Add(-31*time.Minute))
	edge := joinProblem("p-edge", "shop-pay", "p99_ms", now.Add(-30*time.Minute))
	resolved := joinProblem("p-res", "shop-x", "error_rate", now.Add(-1*time.Minute))
	resolved.Status = "resolved"
	cluster := &chstore.Problem{ID: "anomaly-cluster:shop-db", RuleID: "anomaly-cluster:shop-db", Service: "shop-db", Metric: "cluster", Status: "open", StartedAt: now.UnixNano()}
	silent := joinProblem("p-sil", "shop-y", "service_silent", now)
	ext := joinProblem("p-ext", "ext:oracle", "error_rate", now)
	ext.Kind = chstore.ProblemKindExternal
	dropped := joinProblem("p-drop", "shop-api", "p99_ms", now.Add(-2*time.Minute))
	dropped.Comparator = "<"
	resolvingNow := joinProblem("p-rz", "shop-z", "error_rate", now.Add(-2*time.Minute))
	resolving := map[string]bool{resolvingNow.RuleID + "|shop-z": true}

	all := []*chstore.Problem{resolvingNow, silent, dropped, ext, cluster, resolved, edge, old, fresh, nil}
	got := recentOpenCandidates(all, now, clusterJoinWindow, resolving, joinTracked)

	var ids []string
	for _, c := range got {
		if c.Existing == nil {
			t.Fatalf("Existing nil: %+v", c)
		}
		ids = append(ids, c.Existing.ID)
	}
	want := "p-drop,p-a,p-edge" // servis sırası: shop-api, shop-cart, shop-pay
	if strings.Join(ids, ",") != want {
		t.Fatalf("adaylar %v, beklenen %s (eski/çözülmüş/küme/silent/ext/bu-tik-çözülen dışarıda)", ids, want)
	}
	if got[0].Outcome.Direction != "dropped" || got[1].Outcome.Direction != "spiked" {
		t.Fatalf("yön comparator'dan türemeli: %+v", got)
	}
	if got[0].Outcome.Action != "open" || got[0].Outcome.Current != 9 || got[0].Outcome.Median != 3 {
		t.Fatalf("sonuç satırdan: %+v", got[0].Outcome)
	}
}

func TestMergeTargets(t *testing.T) {
	now := time.Now()
	db := joinProblem("p-db", "shop-db", "error_rate", now)
	a := joinProblem("p-a", "shop-a", "error_rate", now)
	cands := []openCandidate{
		{Service: "shop-a", Metric: "error_rate", Existing: a},
		{Service: "shop-b", Metric: "error_rate"}, // taze
		{Service: "shop-db", Metric: "error_rate", Existing: db},
		{Service: "shop-db", Metric: "p99_ms"}, // kaynağın taze satırı
	}
	cl := anomalyCluster{Source: "shop-db", Members: []openCandidate{cands[0], cands[1], cands[0]}}
	got := mergeTargets(cl, cands)
	if len(got) != 2 || got[0].ID != "p-a" || got[1].ID != "p-db" {
		t.Fatalf("hedefler: üye Existing + kaynağın Existing'i, tekrarsız, ID sıralı; got=%+v", got)
	}
	if mergedNote(0) != "" || !strings.Contains(mergedNote(2), "2 previously opened") {
		t.Fatalf("mergedNote: %q / %q", mergedNote(0), mergedNote(2))
	}
	if s := clusterStartedAt(now.UnixNano(), got); s != db.StartedAt && s != a.StartedAt {
		t.Fatalf("küme başlangıcı en eski üyeden: %d", s)
	}
	older := joinProblem("p-old", "shop-c", "p99_ms", now.Add(-10*time.Minute))
	if s := clusterStartedAt(now.UnixNano(), []*chstore.Problem{db, older}); s != older.StartedAt {
		t.Fatalf("en eski StartedAt seçilmeli")
	}
}

// joinSim — saf üçlünün tik simülasyonu. Depo yerine dilim: küme yoksa
// taze açılış bireysel problem olur (StartedAt = tik anı); küme varsa
// hedefler resolved, taze üyeler bastırılır. Döndürdüğü sayaçlar
// tiklerdeki KARARLARDIR — geometri değil gözlenmiş kanıt.
type joinSim struct {
	open     []*chstore.Problem
	adj      []chstore.ServiceEdgePair
	clusters map[string]int // kaynak → açılış sayısı (flip sayacı)
	merged   []string
	seq      int
}

func (s *joinSim) tick(now time.Time, firing map[string]string) (clusterSrc string) {
	var fresh []openCandidate
	for svc, metric := range firing {
		has := false
		for _, p := range s.open {
			if p.Status == "open" && p.Service == svc && p.Metric == metric {
				has = true
			}
		}
		if !has {
			fresh = append(fresh, openCandidate{Service: svc, Metric: metric, Outcome: anomalyOutcome{Action: "open", Severity: "warning", Direction: "spiked"}})
		}
	}
	joined := recentOpenCandidates(s.open, now, clusterJoinWindow, nil, joinTracked)
	cands := append(append([]openCandidate{}, fresh...), joined...)
	cls := detectAnomalyClusters(cands, s.adj, clusterMinMembers)
	if len(cls) == 0 {
		for _, f := range fresh {
			s.seq++
			s.open = append(s.open, joinProblem("p"+itoaClusters(s.seq), f.Service, f.Metric, now))
		}
		return ""
	}
	cl := cls[0]
	s.clusters[cl.Source]++
	for _, tg := range mergeTargets(cl, cands) {
		tg.Status = "resolved"
		s.merged = append(s.merged, tg.ID)
	}
	suppressed := map[string]bool{cl.Source: true}
	for _, m := range cl.Members {
		suppressed[m.Service] = true
	}
	for _, f := range fresh {
		if !suppressed[f.Service] {
			s.seq++
			s.open = append(s.open, joinProblem("p"+itoaClusters(s.seq), f.Service, f.Metric, now))
		}
	}
	return cl.Source
}

func newJoinSim() *joinSim {
	edge := func(caller, callee string, calls, errs uint64) chstore.ServiceEdgePair {
		return chstore.ServiceEdgePair{Caller: caller, Callee: callee, Calls: calls, Errors: errs}
	}
	return &joinSim{
		adj: []chstore.ServiceEdgePair{
			edge("shop-a", "shop-db", 1000, 200), edge("shop-b", "shop-db", 800, 150), edge("shop-c", "shop-db", 600, 90),
		},
		clusters: map[string]int{},
	}
}

// Kaskad: db t0 (tek → küme yok, bireysel), a t+2 (iki → yok, bireysel),
// b t+4 → ÜÇ aday: b taze + db/a önceden açık → küme db; db ve a merged,
// b bastırıldı. Sonraki tiklerde c katılır; küme AYNI kaynakta tek kez
// açılır (flip yok), yeni bireysel problem açılmaz.
func TestJoinOnOpen_CascadeSimulation(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	sim := newJoinSim()
	if src := sim.tick(t0, map[string]string{"shop-db": "error_rate"}); src != "" {
		t.Fatalf("t0: tek aday küme olmaz")
	}
	if src := sim.tick(t0.Add(2*time.Minute), map[string]string{"shop-db": "error_rate", "shop-a": "error_rate"}); src != "" {
		t.Fatalf("t+2: iki aday küme olmaz")
	}
	if n := len(sim.open); n != 2 {
		t.Fatalf("t+2 sonrası 2 bireysel problem beklenir, %d", n)
	}
	src := sim.tick(t0.Add(4*time.Minute), map[string]string{"shop-db": "error_rate", "shop-a": "error_rate", "shop-b": "error_rate"})
	if src != "shop-db" {
		t.Fatalf("t+4: küme kaynağı shop-db olmalı, %q", src)
	}
	if strings.Join(sim.merged, ",") != "p1,p2" {
		t.Fatalf("db ve a önceden açık → merged; got %v", sim.merged)
	}
	if n := len(sim.open); n != 2 {
		t.Fatalf("b bastırılmalı (bireysel açılmaz): open=%d", n)
	}
	// Sonraki 10 tik: hepsi ateşliyor (merged satırlar resolved → taze
	// aday olarak dönerler), c de gelir. Küme tek kez açıldı, flip yok.
	for i := 1; i <= 10; i++ {
		src := sim.tick(t0.Add(time.Duration(4+i)*time.Minute), map[string]string{
			"shop-db": "error_rate", "shop-a": "error_rate", "shop-b": "error_rate", "shop-c": "p99_ms",
		})
		if src != "shop-db" {
			t.Fatalf("tik %d: küme kaynağı değişti: %q", i, src)
		}
	}
	if sim.clusters["shop-db"] != 11 || len(sim.clusters) != 1 {
		t.Fatalf("her tik AYNI küme yeniden tespit edilmeli (tazeleme), başka kaynak yok: %+v", sim.clusters)
	}
	if len(sim.merged) != 2 || len(sim.open) != 2 {
		t.Fatalf("flip: yeni merge/bireysel açılış olmamalı; merged=%v open=%d", sim.merged, len(sim.open))
	}
}

// Pencere kenarı: db'nin problemi 31 dk önce açıldıysa aday DEĞİL →
// a+b taze olsa da kaynak aday kümesinde yok → küme yok (eski bir dert
// yeni kaskada emilmez); a ve b bireysel açılır.
func TestJoinOnOpen_WindowEdgeDoesNotSuckInOldProblem(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	sim := newJoinSim()
	sim.open = append(sim.open, joinProblem("p-old", "shop-db", "error_rate", t0.Add(-31*time.Minute)))
	src := sim.tick(t0, map[string]string{"shop-db": "error_rate", "shop-a": "error_rate", "shop-b": "error_rate"})
	if src != "" {
		t.Fatalf("31 dk önce açılan kaynak kümeye emildi")
	}
	if len(sim.merged) != 0 || len(sim.open) != 3 {
		t.Fatalf("merge olmamalı, a ve b bireysel: merged=%v open=%d", sim.merged, len(sim.open))
	}
	// Aynı senaryo 29 dk: kaynak pencere içinde → küme.
	sim2 := newJoinSim()
	sim2.open = append(sim2.open, joinProblem("p-recent", "shop-db", "error_rate", t0.Add(-29*time.Minute)))
	if src := sim2.tick(t0, map[string]string{"shop-db": "error_rate", "shop-a": "error_rate", "shop-b": "error_rate"}); src != "shop-db" {
		t.Fatalf("29 dk önce açılan kaynak kümeye katılmalı, src=%q", src)
	}
	if strings.Join(sim2.merged, ",") != "p-recent" {
		t.Fatalf("kaynağın kendi problemi merged olmalı: %v", sim2.merged)
	}
}

// Determinizm: aynı snapshot + aynı ateşleme, iki koşu aynı karar.
func TestJoinOnOpen_Deterministic(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	run := func() (string, []string) {
		sim := newJoinSim()
		sim.open = append(sim.open,
			joinProblem("p-b", "shop-b", "error_rate", t0.Add(-3*time.Minute)),
			joinProblem("p-db", "shop-db", "error_rate", t0.Add(-9*time.Minute)),
			joinProblem("p-a", "shop-a", "p99_ms", t0.Add(-6*time.Minute)))
		src := sim.tick(t0, map[string]string{"shop-c": "p99_ms", "shop-db": "error_rate", "shop-a": "p99_ms", "shop-b": "error_rate"})
		return src, sim.merged
	}
	s1, m1 := run()
	s2, m2 := run()
	if s1 != "shop-db" || s1 != s2 || strings.Join(m1, ",") != strings.Join(m2, ",") || strings.Join(m1, ",") != "p-a,p-b,p-db" {
		t.Fatalf("determinizm: %q/%v vs %q/%v", s1, m1, s2, m2)
	}
}

// Scan tarafı pinleri: aday listesi joined'i içerir, merged satır
// tazelenmez, applyClusters cands'ı alır (mergeTargets kaynağı bulsun).
func TestJoinOnOpen_ScanWiring(t *testing.T) {
	src := joinReadSource(t, "anomaly.go")
	for _, want := range []string{
		"recentOpenCandidates(snap.All(), time.Now(), clusterJoinWindow, resolving, trackedSet)",
		"detectAnomalyClusters(cands, adj, clusterMinMembers)",
		"suppressed, merged = d.applyClusters(ctx, clusters, cands, snap, sens, sourceSeverity)",
		"if hasEx && merged[ex.ID] {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("anomaly.go %q içermeli", want)
		}
	}
	cs := joinReadSource(t, "clustering.go")
	if !strings.Contains(cs, "d.mergeIntoCluster(ctx, id, targets, merged)") {
		t.Error("küme açılış VE tazeleme dalı mergeIntoCluster çağırmalı")
	}
	if strings.Count(cs, "d.mergeIntoCluster(ctx, id, targets, merged)") != 2 {
		t.Error("iki dal (açılış + tazeleme) — biri eksik")
	}
}

func joinReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return string(b)
}
