package rollout

import (
	"sort"
	"time"
)

// reconcile.go — ROLLOUT DURUM MAKİNESİ, SAF ÇEKİRDEK (v0.10.199, Faz 2;
// docs/audits/rollouts-audit.md §2.4-2.5, §11, §14.1; üç tur çok-mercekli
// inceleme sonrası dördüncü yazım — model "gözlenmiş GİRİŞ olayı").
//
// Girdi (Input): pencere [WindowStart, son tam kova] için dakikalık etkinlik
// (workload_revision_activity_1m → karar kovasına EPOCH hizalı toplanır),
// tablodaki mevcut olaylar (30 g) ve revizyonların 6 günlük (MV TTL − 1) "ilk
// görülme" ufku (FirstSeen: MV'den ucuz GROUP BY). Çıktı: upsert edilecek satırlar.
//
// MODEL — satır = GÖZLENMİŞ giriş olayı:
//   • Aktif küme: kovada sum(spans) ≥ Threshold olan revizyonlar.
//   • GİRİŞ: revizyon pencere içinde inaktif→aktif geçti ve öncesinde
//     ≥ ExitHysteresis kova boyunca aktif OLMADIĞI GÖZLENDİ (pencere
//     kenarına dayanan yokluk kanıt değildir — kenar bir çukurun içine denk
//     gelebilir); koşu ≥ Hysteresis ardışık kova aktif kaldı. started_at =
//     koşunun ilk kovası — mutlak kova, iki yazıcıda aynı. Pencere kenarında
//     zaten aktif koşu SATIR AÇMAZ; yalnız aynı revizyonun mevcut satırına
//     durum taşır. Böylece kayan pencere olay üretemez.
//   • BİLİNEN revizyonun dönüşü olay DEĞİLDİR: revizyon girişten
//     ≥ ExitHysteresis kova önce görülmüşse (6 g ufku ya da tablo satırı) ve
//     yokluğunda BAŞKA revizyon aktif olmadıysa (gece sessizliği, ingest
//     kesintisi, scale-to-zero) → satırı varsa taşınır, yoksa hiçbir şey
//     yazılmaz. Yokluğunda başkası aktif olduysa yeni giriş olayıdır (prev =
//     o revizyon, "geri dönüş" notu). Yeni (hiç görülmemiş) revizyon giriştir.
//   • VARLIK ≠ ETKİNLİK: giriş ve tamamlanma EŞİK üstü etkinlikle karar
//     verilir; "çekildi" ve "yokluk" ise HERHANGİ BİR span'in olmamasıdır —
//     eşik altına inen ama span üreten revizyon (canary çukuru, sağlık
//     kontrolü) çekilmiş SAYILMAZ, koşusu sürer, yeni giriş doğurmaz.
//   • ÇIKIŞ histerezisi ayrı (ExitHysteresis, 6 kova = 30 dk): kurulmuş
//     koşuyu daha kısa span'siz çukur kesmez; "çekildi" = ≥ EH kova hiç span yok.
//   • DEVRALMA testi: revizyonun yokluğunda "başkası aktifti" demek için o
//     revizyonun eşik üstü VE yokluk öncesi tabanın (revizyonun son aktif
//     kovasındaki span) en az yarısı kadar trafik taşımış olması gerekir —
//     %8'lik düz canary, yerleşik revizyonun 35 dk dalmasını rollout yapamaz.
//   • Küme VERİ BAŞLANGICI (Input.DataStart): MV geçmişi pencere içinde
//     başlıyorsa (ilk etkinleştirme, yeni küme) başlangıçtan < EH kova sonra
//     başlayan koşular için yokluk gözlenmemiştir → satır yok.
//   • Koşu başı geriye/ileri kayarsa (geç span, eşik geçişi) yazılmış satır
//     TAŞINIR: started_at asla değişmez.
//   • prev_revision: girişten önceki EN YAKIN öteki-aktif kovadaki en yoğun
//     revizyon. Eşik-altı öncü kovalar koşunun başına dahil (kısmi ilk kova,
//     blue/green preview, uzun canary ısınması; yokluk öncünün öncesinden
//     sayılır); öncü > Hysteresis ve prev yoksa rampa → giriş değil.
//   • DURUM (satırın kendi revizyonu R, önceki P):
//       completed   R aktif, ötekilerin hepsi ≥ EH kovadır yok.
//                   traffic_confirmed_at = son öteki kovasının bitişi.
//                   Overlap > OverlapMax → not.
//       rolled_back R çekildi ve çekilişten sonra ilk aktif görülen öteki
//                   P → BU rollout geri alındı (olay çekilen revizyonun
//                   satırında; P'nin yeni girişi ayrı, normal olaydır).
//       superseded  R çekildi, ilk aktif görülen öteki P değil → devralındı;
//                   aynı revizyonun daha yeni satırı da eskisini kapatır.
//       in_progress aksi hâlde; R çekildi ama kimse aktif değilse not düşer.
//     Terminal durumlar ve completed satırlar DONUK.
//   • Zayıf sinyal ("son kovada eşik altında kaldı") STATUS YAZMAZ (§11),
//     yalnız not (Config.WeakSignal kapatır); geçici notlar (zayıf sinyal,
//     durağan durum, trafik yok) her tikte yeniden değerlendirilir; stalled
//     KSM'nin işi (Faz 5) — Faz 5'te isOpen'a girer, bugün terminal sayılır.
// Saf: I/O yok, saat çağıranın (now), girdiler DEĞİŞTİRİLMEZ. reconcile_test.go pinler.

