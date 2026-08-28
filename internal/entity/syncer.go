package entity

import (
	"context"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// syncer.go — THANOS SENKRONİZASYONU (design §3).
//
// Her tick: Clusters() (Remote Cluster listesi — her tick okunur, yeni
// cluster restart'sız gelir, kaldırılan cluster sync edilmez ama kayıtları
// kalır) → cluster başına bounded paralel `syncCluster` → altı seçicili
// anlık sorgu (SnapshotQueries; matcher doQuery'de) → Normalize → açık
// ömürlerle Diff → tam-satır INSERT → sync_runs satırı. Bir cluster'ın
// hatası diğerlerini etkilemez; hatalı cluster'da HİÇBİR ömür kapanmaz
// (yokluk bilinmiyor). Silme yok.
//
// Lider kilidi ve ticker'ı çağıran (main.go) kurar: cache.LeaderHolder
// key "entity-sync"; Tick, IsLeader() true iken çağrılır.

// ClusterRef — Remote Cluster kaydının syncer'a gereken kısmı.
type ClusterRef struct {
	ID               string
	Name             string
	NamespaceFilter  string // thanos.nsMatcher çıktısı DEĞİL, ham regex
	SpanClusterValue string // span `cluster` kolonundaki değer (boş = Name)
}

// Source — Thanos kapısı (thanos.Service adaptörü; testte sahte).
type Source interface {
	Clusters() []ClusterRef
	Fetch(ctx context.Context, c ClusterRef, queries map[string]string) (SampleSets, error)
}

// EntityRow / RelationRow — chstore'a giden tam satırlar.
type EntityRow struct {
	Type, ClusterID, ID, Namespace, Name, UID, ParentID string
	ValidFrom, ValidTo, FirstSeen, LastSeen             time.Time
	LabelKeys, LabelValues                              []string
	Source                                              string
	Stale                                               bool
}

type RelationRow struct {
	Type, ClusterID, ParentID, ChildID string
	ValidFrom, ValidTo, LastSeen       time.Time
	Source                             string
}

// Run — entity_sync_runs satırı.
type Run struct {
	ClusterID, Status string
	StartedAt         time.Time
	FinishedAt        time.Time
	EntitiesWritten   int
	RelationsWritten  int
	Closed            int
	UnmappedKeys      []string
	UnmappedCounts    []uint32
	ThanosMs, CHMs    int
	Error             string
}

const (
	RunOK      = "ok"
	RunPartial = "partial"
	RunFailed  = "failed"
	RunSkipped = "skipped"
)

// Store — CH kapısı (chstore adaptörü; testte bellek).
type Store interface {
	OpenLifetimes(ctx context.Context, cid string) (map[string]Lifetime, error)
	Apply(ctx context.Context, cid string, rows []EntityRow, rels []RelationRow) error
	RecordRun(ctx context.Context, run Run) error
}

// Observability — /api/admin/entities/sync + SystemStats.
type Observability struct {
	Ticks            int64
	ClustersOK       int64
	ClustersFailed   int64
	EntitiesWritten  int64
	RelationsWritten int64
	LastTickMs       int64
	LastTickAt       time.Time
}

type clusterState struct {
	prevEnts map[string]Entity // son BAŞARILI anlık görüntünün varlıkları (kapanış satırları tam alan taşısın)
	lastOK   time.Time
	lastErr  string
}

// Syncer — durumlu işçi.
type Syncer struct {
	src      Source
	store    Store
	seen     SeenReader // nil = span geçişi yok
	settings func() Resolved
	now      func() time.Time

	mu    sync.Mutex
	state map[string]*clusterState

	ticks, okN, failN, entsN, relsN, lastMs atomic.Int64
	lastAt                                  atomic.Int64
}

func NewSyncer(src Source, store Store, settings func() Resolved) *Syncer {
	return &Syncer{src: src, store: store, settings: settings, now: time.Now, state: map[string]*clusterState{}}
}

// SetSeenReader — span-türevli geçiş kaynağı (chstore.EntitySeenRecent).
func (s *Syncer) SetSeenReader(r SeenReader) { s.seen = r }

func (s *Syncer) Observability() Observability {
	return Observability{
		Ticks: s.ticks.Load(), ClustersOK: s.okN.Load(), ClustersFailed: s.failN.Load(),
		EntitiesWritten: s.entsN.Load(), RelationsWritten: s.relsN.Load(), LastTickMs: s.lastMs.Load(),
		LastTickAt: time.Unix(0, s.lastAt.Load()),
	}
}

// Run — lider olduğu sürece periyodik tick (çağıran IsLeader ile sarar).
func (s *Syncer) Run(ctx context.Context, isLeader func() bool) {
	for {
		r := s.settings()
		iv := r.SyncInterval
		if iv <= 0 {
			iv = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(iv):
		}
		if isLeader != nil && !isLeader() {
			continue
		}
		s.Tick(ctx)
	}
}

// Tick — bir senkronizasyon turu (bayrak kapalıysa no-op).
func (s *Syncer) Tick(ctx context.Context) {
	r := s.settings()
	if !r.Enabled {
		return
	}
	start := s.now()
	clusters := s.src.Clusters()
	// Span geçişi: tek okuma, cluster'a bölünür; eşlenemeyenler ayrı koşu satırı.
	spanByCID := map[string][]SeenRow{}
	if s.seen != nil {
		rows, err := s.seen.RecentSeen(ctx, start.Add(-seenLookback(r.SyncInterval)))
		if err != nil {
			log.Printf("[entity] entity_seen okunamadı (span geçişi atlandı): %v", err)
		} else {
			var unmapped map[string]int
			spanByCID, unmapped = GroupSeenByCluster(rows, clusters)
			if len(unmapped) > 0 {
				keys, counts := sortedUnmapped(unmapped)
				run := Run{ClusterID: UnmappedClusterID, StartedAt: start, FinishedAt: s.now(), Status: RunOK,
					UnmappedKeys: keys, UnmappedCounts: counts}
				if err := s.store.RecordRun(ctx, run); err != nil {
					log.Printf("[entity] unmapped koşu satırı yazılamadı: %v", err)
				}
			}
		}
	}
	par := r.ParallelClusters
	if par <= 0 {
		par = 1
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for _, c := range clusters {
		if c.ID == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c ClusterRef, extra []SeenRow) {
			defer wg.Done()
			defer func() { <-sem }()
			s.syncCluster(ctx, c, r, extra)
		}(c, spanByCID[c.ID])
	}
	wg.Wait()
	s.ticks.Add(1)
	s.lastMs.Store(int64(s.now().Sub(start) / time.Millisecond))
	s.lastAt.Store(s.now().UnixNano())
}

func (s *Syncer) clusterState(cid string) *clusterState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[cid]
	if !ok {
		st = &clusterState{prevEnts: map[string]Entity{}}
		s.state[cid] = st
	}
	return st
}

