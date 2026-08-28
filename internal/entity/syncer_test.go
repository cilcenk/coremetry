package entity

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// v0.10.129 — senkronizasyon: sahte Thanos + bellek-içi store ile.
// Görev testleri: kısmi senkronizasyon (bazı cluster'lar yanıt vermezken
// tutarlılık), kesinti/resync, eşlenemeyen cluster değeri sayaçlanır,
// silme YOK (yaşlandırma).

type fakeSource struct {
	mu       sync.Mutex
	clusters []ClusterRef
	sets     map[string]SampleSets // cid → yanıt
	errs     map[string]error      // cid → hata
	calls    map[string]int
}

func (f *fakeSource) Clusters() []ClusterRef { return f.clusters }
func (f *fakeSource) Fetch(ctx context.Context, c ClusterRef, queries map[string]string) (SampleSets, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[c.ID]++
	if err := f.errs[c.ID]; err != nil {
		return SampleSets{}, err
	}
	return f.sets[c.ID], nil
}

type memStore struct {
	mu   sync.Mutex
	open map[string]map[string]Lifetime // cid → id → açık ömür
	rows []EntityRow
	rels []RelationRow
	runs []Run
}

func newMemStore() *memStore { return &memStore{open: map[string]map[string]Lifetime{}} }
func (m *memStore) OpenLifetimes(ctx context.Context, cid string) (map[string]Lifetime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Lifetime{}
	for k, v := range m.open[cid] {
		out[k] = v
	}
	return out, nil
}
func (m *memStore) Apply(ctx context.Context, cid string, rows []EntityRow, rels []RelationRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open[cid] == nil {
		m.open[cid] = map[string]Lifetime{}
	}
	// RMT dedup anahtarı (id, valid_from): kapanış yalnız AYNI ömrü kapatır —
	// aynı tick'te kapat+aç gelirse yeni ömür açık kalır.
	for _, r := range rows {
		m.rows = append(m.rows, r)
		key := r.ID
		if r.ValidTo.IsZero() {
			m.open[cid][key] = Lifetime{ID: r.ID, UID: r.UID, ValidFrom: r.ValidFrom, LastSeen: r.LastSeen}
		} else if cur, ok := m.open[cid][key]; ok && cur.ValidFrom.Equal(r.ValidFrom) {
			delete(m.open[cid], key)
		}
	}
	for _, r := range rels {
		m.rels = append(m.rels, r)
		key := relKey(r.Type, r.ParentID, r.ChildID)
		if r.ValidTo.IsZero() {
			m.open[cid][key] = Lifetime{ID: key, ValidFrom: r.ValidFrom, LastSeen: r.LastSeen}
		} else if cur, ok := m.open[cid][key]; ok && cur.ValidFrom.Equal(r.ValidFrom) {
			delete(m.open[cid], key)
		}
	}
	return nil
}
func (m *memStore) RecordRun(ctx context.Context, run Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, run)
	return nil
}

func podSet(pods ...string) SampleSets {
	var ss SampleSets
	ss.NodeInfo = []Sample{{Labels: map[string]string{"node": "w1"}}}
	for _, p := range pods {
		ss.PodInfo = append(ss.PodInfo, Sample{Labels: map[string]string{"namespace": "ns", "pod": p, "uid": "u-" + p, "node": "w1"}})
		ss.PodOwner = append(ss.PodOwner, Sample{Labels: map[string]string{"namespace": "ns", "pod": p, "owner_kind": "StatefulSet", "owner_name": "db"}})
	}
	return ss
}

