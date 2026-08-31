package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// reconciler.go — ROLLOUT RECONCILER (v0.10.199, Faz 2; audit §2).
//
// Yalnız LİDERDE koşar (cache.LeaderHolder, kendi kilidi
// "rollout-reconciler"; entity-sync kilidini paylaşmaz — entity bayrağı
// kapalıyken rollout koşmalı). Tik: inFlight CAS (SetOnAcquire ile
// örtüşmeye karşı ZORUNLU) → lider mi → süre sınırı (5×aralık, ≥2 dk —
// CH bütçelerinin üstünde) → MV etkinliği (tavan: kesilen iş yükü düşer ve
// ADIYLA ilan edilir) + önceki satırlar (keyset sayfalı, tavansız) → span
// cluster değeri → Remote Cluster id eşlemesi (eşlenmeyen düşer ve İLAN
// edilir) → SAF Reconcile → liderlik YENİDEN doğrulanır → tek upsert → koşu
// kaydı (ayrık kısa ctx: tik süresi dolsa da yazılır; kapanışta kesilen tik
// "skipped"). KSM ayağı Faz 5.
//
// Süreklilik: reconciler koşmadığı süre "sessizlik" sayılmaz — çekirdek
// yalnız TAM kovalara bakar, olay yalnız gözlenmiş girişten doğar ve zayıf
// sinyal status yazmaz. Kapalı bayrak / lider olmayan pod koşu satırı
// YAZMAZ (N pod × N satır olmasın; "hiç koşmadı" ile "kapalı" ayrımı
// ayar + /api/health'te, v0.10.200).

// ClusterRef — registry kaydı (entity.ClusterRef ile aynı şekil; import
// döngüsü olmasın diye kopya).
type ClusterRef struct {
	ID                string
	Name              string
	SpanClusterValue  string
	SpanClusterValues []string
}

// Store — chstore kapısı (testte sahte). RolloutActivity: cut = tavanın
// kestiği (düşürülen) iş yükü anahtarı ("" = kesilmedi).
type Store interface {
	RolloutActivity(ctx context.Context, since time.Time, bucket time.Duration) (rows []ActivityRow, cut string, err error)
	// RolloutFirstSeen — 6 g ufkunda (MV TTL 7 g − 1) (cluster değeri, ns,
	// workload, revision) ilk kovası: bilinen revizyonun dönüşü olay değildir.
	RolloutFirstSeen(ctx context.Context, since time.Time) ([]FirstSeenRow, error)
	RolloutRecentRows(ctx context.Context, since time.Time) ([]Rollout, error)
	RolloutUpsert(ctx context.Context, rows []Rollout) error
	RolloutRecordRun(ctx context.Context, run Run) error
}

// ActivityRow — chstore.RolloutActivityRow'un paket-içi ikizi (cluster = span DEĞERİ).
type ActivityRow struct {
	ClusterValue, Namespace, Workload, Kind, Revision string
	Bucket                                            time.Time
	Spans                                             int64
	FirstSeen, LastSeen                               time.Time
	Image, ImageTag                                   string
}

// FirstSeenRow — chstore.RolloutFirstSeen satırı (cluster = span DEĞERİ).
type FirstSeenRow struct {
	ClusterValue, Namespace, Workload, Revision string
	First                                       time.Time
}

// MapFirstSeen — span cluster değeri → registry id (MapClusters ile aynı
// eşleme). İkinci dönüş: küme (ClusterID) başına MV veri başlangıcı. SAF.
func MapFirstSeen(rows []FirstSeenRow, refs []ClusterRef) (map[Key]map[string]time.Time, map[string]time.Time) {
	byValue := clusterValueIndex(refs)
	out := map[Key]map[string]time.Time{}
	dataStart := map[string]time.Time{}
	for _, r := range rows {
		cid, ok := byValue[r.ClusterValue]
		if !ok || r.Workload == "" || r.Revision == "" {
			continue
		}
		k := Key{cid, r.Namespace, r.Workload}
		if out[k] == nil {
			out[k] = map[string]time.Time{}
		}
		if t, ok := out[k][r.Revision]; !ok || r.First.Before(t) {
			out[k][r.Revision] = r.First
		}
		if t, ok := dataStart[cid]; !ok || r.First.Before(t) {
			dataStart[cid] = r.First
		}
	}
	return out, dataStart
}