// syncCluster — tek cluster; hata = sync_runs failed, hiçbir yazım yok.
func (s *Syncer) syncCluster(ctx context.Context, c ClusterRef, r Resolved, spanRows []SeenRow) {
	now := s.now()
	run := Run{ClusterID: c.ID, StartedAt: now}
	fail := func(err error) {
		s.failN.Add(1)
		run.Status, run.Error, run.FinishedAt = RunFailed, err.Error(), s.now()
		st := s.clusterState(c.ID)
		s.mu.Lock()
		st.lastErr = err.Error()
		s.mu.Unlock()
		if rerr := s.store.RecordRun(ctx, run); rerr != nil {
			log.Printf("[entity] sync_runs yazılamadı (%s): %v", c.ID, rerr)
		}
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t0 := s.now()
	sets, err := s.src.Fetch(tctx, c, SnapshotQueries(nsMatcher(c.NamespaceFilter)))
	run.ThanosMs = int(s.now().Sub(t0) / time.Millisecond)
	if err != nil {
		fail(err)
		return
	}
	ents, rels := Normalize(c.ID, SnapshotFromSamples(sets))
	seen := make(map[string]Entity, len(ents)+len(rels))
	entByID := make(map[string]Entity, len(ents))
	for _, e := range ents {
		seen[e.ID] = e
		entByID[e.ID] = e
	}
	// Span geçişi AYNI diff'e girer (ayrı diff, görülmeyen her şeyi kapatırdı).
	if len(spanRows) > 0 {
		se, sr := SpanSeenToEntities(c.ID, spanRows, entByID)
		for _, e := range se {
			if _, ok := entByID[e.ID]; !ok {
				seen[e.ID] = e
				entByID[e.ID] = e
			}
		}
		rels = append(rels, sr...)
	}
	relByKey := make(map[string]Relation, len(rels))
	for _, rl := range rels {
		k := relKey(rl.Type, rl.ParentID, rl.ChildID)
		if _, dup := relByKey[k]; dup {
			continue // Thanos'unki önce geldi, kazanır
		}
		seen[k] = Entity{ID: k}
		relByKey[k] = rl
	}
	prev, err := s.store.OpenLifetimes(ctx, c.ID)
	if err != nil {
		fail(err)
		return
	}
	ch := DiffLifetimes(now, prev, seen, r.PodGap, true)
	st := s.clusterState(c.ID)
	s.mu.Lock()
	prevEnts := st.prevEnts
	s.mu.Unlock()
	rows := RowsForChange(c.ID, ch, entByID, prevEnts)
	relRows := relationRowsForChange(c.ID, ch, relByKey)
	t1 := s.now()
	if err := s.store.Apply(ctx, c.ID, rows, relRows); err != nil {
		fail(err)
		return
	}
	run.CHMs = int(s.now().Sub(t1) / time.Millisecond)
	run.Status, run.FinishedAt = RunOK, s.now()
	run.EntitiesWritten, run.RelationsWritten = len(rows), len(relRows)
	for _, l := range ch.Close {
		if _, isRel := relByKey[l.ID]; !isRel {
			if _, wasEnt := prevEnts[l.ID]; wasEnt || ParseIDOK(l.ID) {
				run.Closed++
			}
		}
	}
	s.mu.Lock()
	st.prevEnts, st.lastOK, st.lastErr = entByID, now, ""
	s.mu.Unlock()
	s.okN.Add(1)
	s.entsN.Add(int64(len(rows)))
	s.relsN.Add(int64(len(relRows)))
	if err := s.store.RecordRun(ctx, run); err != nil {
		log.Printf("[entity] sync_runs yazılamadı (%s): %v", c.ID, err)
	}
}

// ParseIDOK — kapanış sayacı için: id bir varlık mı (ilişki anahtarı değil).
func ParseIDOK(id string) bool { _, ok := ParseID(id); return ok }

// relKey — ilişki ömrünün diff anahtarı (entity_id uzayıyla çakışmaz:
// "rel|" öneki hiçbir tip öneki değil).
func relKey(typ, parent, child string) string { return "rel|" + typ + "|" + parent + "|" + child }

// nsMatcher — thanos.nsMatcher'ın aynası (import döngüsü olmasın): boş →
// boş; doluysa `,namespace=~"<re>"` (tırnak/ters bölü kaçışlı).
func nsMatcher(re string) string {
	if re == "" {
		return ""
	}
	esc := ""
	for _, ch := range re {
		switch ch {
		case '\\':
			esc += `\\`
		case '"':
			esc += `\"`
		default:
			esc += string(ch)
		}
	}
	return `,namespace=~"` + esc + `"`
}

// sortedLabels — Array çifti, anahtara göre sıralı (deterministik satır).
func sortedLabels(m map[string]string) ([]string, []string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = m[k]
	}
	return keys, vals
}