func TestSyncerPartialFailureAndLifecycle(t *testing.T) {
	src := &fakeSource{
		clusters: []ClusterRef{{ID: "c-a", Name: "a"}, {ID: "c-b", Name: "b"}},
		sets:     map[string]SampleSets{"c-a": podSet("db-0", "db-1"), "c-b": podSet("db-0")},
		errs:     map[string]error{"c-b": errors.New("thanos b: HTTP 503")},
	}
	st := newMemStore()
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	sy := NewSyncer(src, st, func() Resolved {
		return Resolved{Enabled: true, SyncInterval: time.Minute, PodGap: 10 * time.Minute, StaleAfter: 24 * time.Hour, ParallelClusters: 2}
	})
	sy.now = func() time.Time { return t0 }

	// Tick 1: a başarılı, b hatalı.
	sy.Tick(context.Background())
	if len(st.runs) != 2 {
		t.Fatalf("iki cluster iki koşu kaydı, alınan %d", len(st.runs))
	}
	byCid := map[string]Run{}
	for _, r := range st.runs {
		byCid[r.ClusterID] = r
	}
	if byCid["c-a"].Status != RunOK || byCid["c-a"].EntitiesWritten == 0 {
		t.Fatalf("a: %+v", byCid["c-a"])
	}
	if byCid["c-b"].Status != RunFailed || !strings.Contains(byCid["c-b"].Error, "503") || byCid["c-b"].EntitiesWritten != 0 {
		t.Fatalf("b hatalı kaydedilmeli, yazım 0: %+v", byCid["c-b"])
	}
	if _, ok := st.open["c-a"][PodID("c-a", "ns", "db-1")]; !ok {
		t.Fatal("a'nın pod'ları açık ömür olmalı")
	}
	if len(st.open["c-b"]) != 0 {
		t.Fatal("b'ye hiçbir şey yazılmamalı")
	}
	obs := sy.Observability()
	if obs.Ticks != 1 || obs.ClustersOK != 1 || obs.ClustersFailed != 1 {
		t.Fatalf("obs: %+v", obs)
	}

	// Tick 2 (2 dk sonra): a'da db-1 gitti → kapanır; b hâlâ hatalı → dokunulmaz.
	t1 := t0.Add(2 * time.Minute)
	sy.now = func() time.Time { return t1 }
	src.sets["c-a"] = podSet("db-0")
	sy.Tick(context.Background())
	if _, still := st.open["c-a"][PodID("c-a", "ns", "db-1")]; still {
		t.Fatal("görülmeyen pod ömrü kapanmalı (cluster ulaşıldı)")
	}
	var closedDB1 bool
	for _, r := range st.rows {
		if r.ID == PodID("c-a", "ns", "db-1") && !r.ValidTo.IsZero() {
			closedDB1 = true
			if !r.ValidTo.Equal(t0) {
				t.Fatalf("kapanış eski last_seen'de olmalı: %v", r.ValidTo)
			}
		}
	}
	if !closedDB1 {
		t.Fatal("db-1 kapanış satırı yazılmalı")
	}
	// runs_on ilişkisi de yazılmış olmalı (pod→node).
	var runsOn bool
	for _, r := range st.rels {
		if r.Type == RelRunsOn && r.ParentID == PodID("c-a", "ns", "db-0") && r.ChildID == NodeID("c-a", "w1") {
			runsOn = true
		}
	}
	if !runsOn {
		t.Fatal("pod→node runs_on ilişkisi yazılmalı")
	}

	// Tick 3: b toparlandı → yazılır; a'nın db-0'ı 15 dk görülmeyip geri geldi → yeni ömür.
	t2 := t1.Add(15 * time.Minute)
	sy.now = func() time.Time { return t2 }
	delete(src.errs, "c-b")
	sy.Tick(context.Background())
	if len(st.open["c-b"]) == 0 {
		t.Fatal("toparlanan cluster yazılmalı")
	}
	lt := st.open["c-a"][PodID("c-a", "ns", "db-0")]
	if !lt.ValidFrom.Equal(t2) {
		t.Fatalf("podGap aşıldı → yeni ömür now'da açılmalı: %+v", lt)
	}
	// Bayrak kapalı → tick hiçbir şey yapmaz.
	sy.settings = func() Resolved { return Resolved{Enabled: false} }
	before := len(st.runs)
	sy.Tick(context.Background())
	if len(st.runs) != before {
		t.Fatal("bayrak kapalıyken koşu olmamalı")
	}
}