// Run — rollout_reconcile_runs satırı. JSON: camelCase + epoch-ms zamanlar
// (RolloutRow ile aynı sözleşme; /api/rollouts/runs v0.10.200).
type Run struct {
	StartedAt, FinishedAt time.Time
	Host                  string // yazan pod (ORDER BY ayırıcısı)
	Status                string // ok | partial | failed | skipped
	Clusters              int
	RolloutsWritten       int
	SpanMs, KSMMs         int
	Error                 string
}

func (r Run) MarshalJSON() ([]byte, error) {
	ms := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.UnixMilli()
	}
	return json.Marshal(struct {
		StartedAt       int64  `json:"startedAt"`
		FinishedAt      int64  `json:"finishedAt"`
		Host            string `json:"host"`
		Status          string `json:"status"`
		Clusters        int    `json:"clusters"`
		RolloutsWritten int    `json:"rolloutsWritten"`
		SpanMs          int    `json:"spanMs"`
		KSMMs           int    `json:"ksmMs"`
		Error           string `json:"error,omitempty"`
	}{ms(r.StartedAt), ms(r.FinishedAt), r.Host, r.Status, r.Clusters, r.RolloutsWritten, r.SpanMs, r.KSMMs, r.Error})
}

// firstSeenHorizon — MV TTL'i 7 g (rollout_schema.go); ufuk TTL'in bir gün
// içi: TTL sınırındaki satır düşmüşse "yeni" sanılmasın.
const firstSeenHorizon = 6 * 24 * time.Hour

const (
	RunOK      = "ok"
	RunPartial = "partial"
	RunFailed  = "failed"
	RunSkipped = "skipped" // kapanışta (ctx iptali) yarıda kesilen tik — arıza değil
)

// ClusterSource — registry (thanos.Service adaptörü; testte sahte).
type ClusterSource interface {
	Clusters() []ClusterRef
}

// Reconciler — tek yazıcı.
type Reconciler struct {
	store    Store
	clusters ClusterSource
	resolved func() Resolved
	leader   func() bool
	inFlight atomic.Bool
	// Observability — son tik (lider pod'un belleği; koşu kaydı CH'de, v0.10.200 /api/health).
	lastRun atomic.Pointer[Run]
	host    string
	// lastStatus — ok→partial/failed geçişinde tek log (her tikte değil).
	lastStatus atomic.Pointer[string]
	now        func() time.Time // testte sabitlenir
	ksm        KSMSource        // Faz 5 (v0.10.212): nil = KSM ayağı kapalı
}

func New(store Store, clusters ClusterSource, resolved func() Resolved) *Reconciler {
	host, _ := os.Hostname()
	return &Reconciler{store: store, clusters: clusters, resolved: resolved, leader: func() bool { return true }, host: host, now: time.Now}
}

func (r *Reconciler) SetLeaderCheck(f func() bool) { r.leader = f }

// SetKSMSource — Faz 5 (v0.10.212): Thanos KSM adaptörü (nil = ayak kapalı).
func (r *Reconciler) SetKSMSource(s KSMSource) { r.ksm = s }

// LastRun — son tikin KOPYASI (canlı işaretçi dışarı sızmaz).
func (r *Reconciler) LastRun() (Run, bool) {
	if p := r.lastRun.Load(); p != nil {
		return *p, true
	}
	return Run{}, false
}

// Run — periyodik döngü; aralık ayardan, uyku 30 s adımlarla (ayar değişince
// kalan süre yeniden hesaplanır — 15 dk → 30 s değişikliği 15 dk gecikmez).
// İlk tik SetOnAcquire'dan gelir (entity syncer emsali: Run bir aralık bekler).
func (r *Reconciler) Run(ctx context.Context) {
	for {
		if !r.sleepInterval(ctx) {
			return
		}
		r.Tick(ctx)
	}
}