// RowsForChange — ömür değişikliği → tam satırlar (invariant #4: her satır
// TÜM alanları taşır; kapanış satırı önceki anlık görüntünün alanlarıyla).
func RowsForChange(cid string, ch Change, cur, prev map[string]Entity) []EntityRow {
	out := make([]EntityRow, 0, len(ch.Open)+len(ch.Close)+len(ch.Refresh))
	mk := func(e Entity, l Lifetime) EntityRow {
		lk, lv := sortedLabels(e.Labels)
		if lk == nil {
			lk, lv = []string{}, []string{}
		}
		uid := l.UID
		if uid == "" {
			uid = e.UID
		}
		return EntityRow{
			Type: e.Type, ClusterID: cid, ID: l.ID, Namespace: e.Namespace, Name: e.Name, UID: uid,
			ParentID: e.ParentID, ValidFrom: l.ValidFrom, ValidTo: l.ValidTo, FirstSeen: l.ValidFrom,
			LastSeen: l.LastSeen, LabelKeys: lk, LabelValues: lv, Source: e.Source,
		}
	}
	lookup := func(id string) (Entity, bool) {
		if e, ok := cur[id]; ok {
			return e, true
		}
		e, ok := prev[id]
		return e, ok
	}
	for _, l := range ch.Open {
		if e, ok := cur[l.ID]; ok {
			out = append(out, mk(e, l))
		}
	}
	for _, l := range ch.Refresh {
		if e, ok := lookup(l.ID); ok {
			out = append(out, mk(e, l))
		}
	}
	for _, l := range ch.Close {
		if e, ok := lookup(l.ID); ok {
			out = append(out, mk(e, l))
		} else if ref, ok := ParseID(l.ID); ok {
			// Önceki anlık görüntü bellekte yok (restart sonrası ilk tick):
			// kimlikten türetilebilen alanlarla kapat — satır boş kalmaz.
			out = append(out, mk(Entity{Type: ref.Type, ClusterID: cid, ID: l.ID, Namespace: ref.Namespace, Name: ref.Name, Source: SourceThanos}, l))
		}
	}
	return out
}