// Activity — MV'den okunan bir (cluster, ns, workload, revision, kova) satırı.
type Activity struct {
	ClusterID string // Remote Cluster EffectiveID (çağıran eşler; eşlenmeyen düşürülür ve sayılır)
	Namespace string
	Workload  string
	Kind      string
	Revision  string
	Bucket    time.Time // KARAR kovasının başı; çağıran 1 dk satırlarını hizalar (AlignBucket)
	Spans     int64
	FirstSeen time.Time
	LastSeen  time.Time
	Image     string
	ImageTag  string
}

// Rollout — workload_rollouts satırı (kimlik = ClusterID, Namespace, Workload,
// Revision, StartedAt). Yalnız TABLODAKİ alanlar: değişiklik tespiti
// (rolloutEqual) bunlarla yapılır. SpanCount = koşu boyunca görülen en büyük
// karar-penceresi toplamı (kayan pencere; olayın mutlak span sayısı değil).
type Rollout struct {
	ClusterID          string
	Namespace          string
	Workload           string
	Kind               string
	Revision           string
	StartedAt          time.Time
	Status             string
	PrevRevision       string
	Image              string
	ImageTag           string
	PrevImage          string
	PrevImageTag       string
	FirstSpanAt        time.Time
	TrafficConfirmedAt time.Time
	KSMStartedAt       time.Time
	PodsReadyAt        time.Time
	KSMNotReadySince   time.Time
	CompletedAt        time.Time
	DetectedBy         string
	SpanCount          int64
	Note               string
}

const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusRolledBack = "rolled_back"
	StatusStalled    = "stalled"
	// StatusSuperseded — v0.10.199: çekilen revizyonun satırı terminal (devralındı).
	StatusSuperseded = "superseded"
)

// Config — ayarlanabilir eşikler (system_settings["rollouts"]).
type Config struct {
	Bucket         time.Duration // karar kovası (5 dk)
	Threshold      int64         // kovada aktif sayılmak için span (10)
	Hysteresis     int           // GİRİŞ: ardışık kova (2)
	ExitHysteresis int           // ÇIKIŞ: "çekildi" için ardışık inaktif kova (6 = 30 dk)
	OverlapMax     time.Duration // bu süreden uzun çakışma → çok-revizyonlu notu (30 dk)
	WeakSignal     bool          // zayıf sinyal notu (varsayılan açık)
	StalledMin     time.Duration // KSM: ready < istenen bu süreden uzun → stalled (0 = kapalı)
}

func DefaultConfig() Config {
	return Config{Bucket: 5 * time.Minute, Threshold: 10, Hysteresis: 2, ExitHysteresis: 6, OverlapMax: 30 * time.Minute, WeakSignal: true}
}

func (c Config) normalized() Config {
	if c.Bucket <= 0 {
		c = DefaultConfig()
	}
	if c.Hysteresis < 1 {
		c.Hysteresis = 1
	}
	if c.ExitHysteresis < c.Hysteresis {
		c.ExitHysteresis = c.Hysteresis
	}
	return c
}

// Input — Reconcile girdisi.
type Input struct {
	Now time.Time
	// WindowStart — etkinlik sorgusunun GERÇEK başı (align(now − lookback));
	// sıfırsa anahtarın ilk verili kovası (test kolaylığı).
	WindowStart time.Time
	Prev        []Rollout
	Acts        []Activity
	// FirstSeen — 6 g ufkunda (MV TTL − 1) revizyonun ilk görüldüğü kova (Key → revision →
	// kova); pencere dışı geçmiş. nil = ufuk yok (yalnız tablo satırları).
	FirstSeen map[Key]map[string]time.Time
	// DataStart — kümenin (ClusterID) MV geçmişinin ilk kovası (ufuk içinde).
	DataStart map[string]time.Time
	// Truncated — etkinlik okuması tavana takıldı: etkinliği görünmeyen iş
	// yükleri için "yokluk" kanıt DEĞİLDİR (bayat satır notu atlanır).
	Truncated bool
	// KSM — Faz 5 (v0.10.212): Thanos/kube-state-metrics RS anlık görüntüsü
	// (Key → revizyon(RS adı) → KSMRev). nil/eksik anahtar = aile yok →
	// spans tek kaynak, stalled üretilmez (audit §7 kabulü).
	KSM map[Key]map[string]KSMRev
}

// AlignBucket — EPOCH hizalı kova başı: CH `toStartOfInterval` ile aynı ızgara.
// Go `Truncate` yıl-1'e göre hizalar; 1/5/10/15/30 dk dışındaki kovalarda iki
// ızgara ayrışır ve aynı MV satırı iki farklı karar kovasına düşerdi.
func AlignBucket(t time.Time, d time.Duration) time.Time {
	sec := int64(d / time.Second)
	if sec <= 0 {
		return t
	}
	u := t.Unix()
	return time.Unix(u-((u%sec)+sec)%sec, 0).UTC()
}

// Key — iş yükü kimliği.
type Key struct{ ClusterID, Namespace, Workload string }

func keyOf(a Activity) Key { return Key{a.ClusterID, a.Namespace, a.Workload} }

// revBucket — bir revizyonun bir kovadaki toplamı.
type revBucket struct {
	spans     int64
	firstSeen time.Time
	lastSeen  time.Time
	image     string
	imageTag  string
	kind      string
}