func (r *Reconciler) interval() time.Duration {
	iv := r.resolved().Interval
	if iv <= 0 {
		iv = time.Minute // entity emsali: sıfır aralık sıkı döngü olmasın
	}
	return iv
}

func (r *Reconciler) sleepInterval(ctx context.Context) bool {
	start := r.now()
	iv := r.interval()
	for {
		if ctx.Err() != nil {
			return false
		}
		rem := start.Add(iv).Sub(r.now())
		if rem <= 0 {
			return true
		}
		if rem > 30*time.Second {
			rem = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(rem):
		}
		iv = r.interval()
	}
}

// tickDeadline — 5×aralık, en az 2 dk: CH bütçeleri (etkinlik 20 s + satırlar
// 15 s/sayfa + upsert) ve 30 s'lik en kısa aralık için yeterli; süre dolsa
// da koşu kaydı ayrık ctx ile yazılır (finish).
func tickDeadline(interval time.Duration) time.Duration {
	d := 5 * interval
	if d < 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}

// Tick — bir tur; örtüşen çağrı (onAcquire + ticker) atlanır. Döner: koştu mu.
func (r *Reconciler) Tick(ctx context.Context) bool {
	if !r.inFlight.CompareAndSwap(false, true) {
		log.Printf("[rollout] tik atlandı: önceki tik sürüyor")
		return false
	}
	defer r.inFlight.Store(false)
	cfg := r.resolved()
	if !cfg.Enabled || !r.leader() {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, tickDeadline(cfg.Interval))
	defer cancel()
	r.tick(ctx, tctx, cfg)
	return true
}

func clusterValueIndex(refs []ClusterRef) map[string]string {
	byValue := map[string]string{}
	for _, c := range refs {
		if c.ID == "" {
			continue
		}
		vals := append([]string{c.SpanClusterValue}, c.SpanClusterValues...)
		if c.SpanClusterValue == "" && len(c.SpanClusterValues) == 0 {
			vals = []string{c.Name}
		}
		for _, v := range vals {
			if v != "" {
				byValue[v] = c.ID
			}
		}
	}
	return byValue
}

// MapClusters — span cluster değeri → registry id (entity.GroupSeenByCluster deseni). SAF.
func MapClusters(rows []ActivityRow, refs []ClusterRef) ([]Activity, map[string]int) {
	byValue := clusterValueIndex(refs)
	out := make([]Activity, 0, len(rows))
	unmapped := map[string]int{}
	for _, a := range rows {
		cid, ok := byValue[a.ClusterValue]
		if !ok {
			unmapped[a.ClusterValue]++
			continue
		}
		out = append(out, Activity{ClusterID: cid, Namespace: a.Namespace, Workload: a.Workload, Kind: a.Kind, Revision: a.Revision,
			Bucket: a.Bucket, Spans: a.Spans, FirstSeen: a.FirstSeen, LastSeen: a.LastSeen, Image: a.Image, ImageTag: a.ImageTag})
	}
	return out, unmapped
}