// relationRowsForChange — ilişki ömürleri (anahtar "rel|…").
func relationRowsForChange(cid string, ch Change, rels map[string]Relation) []RelationRow {
	var out []RelationRow
	mk := func(l Lifetime) (RelationRow, bool) {
		r, ok := rels[l.ID]
		if !ok {
			// kapanış: anahtardan çöz
			typ, parent, child, pok := parseRelKey(l.ID)
			if !pok {
				return RelationRow{}, false
			}
			r = Relation{Type: typ, ClusterID: cid, ParentID: parent, ChildID: child, Source: SourceThanos}
		}
		return RelationRow{Type: r.Type, ClusterID: cid, ParentID: r.ParentID, ChildID: r.ChildID,
			ValidFrom: l.ValidFrom, ValidTo: l.ValidTo, LastSeen: l.LastSeen, Source: r.Source}, true
	}
	for _, group := range [][]Lifetime{ch.Open, ch.Refresh, ch.Close} {
		for _, l := range group {
			if _, isRel := rels[l.ID]; !isRel && !isRelKey(l.ID) {
				continue
			}
			if row, ok := mk(l); ok {
				out = append(out, row)
			}
		}
	}
	return out
}

func isRelKey(k string) bool { return len(k) > 4 && k[:4] == "rel|" }

func parseRelKey(k string) (typ, parent, child string, ok bool) {
	if !isRelKey(k) {
		return "", "", "", false
	}
	rest := k[4:]
	i := indexByte(rest, '|')
	if i < 0 {
		return "", "", "", false
	}
	typ = rest[:i]
	rest = rest[i+1:]
	j := indexByte(rest, '|')
	if j < 0 {
		return "", "", "", false
	}
	return typ, rest[:j], rest[j+1:], true
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