// Reconcile — SAF. Döner: değişen/yeni satırlar (upsert listesi).
func Reconcile(cfg Config, in Input) []Rollout {
	cfg = cfg.normalized()
	lastFull := AlignBucket(in.Now, cfg.Bucket).Add(-cfg.Bucket) // son tamamlanmış kova
	byKey := map[Key]map[time.Time]map[string]*revBucket{}
	for _, a := range in.Acts {
		if a.ClusterID == "" || a.Workload == "" || a.Revision == "" {
			continue
		}
		b := AlignBucket(a.Bucket, cfg.Bucket)
		if b.After(lastFull) {
			continue // yarım kova: karar dışı
		}
		k := keyOf(a)
		if byKey[k] == nil {
			byKey[k] = map[time.Time]map[string]*revBucket{}
		}
		if byKey[k][b] == nil {
			byKey[k][b] = map[string]*revBucket{}
		}
		rb := byKey[k][b][a.Revision]
		if rb == nil {
			rb = &revBucket{firstSeen: a.FirstSeen, lastSeen: a.LastSeen}
			byKey[k][b][a.Revision] = rb
		}
		rb.spans += a.Spans
		if !a.FirstSeen.IsZero() && (rb.firstSeen.IsZero() || a.FirstSeen.Before(rb.firstSeen)) {
			rb.firstSeen = a.FirstSeen
		}
		if a.LastSeen.After(rb.lastSeen) {
			rb.lastSeen = a.LastSeen
		}
		if a.Image != "" {
			rb.image, rb.imageTag = a.Image, a.ImageTag
		}
		if a.Kind != "" {
			rb.kind = a.Kind
		}
	}
	prevBy := map[Key]map[string][]Rollout{}
	for _, r := range in.Prev {
		k := Key{r.ClusterID, r.Namespace, r.Workload}
		if prevBy[k] == nil {
			prevBy[k] = map[string][]Rollout{}
		}
		prevBy[k][r.Revision] = append(prevBy[k][r.Revision], r)
	}
	for _, m := range prevBy {
		for rev := range m {
			rows := m[rev]
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].StartedAt.Before(rows[j].StartedAt) })
			m[rev] = rows
		}
	}
	var out []Rollout
	keys := make([]Key, 0, len(byKey)+len(prevBy))
	seenKey := map[Key]bool{}
	for k := range byKey {
		keys = append(keys, k)
		seenKey[k] = true
	}
	if !in.Truncated {
		for k := range prevBy {
			if !seenKey[k] {
				keys = append(keys, k) // etkinliği hiç olmayan iş yükü: yalnız bayat açık satır notu
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.ClusterID+"/"+a.Namespace+"/"+a.Workload < b.ClusterID+"/"+b.Namespace+"/"+b.Workload
	})
	ws := time.Time{}
	if !in.WindowStart.IsZero() {
		ws = AlignBucket(in.WindowStart, cfg.Bucket)
	}
	for _, k := range keys {
		out = append(out, reconcileKey(cfg, lastFull, ws, k, byKey[k], prevBy[k], in.FirstSeen[k], in.DataStart[k.ClusterID], in.Truncated, in.KSM[k])...)
	}
	// Kalıcı satırla birebir aynı olan çıktı yazılmaz (aynı revizyonun ikinci
	// koşusu çağrı-içi kopyaya karşı diff alıp bayt-aynı satırı yeniden
	// basıyordu → her tik upsert + tail olayı).
	persisted := map[string]Rollout{}
	for _, r := range in.Prev {
		persisted[r.ClusterID+"/"+r.Namespace+"/"+r.Workload+"/"+r.Revision+"@"+r.StartedAt.UTC().Format(time.RFC3339Nano)] = r
	}
	final := out[:0]
	for _, r := range out {
		if p, ok := persisted[r.ClusterID+"/"+r.Namespace+"/"+r.Workload+"/"+r.Revision+"@"+r.StartedAt.UTC().Format(time.RFC3339Nano)]; ok && rolloutEqual(p, r) {
			continue
		}
		final = append(final, r)
	}
	return final
}

// run — bir revizyonun ardışık koşusu: aktif (eşik üstü) kovalar + aralardaki
// canlı (eşik altı, span var) kovalar; span'siz çukur < ExitHysteresis ise köprülenir.
type run struct {
	start    time.Time // ilk AKTİF kovanın başı
	end      time.Time // son AKTİF kovanın başı
	endAlive time.Time // son CANLI (span var) kovanın başı (≥ end)
	n        int       // aktif kova sayısı
}

// runsOf — kovalar (artan) → koşular. Koşu yalnız aktif kovada başlar; canlı
// (eşik altı) kova koşuyu sürdürür ama saymaz; kurulmuş (n ≥ Hysteresis)
// koşuyu < ExitHysteresis kovalık span'siz çukur kesmez; kurulmamış
// parıltılar köprülenmez.
func runsOf(cfg Config, buckets []time.Time, spansAt func(b time.Time) int64) []run {
	bridge := time.Duration(cfg.ExitHysteresis) * cfg.Bucket // b − endAlive ≤ EH·B ⇔ çukur ≤ EH−1 kova
	var runs []run
	var cur *run
	for _, b := range buckets {
		sp := spansAt(b)
		if sp <= 0 {
			continue
		}
		active := sp >= cfg.Threshold
		if cur != nil && (b.Sub(cur.endAlive) <= cfg.Bucket || (cur.n >= cfg.Hysteresis && b.Sub(cur.endAlive) <= bridge)) {
			cur.endAlive = b
			if active {
				cur.end = b
				cur.n++
			}
			continue
		}
		if !active {
			continue // koşu aktif kovada başlar (öncü kovalar entryStart'ta katlanır)
		}
		runs = append(runs, run{start: b, end: b, endAlive: b, n: 1})
		cur = &runs[len(runs)-1]
	}
	return runs
}

// stripNote — geçici bir notu (her tikte yeniden değerlendirilen) çıkarır.
func stripNote(cur, note string) string {
	if cur == note {
		return ""
	}
	if i := indexOf(cur, "; "+note); i >= 0 {
		return cur[:i] + cur[i+len("; "+note):]
	}
	if i := indexOf(cur, note+"; "); i == 0 {
		return cur[len(note)+2:]
	}
	return cur
}