// tick — parent: koşu kaydı için (ayrık); ctx: tik bütçesi.
func (r *Reconciler) tick(parent, ctx context.Context, cfg Resolved) {
	run := Run{StartedAt: r.now(), Host: r.host, Status: RunOK}
	var tickErr error
	finish := func() {
		run.FinishedAt = r.now()
		// Kapanış (parent iptali) ile kesilen başarısız tik ARIZA değil: kalıcı
		// "failed" satırı yanlış alarm olurdu.
		if run.Status == RunFailed && errors.Is(tickErr, context.Canceled) && parent.Err() != nil {
			run.Status, run.Error = RunSkipped, "kapanış — tik yarıda kesildi"
		}
		r.lastRun.Store(&run)
		prev := ""
		if p := r.lastStatus.Load(); p != nil {
			prev = *p
		}
		if run.Status != prev && run.Status != RunOK {
			log.Printf("[rollout] tik %s: %s", run.Status, run.Error)
		}
		st := run.Status
		r.lastStatus.Store(&st)
		// Ayrık kısa ctx: tik süresi DOLDUĞUNDA da koşu satırı yazılmalı — tam
		// da görünür olması gereken an.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer rcancel()
		if err := r.store.RolloutRecordRun(rctx, run); err != nil {
			log.Printf("[rollout] koşu kaydı: %v", err)
		}
	}
	defer finish()
	now := r.now()
	t0 := r.now()
	windowStart := AlignBucket(now.Add(-cfg.Lookback), cfg.Bucket)
	rows, cut, err := r.store.RolloutActivity(ctx, windowStart, cfg.Bucket)
	run.SpanMs = int(r.now().Sub(t0) / time.Millisecond)
	if err != nil {
		tickErr = err
		run.Status, run.Error = RunFailed, appendNote(run.Error, "activity: "+err.Error())
		return
	}
	seen, err := r.store.RolloutFirstSeen(ctx, now.Add(-firstSeenHorizon))
	if err != nil {
		tickErr = err
		run.Status, run.Error = RunFailed, appendNote(run.Error, "first-seen: "+err.Error())
		return
	}
	prev, err := r.store.RolloutRecentRows(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		tickErr = err
		run.Status, run.Error = RunFailed, appendNote(run.Error, "rows: "+err.Error())
		return
	}
	refs := r.clusters.Clusters()
	run.Clusters = len(refs)
	acts, unmapped := MapClusters(rows, refs)
	if len(unmapped) > 0 {
		keys := make([]string, 0, len(unmapped))
		for k := range unmapped {
			if k == "" {
				k = "(boş cluster niteliği)"
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		run.Status = RunPartial
		run.Error = appendNote(run.Error, "eşlenmemiş cluster değeri: "+joinMax(keys, 5))
	}
	if cut != "" {
		// chstore kesilen SON iş yükünü ve sonrasını düşürdü (öteki gruplar tam).
		run.Status = RunPartial
		run.Error = appendNote(run.Error, "etkinlik tavanı: "+cut+" ve sonrası bu tikte atlandı")
	}
	firstSeen, dataStart := MapFirstSeen(seen, refs)
	// Faz 5 (v0.10.212): KSM ikinci kanıt — cluster başına hep-ya-da-hiç
	// (audit §16.3): sorgu hatası o cluster'ın KSM'ini bu tikte düşürür ve
	// koşu partial olur; aile yokluğu (nil) hata DEĞİL (spans tek kaynak).
	var ksm map[Key]map[string]KSMRev
	if r.ksm != nil {
		t1 := r.now()
		for _, ref := range refs {
			m, err := r.ksm.FetchKSM(ctx, ref)
			if err != nil {
				run.Status = RunPartial
				run.Error = appendNote(run.Error, "ksm "+ref.Name+": "+err.Error())
				continue
			}
			for k, v := range m {
				if ksm == nil {
					ksm = map[Key]map[string]KSMRev{}
				}
				ksm[k] = v
			}
		}
		run.KSMMs = int(r.now().Sub(t1) / time.Millisecond)
	}
	changed := Reconcile(cfg.Config(), Input{Now: now, WindowStart: windowStart, Prev: prev, Acts: acts, FirstSeen: firstSeen, DataStart: dataStart, Truncated: cut != "", KSM: ksm})
	if len(changed) == 0 {
		return
	}
	for i := range changed {
		if changed[i].DetectedBy == "" {
			changed[i].DetectedBy = "spans"
		}
	}
	// Liderlik yazımdan hemen önce yeniden doğrulanır: uzun bir tik lease'i
	// aşmış olabilir (bayat yazıcı iki liderin satırlarını yarıştırmasın).
	if !r.leader() {
		run.Status, run.Error = RunPartial, appendNote(run.Error, "liderlik tik sırasında kaybedildi — yazım atlandı")
		return
	}
	if err := r.store.RolloutUpsert(ctx, changed); err != nil {
		tickErr = err
		run.Status, run.Error = RunFailed, appendNote(run.Error, "upsert: "+err.Error())
		return
	}
	run.RolloutsWritten = len(changed)
}

func joinMax(xs []string, n int) string {
	if len(xs) > n {
		return strings.Join(xs[:n], ", ") + " … (+" + strconv.Itoa(len(xs)-n) + ")"
	}
	return strings.Join(xs, ", ")
}