// Cluster kaldırıldı (Settings'ten silindi): kayıtları SİLİNMEZ; yalnız
// artık sync edilmez. Yeni cluster eklendi: sonraki tick'te alınır
// (restart yok — Clusters() her tick okunur).
func TestSyncerClusterAddRemoveWithoutRestart(t *testing.T) {
	src := &fakeSource{clusters: []ClusterRef{{ID: "c-a", Name: "a"}}, sets: map[string]SampleSets{"c-a": podSet("p1"), "c-n": podSet("q1")}}
	st := newMemStore()
	sy := NewSyncer(src, st, func() Resolved { return Resolved{Enabled: true, PodGap: 10 * time.Minute, ParallelClusters: 1} })
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	sy.now = func() time.Time { return t0 }
	sy.Tick(context.Background())
	src.clusters = []ClusterRef{{ID: "c-n", Name: "n"}} // a kaldırıldı, n eklendi
	sy.now = func() time.Time { return t0.Add(time.Minute) }
	sy.Tick(context.Background())
	if len(st.open["c-a"]) == 0 {
		t.Fatal("kaldırılan cluster'ın kayıtları korunmalı (silme yok)")
	}
	if len(st.open["c-n"]) == 0 {
		t.Fatal("yeni cluster restart'sız alınmalı")
	}
	ids := []string{}
	for _, r := range st.runs {
		ids = append(ids, r.ClusterID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "c-a,c-n" {
		t.Fatalf("koşular: %v", ids)
	}
}

// Satır üretimi: tam-satır replace — her satır TÜM alanları taşır
// (invariant #4), etiketler Array çifti, kapanış satırı valid_to dolu.
func TestRowsForChange(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ents := map[string]Entity{
		"pod:c/ns/a": {Type: TypePod, ClusterID: "c", ID: "pod:c/ns/a", Namespace: "ns", Name: "a", UID: "u", ParentID: "wl:c/ns/StatefulSet/db",
			Labels: map[string]string{"app": "x", "tier": "db"}, Source: SourceThanos},
	}
	ch := Change{
		Open:    []Lifetime{{ID: "pod:c/ns/a", UID: "u", ValidFrom: t0, LastSeen: t0}},
		Close:   []Lifetime{{ID: "pod:c/ns/gone", ValidFrom: t0.Add(-time.Hour), ValidTo: t0.Add(-time.Minute), LastSeen: t0.Add(-time.Minute)}},
		Refresh: []Lifetime{{ID: "pod:c/ns/b", ValidFrom: t0.Add(-time.Hour), LastSeen: t0}},
	}
	prevEnts := map[string]Entity{
		"pod:c/ns/gone": {Type: TypePod, ClusterID: "c", ID: "pod:c/ns/gone", Namespace: "ns", Name: "gone", Source: SourceThanos},
		"pod:c/ns/b":    {Type: TypePod, ClusterID: "c", ID: "pod:c/ns/b", Namespace: "ns", Name: "b", Source: SourceThanos},
	}
	rows := RowsForChange("c", ch, ents, prevEnts)
	if len(rows) != 3 {
		t.Fatalf("üç satır beklenir, alınan %d", len(rows))
	}
	byID := map[string]EntityRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	a := byID["pod:c/ns/a"]
	if a.Type != TypePod || a.ClusterID != "c" || a.Namespace != "ns" || a.Name != "a" || a.UID != "u" || a.ParentID == "" || a.Source != SourceThanos {
		t.Fatalf("tam satır: %+v", a)
	}
	if len(a.LabelKeys) != 2 || a.LabelKeys[0] != "app" || a.LabelValues[0] != "x" {
		t.Fatalf("etiketler sıralı Array çifti: %+v", a)
	}
	if !a.ValidTo.IsZero() || !a.FirstSeen.Equal(t0) || !a.LastSeen.Equal(t0) {
		t.Fatalf("açık ömür: %+v", a)
	}
	g := byID["pod:c/ns/gone"]
	if g.ValidTo.IsZero() || g.Name != "gone" {
		t.Fatalf("kapanış satırı valid_to + tam alanlar taşımalı: %+v", g)
	}
	b := byID["pod:c/ns/b"]
	if !b.LastSeen.Equal(t0) || !b.ValidFrom.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("tazeleme valid_from korur, last_seen now: %+v", b)
	}
}

func (m *memStore) Existing(context.Context, string, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