const (
	noteNoTraffic   = "revizyon çekildi, iş yükü trafik üretmiyor"
	noteWeakSignal  = "yeni revizyon son kovada eşik altında kaldı (zayıf sinyal; stalled kararı KSM'den)"
	noteSteadyState = "çok-revizyonlu durağan durum: eski revizyon hâlâ trafik alıyor"
	// v0.10.206 (operatör bildirimi): completed kararı ≥EH kova gözlenmiş
	// sessizlik ister; bekleyiş satırda okunmuyordu ve "bitti ama sürüyor
	// yazıyor" algısı üretiyordu. Geçici not — karar düşünce silinir.
	notePendingExit = "eski revizyonun sessizliği gözleniyor — tamamlandı kararı çıkış histerezisini bekliyor"
	// v0.10.212 — Faz 5: yalnız KSM kanıtıyla (ready < istenen ≥ StalledMin).
	// Span sessizliği stalled ÜRETMEZ (audit madde 7: üç kez yaşandı).
	noteStalled = "K8s: hazır replika istenenin altında (KSM)"
)

// ksmFreshGrace — SPAN KANITI YOKKEN kullanılan tek kanıt: RS'in taze
// yaratılmış olması. Pencere "trafik RS yaratılmasından ne kadar geç
// gelebilir" sorusunu yanıtlar — deploy hızını değil İŞ TAKVİMİNİ ölçer:
// sabah 07:00 deploy edilen gecelik import'un ilk trafiği 23:50'de gelir.
// v0.10.213'teki 6 sa bu sınıfı kesiyordu (inceleme bulgusu); 24 sa gerçek
// gecikmeli deploy'ları kurtarır ve bootstrap sahteliğini (RS yaşı gün/hafta
// mertebesinde) kesmeye devam eder.
//
// KALAN SINIR (bilinçli): cadence'i hem 6 g FirstSeen ufkundan hem bu
// pencereden uzun olan iş yükleri (aylık mutabakat) kanıtsız kalır ve satır
// almaz — span'lerden "ilk gözlem" ile "deploy" ayırt EDİLEMEZ.
const ksmFreshGrace = 24 * time.Hour

// isOpen — v0.10.212: stalled da AÇIK bir durumdur (Faz 5) — span tarafı
// çekilme/devralma kararlarını stalled satır üstünde de işletir.
func isOpen(r Rollout) bool {
	return r.CompletedAt.IsZero() && (r.Status == StatusInProgress || r.Status == StatusStalled)
}

func reconcileKey(cfg Config, lastFull, ws time.Time, k Key, byBucket map[time.Time]map[string]*revBucket, prevRows map[string][]Rollout, firstSeenEver map[string]time.Time, dataStart time.Time, truncated bool, ksm map[string]KSMRev) []Rollout {
	B := cfg.Bucket
	exitSpan := time.Duration(cfg.ExitHysteresis) * B
	buckets := make([]time.Time, 0, len(byBucket))
	for b := range byBucket {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Before(buckets[j]) })
	var out []Rollout
	outIdx := map[string]int{}
	emit := func(row Rollout) {
		id := row.Revision + "@" + row.StartedAt.UTC().Format(time.RFC3339Nano)
		if i, ok := outIdx[id]; ok {
			out[i] = row
			return
		}
		outIdx[id] = len(out)
		out = append(out, row)
	}
	inCall := map[string][]Rollout{}
	// known — tablo satırları + bu çağrıda üretilenler; aynı started_at'te
	// bu çağrının (taze) kopyası kazanır (bayat tablo kopyası kararı ezmesin).
	known := func(rev string) []Rollout {
		rows := append(append([]Rollout{}, prevRows[rev]...), inCall[rev]...)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].StartedAt.Before(rows[j].StartedAt) })
		dedup := rows[:0]
		for _, r := range rows {
			if n := len(dedup); n > 0 && dedup[n-1].StartedAt.Equal(r.StartedAt) {
				dedup[n-1] = r
				continue
			}
			dedup = append(dedup, r)
		}
		return dedup
	}
	// Etkinliği hiç olmayan iş yükü: açık satırlara yalnız not (kimse aktif değil).
	if len(buckets) == 0 {
		revs := make([]string, 0, len(prevRows))
		for rev := range prevRows {
			revs = append(revs, rev)
		}
		sort.Strings(revs)
		for _, rev := range revs {
			rows := prevRows[rev]
			last := rows[len(rows)-1]
			if !isOpen(last) {
				continue
			}
			row := last
			row.Note = appendNote(row.Note, noteNoTraffic)
			if !rolloutEqual(last, row) {
				emit(row)
			}
		}
		return out
	}
	// Küme veri başlangıcı pencere içindeyse (ilk etkinleştirme / yeni küme):
	// bundan < EH kova sonra başlayan koşular için yokluk gözlenmemiştir.
	dataEdge := time.Time{}
	if !dataStart.IsZero() {
		dataEdge = AlignBucket(dataStart, B).Add(exitSpan)
	}
	windowStart := buckets[0]
	if !ws.IsZero() && ws.Before(windowStart) {
		windowStart = ws
	}
	revs := map[string]bool{}
	for _, m := range byBucket {
		for r := range m {
			revs[r] = true
		}
	}
	spansAt := func(b time.Time, rev string) int64 {
		if rb := byBucket[b][rev]; rb != nil {
			return rb.spans
		}
		return 0
	}
	isActive := func(b time.Time, rev string) bool { return spansAt(b, rev) >= cfg.Threshold }
	isAlive := func(b time.Time, rev string) bool { return spansAt(b, rev) > 0 }
	runsBy := map[string][]run{}
	for rev := range revs {
		r := rev
		runsBy[rev] = runsOf(cfg, buckets, func(b time.Time) int64 { return spansAt(b, r) })
	}
	// othersActiveAt — kovada rev DIŞINDA aktif revizyon; en yoğunu (eşitlikte ad sırası)
	othersActiveAt := func(b time.Time, rev string) (string, bool) {
		var best string
		var bestSpans int64 = -1
		for r, rb := range byBucket[b] {
			if r == rev || rb.spans < cfg.Threshold {
				continue
			}
			if rb.spans > bestSpans || (rb.spans == bestSpans && r < best) {
				best, bestSpans = r, rb.spans
			}
		}
		return best, bestSpans >= 0
	}
	// prevAt — girişten önceki EN YAKIN öteki-aktif kova (pencere içinde)
	prevAt := func(e time.Time, rev string) (string, time.Time, bool) {
		for b := e.Add(-B); !b.Before(windowStart); b = b.Add(-B) {
			if r2, ok := othersActiveAt(b, rev); ok {
				return r2, b, true
			}
		}
		return "", time.Time{}, false
	}
	// takeoverWhileAbsent — [from, to) kovalarında rev CANLI DEĞİLKEN trafiği
	// DEVRALAN öteki (en yoğun): eşik üstü VE base'in (rev'in yokluk öncesi son
	// aktif kovasındaki span; bilinmiyorsa dönüş kovasındaki) en az yarısı.
	// Düz %8 canary bu testi geçemez → yerleşik revizyonun dalması olay değil.
	// coSilent — aday x, r'nin son aktif kovasında (lastActive) birlikte
	// aktifti (ya da o kova pencere dışında kaldı: gözlenmedi → birlikte
	// sayılır) ve b'den hemen önce ≥ EH ardışık canlı kova biriktirmediyse
	// ortak sessizlikten dönen bir kardeştir (ingest kesintisi, iş yükünün
	// ortak boşluğu): devralma DEĞİL. Süreye dayalı: 1 kovalık kaymada
	// değil, kardeş ≥ EH kova tek başına aktif kalana dek tutar.
	coSilent := func(x string, lastActive, from, b time.Time) bool {
		if !lastActive.IsZero() && !isActive(lastActive, x) {
			return false
		}
		run := 0
		for bb := b.Add(-B); !bb.Before(from) && run < cfg.ExitHysteresis && isAlive(bb, x); bb = bb.Add(-B) {
			run++
		}
		return run < cfg.ExitHysteresis
	}
	takeoverWhileAbsent := func(rev string, from, to time.Time, base int64) string {
		best := ""
		var bestSpans int64 = -1
		need := cfg.Threshold
		if base/2 > need {
			need = base / 2
		}
		var lastActive time.Time
		for _, b := range buckets {
			if !b.Before(to) {
				break
			}
			if !b.Before(from) && isActive(b, rev) {
				lastActive = b
			}
		}
		for _, b := range buckets {
			if b.Before(from) || !b.Before(to) || isAlive(b, rev) {
				continue
			}
			for r2, rb := range byBucket[b] {
				if r2 == rev || rb.spans < need || coSilent(r2, lastActive, from, b) {
					continue
				}
				if rb.spans > bestSpans || (rb.spans == bestSpans && r2 < best) {
					best, bestSpans = r2, rb.spans
				}
			}
		}
		return best
	}
	// baseBefore — rev'in yokluk öncesi trafik SEVİYESİ: son aktif kovada biten
	// EH kovalık dilimin en yüksek span'i (tek kova rampa-aşağıda çöker →
	// düz canary "devralmış" görünürdü). Dilim pencere başına sığmıyorsa
	// seviye gözlenmemiştir → 0 (çağıran dönüş kovasına düşer).
	baseBefore := func(rev string, e time.Time) int64 {
		var last time.Time
		for b := e.Add(-B); !b.Before(windowStart); b = b.Add(-B) {
			if spansAt(b, rev) >= cfg.Threshold {
				last = b
				break
			}
		}
		if last.IsZero() {
			return 0
		}
		var best int64
		n := 0
		for b := last; n < cfg.ExitHysteresis; b = b.Add(-B) {
			if b.Before(windowStart) {
				return 0 // seviye gözlenmedi
			}
			if sp := spansAt(b, rev); sp > best {
				best = sp
			}
			n++
		}
		return best
	}
	// firstSeen — revizyonun bilinen en eski izi: 6 g ufku, tablo satırı, pencere
	firstSeen := func(rev string) time.Time {
		t := firstSeenEver[rev]
		for _, r := range prevRows[rev] {
			if t.IsZero() || r.StartedAt.Before(t) {
				t = r.StartedAt
			}
		}
		for _, b := range buckets {
			if byBucket[b][rev] != nil && (t.IsZero() || b.Before(t)) {
				t = b
				break
			}
		}
		return t
	}
	// decideWithdrawn — R (satır row) çekildi: çekilişten sonra İLK aktif görülen öteki kim?
	decideWithdrawn := func(row *Rollout, endedAt, from, to time.Time) {
		who := ""
		deferred := ""
		var deferredAt time.Time
		for b := from; !b.After(to); b = b.Add(B) {
			if isAlive(b, row.Revision) {
				break // revizyonun kendi dönüşü (span var): ayrı koşu ya da devam
			}
			if !deferredAt.IsZero() {
				// Ortak sessizlikten dönen aday: R dönmeden ≥ EH kova AKTİF KALIRSA
				// devralmıştır (gerçek rollback bir lookback ertelenmesin); susarsa
				// erteleme düşer ve bu kova yeniden değerlendirilir (gerçek halef
				// kaçmasın).
				switch {
				case !isActive(b, deferred):
					deferred, deferredAt = "", time.Time{}
				case b.Sub(deferredAt) >= time.Duration(cfg.ExitHysteresis)*B:
					who = deferred
				default:
					continue
				}
				if who != "" {
					break
				}
			}
			if x, ok := othersActiveAt(b, row.Revision); ok {
				// Ortak sessizlik: aday yokluktan önce birlikte aktifti ve kendisi de
				// ≥ EH kova sustuysa hemen karar verme — ertele.
				if coSilent(x, endedAt, from, b) {
					deferred, deferredAt = x, b
					continue
				}
				who = x
				break
			}
		}
		switch {
		case who == "":
			row.Note = appendNote(row.Note, noteNoTraffic)
		case who == row.PrevRevision:
			row.Status = StatusRolledBack
			row.CompletedAt = endedAt.Add(B)
			row.Note = appendNote(row.Note, "yeni revizyon çekildi, önceki revizyon ("+who+") trafiği sürdürdü (rollback)")
		default:
			row.Status = StatusSuperseded
			row.CompletedAt = endedAt.Add(B)
			row.Note = appendNote(row.Note, "yeni revizyon devraldı: "+who)
		}
	}
	revList := make([]string, 0, len(revs))
	for r := range revs {
		revList = append(revList, r)
	}
	sort.Strings(revList)
	touched := map[string]bool{}
	type revRun struct {
		rev string
		idx int
		rn  run
	}
	var order []revRun
	for _, rev := range revList {
		for i, rn := range runsBy[rev] {
			if rn.n >= cfg.Hysteresis {
				order = append(order, revRun{rev, i, rn})
			}
		}
	}
	// Koşular KRONOLOJİK işlenir: aynı çağrıdaki A→B→A'da A'nın ikinci girişi
	// B'nin satırını görmeli (geri dönüş notu).
	sort.SliceStable(order, func(i, j int) bool {
		if !order[i].rn.start.Equal(order[j].rn.start) {
			return order[i].rn.start.Before(order[j].rn.start)
		}
		return order[i].rev < order[j].rev
	})
	for _, rr := range order {
		rev, rn := rr.rev, rr.rn
		touched[rev] = true
		isLast := rr.idx == len(runsBy[rev])-1
		// Öncü kovalar: koşudan hemen önce eşik ALTI görülen ardışık kovalar
		// koşunun başına dahil (kısmi ilk kova, blue/green preview, uzun canary
		// ısınması); yokluk öncünün ÖNCESİNDEN sayılır.
		entryStart := rn.start
		lead := 0
		for bb := rn.start.Add(-B); !bb.Before(windowStart); bb = bb.Add(-B) {
			rb := byBucket[bb][rev]
			if rb == nil || rb.spans >= cfg.Threshold {
				break
			}
			entryStart = bb
			lead++
		}
		// Gözlenmiş yokluk: entryStart'tan geriye, pencere içinde, SPAN'SİZ kova sayısı
		gap := 0
		for bb := entryStart.Add(-B); !bb.Before(windowStart); bb = bb.Add(-B) {
			if isAlive(bb, rev) {
				break
			}
			gap++
		}
		base := baseBefore(rev, entryStart)
		if base == 0 {
			// seviye gözlenmedi (pencere kenarı): dönen koşunun KENDİ seviyesi (ilk
			// kova değil — ilk kova rampa olabilir, düz canary'ye eşik kapısı çökerdi)
			for _, bb := range buckets {
				if bb.Before(rn.start) || bb.After(rn.end) {
					continue
				}
				if sp := spansAt(bb, rev); sp > base {
					base = sp
				}
			}
		}
		rows := known(rev)
		var existing *Rollout
		var older []Rollout
		for i := range rows {
			switch {
			case rows[i].StartedAt.Equal(entryStart):
				existing = &rows[i]
			case rows[i].StartedAt.Before(entryStart):
				older = append(older, rows[i])
			case existing == nil && !rows[i].StartedAt.After(rn.end):
				existing = &rows[i] // koşu başı geriye kaydı: satır taşınır
			}
		}
		if existing == nil && len(older) > 0 {
			last := older[len(older)-1]
			carry := isOpen(last) || last.Status == StatusCompleted
			switch {
			case !carry:
			case gap < cfg.ExitHysteresis: // kenar: giriş kanıtı yok → aynı satır
				existing = &last
			case isOpen(last) && entryStart.Sub(last.StartedAt) <= exitSpan: // koşu başı ileri kaydı: satır taşınır
				existing = &last
			case takeoverWhileAbsent(rev, maxTime(last.StartedAt, windowStart), entryStart, base) == "": // yokluğunda kimse devralmadı
				existing = &last
			}
		}
		var row Rollout
		if existing != nil {
			row = *existing
			if !isOpen(row) {
				inCall[rev] = append(inCall[rev], row)
				continue // kapalı satır DONUK: toplam/imaj güncellenmez
			}
		} else {
			// GİRİŞ kararı: gözlenmiş yokluk ≥ EH (kenar kanıt değil; küme veri
			// başlangıcından < EH kova sonra da kanıt değil)
			if gap < cfg.ExitHysteresis || (!dataEdge.IsZero() && entryStart.Before(dataEdge)) {
				continue
			}
			p, pb, ok := prevAt(entryStart, rev)
			// Bilinen revizyonun dönüşü, yokluğunda kimse devralmadıysa olay değil.
			if fs := firstSeen(rev); !fs.IsZero() && fs.Before(entryStart.Add(-exitSpan)) &&
				takeoverWhileAbsent(rev, windowStart, entryStart, base) == "" {
				continue
			}
			if !ok && lead > cfg.Hysteresis {
				continue // rampa: giriş değil
			}
			// v0.10.213 (+215 düzeltmesi) — giriş bir OLAY İDDİASIDIR; kanıt
			// ister. Kanıtlar SIRALI DEĞİL ÖNCELİKLİDİR:
			//   1) SPAN TARAFINDA GÖZLENMİŞ GEÇİŞ — önceki revizyon girişte
			//      aktif (prevAt) ya da kardeş revizyon izi (pencere kovası /
			//      FirstSeen ufku / entryStart'tan ÖNCE başlamış tablo satırı).
			//      Bu en güçlü kanıttır ve KSM damgasıyla EZİLEMEZ.
			//   2) Span kanıtı yoksa tek kanıt RS'in TAZE yaratılmış olmasıdır
			//      (ksmFreshGrace); o da yoksa satır ÜRETİLMEZ — "ilk gözlem"
			//      sahte "tamamlandı" üretemez (2026-08-31 prod olayı).
			//
			// v0.10.213'te (2) bir VETO idi ve (1)'i eziyordu: Kubernetes
			// `rollout undo` MEVCUT RS'i yeniden ölçekler (created haftalar
			// önce) ve blue/green'de RS trafikten saatler önce doğar — yani
			// en kanıtlı geçişler yutuluyordu; üstelik eski satır "devraldı"
			// notuyla kapanıp yeni revizyona hiç satır yazılmıyordu (yarım
			// kayıt). Çok-mercekli inceleme bunu çalıştırarak kanıtladı.
			if !ok && !hasSiblingHistory(byBucket, prevRows, firstSeenEver, rev, entryStart) {
				kr, kok := ksm[rev]
				if !kok || kr.CreatedAt.IsZero() || kr.CreatedAt.Before(entryStart.Add(-ksmFreshGrace)) {
					continue
				}
			}
			row = Rollout{ClusterID: k.ClusterID, Namespace: k.Namespace, Workload: k.Workload, Revision: rev, StartedAt: entryStart, DetectedBy: "spans", Status: StatusInProgress, PrevRevision: p}
			if ok {
				if prb := byBucket[pb][p]; prb != nil {
					row.PrevImage, row.PrevImageTag = prb.image, prb.imageTag
				}
				for _, pr := range known(p) {
					if pr.PrevRevision == rev && pr.StartedAt.Before(entryStart) {
						row.Note = appendNote(row.Note, "önceki revizyona geri dönüş ("+p+" geri alındı)")
						break
					}
				}
			}
		}
		// toplamlar + imaj + kind (koşu boyunca; köprülenen çukur kovaları da sayılır)
		var spans int64
		firstSpan := row.FirstSpanAt
		for _, b := range buckets {
			if b.Before(entryStart) || b.After(rn.end) {
				continue
			}
			rb := byBucket[b][rev]
			if rb == nil {
				continue
			}
			spans += rb.spans
			if !rb.firstSeen.IsZero() && (firstSpan.IsZero() || rb.firstSeen.Before(firstSpan)) {
				firstSpan = rb.firstSeen
			}
			if rb.image != "" {
				row.Image, row.ImageTag = rb.image, rb.imageTag
			}
			if rb.kind != "" {
				row.Kind = rb.kind
			}
		}
		if spans > row.SpanCount {
			row.SpanCount = spans
		}
		row.FirstSpanAt = firstSpan
		if isOpen(row) {
			// geçici notlar: her tikte yeniden değerlendirilir
			row.Note = stripNote(row.Note, noteNoTraffic)
			row.Note = stripNote(row.Note, noteWeakSignal)
			row.Note = stripNote(row.Note, noteSteadyState)
			row.Note = stripNote(row.Note, notePendingExit)
			row.Note = stripNote(row.Note, noteStalled)
			if kr, ok := ksm[rev]; ok {
				applyKSM(cfg, lastFull.Add(B), &row, kr)
			}
			withdrawn := !isLast || lastFull.Sub(rn.endAlive) >= exitSpan
			if withdrawn {
				decideWithdrawn(&row, rn.endAlive, rn.endAlive.Add(B), lastFull)
			} else {
				entryStart := maxTime(row.StartedAt, windowStart)
				var lastOther time.Time
				for b := entryStart; !b.After(lastFull); b = b.Add(B) {
					if _, ok := othersActiveAt(b, rev); ok {
						lastOther = b
					}
				}
				quiet := int(lastFull.Sub(entryStart)/B) + 1
				if !lastOther.IsZero() {
					quiet = int(lastFull.Sub(lastOther) / B)
				}
				if quiet >= cfg.ExitHysteresis && isActive(lastFull, rev) {
					confirmed := entryStart.Add(B)
					if !lastOther.IsZero() {
						confirmed = lastOther.Add(B)
					}
					row.TrafficConfirmedAt = confirmed
					row.CompletedAt = confirmed
					row.Status = StatusCompleted
					if lastOther.IsZero() && row.StartedAt.Before(windowStart) {
						// önceki revizyonun çekilme anı pencere dışında (reconciler kesintisi):
						// damga üst sınırdır, pencere geometrisinden overlap ÜRETİLMEZ
						row.Note = appendNote(row.Note, "önceki revizyonun çekilme anı pencere dışında kaldı: doğrulama anı üst sınır")
					} else if overlap := confirmed.Sub(row.StartedAt); overlap > cfg.OverlapMax {
						row.Note = appendNote(row.Note, "çok-revizyonlu geçiş: "+overlap.Truncate(time.Minute).String()+" boyunca eski revizyonla birlikte (canary/blue-green)")
					}
				} else if !lastOther.IsZero() && lastFull.Sub(row.StartedAt) > cfg.OverlapMax && lastOther.Equal(lastFull) {
					row.Note = appendNote(row.Note, noteSteadyState)
				}
				// v0.10.206 — bekleyişi adlandır: eski revizyon sustu ama karar
				// için gözlem henüz EH kovaya ulaşmadı. İlk revizyonda (önceki
				// yok: lastOther boş VE PrevRevision boş) yazılmaz.
				if (!lastOther.IsZero() || row.PrevRevision != "") && quiet >= 1 && quiet < cfg.ExitHysteresis && isActive(lastFull, rev) {
					row.Note = appendNote(row.Note, notePendingExit)
				}
				if cfg.WeakSignal && !isActive(lastFull, rev) {
					if _, ok := othersActiveAt(lastFull, rev); ok {
						row.Note = appendNote(row.Note, noteWeakSignal)
					}
				}
			}
		}
		if row.Status == StatusStalled {
			row.Note = appendNote(row.Note, noteStalled)
		}
		if existing == nil || !rolloutEqual(*existing, row) {
			emit(row)
		}
		inCall[rev] = append(inCall[rev], row)
	}
	// Pencerede kurulmuş koşusu olmayan revizyonların AÇIK satırları: çekilmiştir.
	// Bitiş = pencere başından bir kova önce ya da son eşik-altı izi (veri
	// desteklemeyen damga UYDURULMAZ). Kesik okumada yokluk kanıt değil → atlanır.
	prevRevs := make([]string, 0, len(prevRows))
	for rev := range prevRows {
		if !truncated {
			prevRevs = append(prevRevs, rev)
		}
	}
	sort.Strings(prevRevs)
	for _, rev := range prevRevs {
		if touched[rev] {
			continue
		}
		rows := known(rev)
		last := rows[len(rows)-1]
		if !isOpen(last) {
			continue
		}
		row := last
		endedAt := windowStart.Add(-B)
		for _, bb := range buckets {
			if rb := byBucket[bb][rev]; rb != nil && rb.spans > 0 && bb.After(endedAt) {
				endedAt = bb
			}
		}
		if row.StartedAt.After(endedAt) {
			endedAt = row.StartedAt
		}
		if lastFull.Sub(endedAt) < exitSpan {
			inCall[rev] = append(inCall[rev], last)
			continue // hâlâ span üretiyor (eşik altı / tek kova): çekilmedi
		}
		row.Note = stripNote(stripNote(stripNote(stripNote(stripNote(row.Note, noteNoTraffic), noteWeakSignal), noteSteadyState), notePendingExit), noteStalled)
		decideWithdrawn(&row, endedAt, endedAt.Add(B), lastFull)
		if !rolloutEqual(last, row) {
			emit(row)
		}
		inCall[rev] = append(inCall[rev], row)
	}
	// Aynı revizyonun daha yeni satırı varsa eski açık satır devralınmıştır.
	allRevs := map[string]bool{}
	for rev := range prevRows {
		allRevs[rev] = true
	}
	for rev := range inCall {
		allRevs[rev] = true
	}
	revAll := make([]string, 0, len(allRevs))
	for rev := range allRevs {
		revAll = append(revAll, rev)
	}
	sort.Strings(revAll)
	for _, rev := range revAll {
		rows := known(rev)
		if len(rows) < 2 {
			continue
		}
		newest := rows[len(rows)-1].StartedAt
		for _, r := range rows {
			if !r.StartedAt.Before(newest) || !isOpen(r) {
				continue
			}
			r.Status = StatusSuperseded
			r.CompletedAt = newest
			r.Note = appendNote(r.Note, "aynı revizyonun daha yeni koşusu devraldı")
			emit(r)
		}
	}
	return out
}

// hasSiblingHistory — v0.10.213: iş yükünün BAŞKA bir revizyonu iz bırakmış
// mı — pencere içinde (entryStart öncesi kovada span), FirstSeen ufkunda ya
// da tabloda (önceki rollout satırı). İz = bu iş yükü zaten izleniyordu,
// yeni revizyon gerçek bir geçiş; izsizlik = bootstrap/ilk gözlem.
func hasSiblingHistory(byBucket map[time.Time]map[string]*revBucket, prevRows map[string][]Rollout, firstSeenEver map[string]time.Time, rev string, entryStart time.Time) bool {
	for b, m := range byBucket {
		if !b.Before(entryStart) {
			continue
		}
		for r2, rb := range m {
			if r2 != rev && rb != nil && rb.spans > 0 {
				return true
			}
		}
	}
	for r2, fs := range firstSeenEver {
		if r2 != rev && !fs.IsZero() && fs.Before(entryStart) {
			return true
		}
	}
	// v0.10.215 — tablo ayağı ZAMAN SINIRLI olmalı: entryStart'tan SONRA
	// yazılmış bir kardeş satır "iz" sayılırsa, bir tik önce kanıtsız diye
	// reddedilen giriş bir sonraki tikte GERİYE DÖNÜK meşrulaşır (sahte satır
	// kendini besler — inceleme bulgusu, kopyada koşturularak kanıtlandı).
	for r2, rows := range prevRows {
		if r2 == rev {
			continue
		}
		for _, pr := range rows {
			if pr.StartedAt.Before(entryStart) {
				return true
			}
		}
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func appendNote(cur, add string) string {
	if cur == "" {
		return add
	}
	if containsNote(cur, add) {
		return cur
	}
	return cur + "; " + add
}

func containsNote(cur, add string) bool {
	return len(cur) >= len(add) && (cur == add || indexOf(cur, add) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// rolloutEqual — yalnız TABLODAKİ alanlar (whole-row replace; KSM alanları span
// yolunda değişmez ama taşınır).
func rolloutEqual(a, b Rollout) bool {
	return a.Status == b.Status && a.SpanCount == b.SpanCount && a.Note == b.Note &&
		a.Image == b.Image && a.ImageTag == b.ImageTag && a.PrevRevision == b.PrevRevision &&
		a.PrevImage == b.PrevImage && a.PrevImageTag == b.PrevImageTag && a.Kind == b.Kind &&
		a.DetectedBy == b.DetectedBy &&
		a.CompletedAt.Equal(b.CompletedAt) && a.TrafficConfirmedAt.Equal(b.TrafficConfirmedAt) &&
		a.FirstSpanAt.Equal(b.FirstSpanAt) && a.KSMStartedAt.Equal(b.KSMStartedAt) &&
		a.PodsReadyAt.Equal(b.PodsReadyAt) && a.KSMNotReadySince.Equal(b.KSMNotReadySince)
}
