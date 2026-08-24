package evaluator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// db_capacity.go — capacity / saturation alerting off DB-receiver gauges
// (feature #5). The /databases dashboards already colour their tiles when a
// tablespace / session / connection pool nears its cap, but that tone was
// cosmetic — nothing opened a Problem, so nobody got PAGED. This pass reads
// the same receiver saturation gauges on the existing leader-locked
// evaluator tick and opens/resolves Problems against them, deduped per
// (instance, check) exactly like every other Problem.
//
// Why the evaluator (not anomaly): the evaluator OWNS auto-created
// threshold Problems — leader lock, FindOpenProblem/UpsertProblem dedup,
// the ReplacingMergeTree(version) problems table, notify fan-out,
// incident-attach. internal/anomaly/ is unsupervised span/log shape
// detection (Drain, significant_text, ratio recorder); a fixed-threshold
// gauge breach is not that. So this rides evaluateAll like evaluateSLOs.
//
// Dedup model — match the existing callers EXACTLY:
//   • rule_id  = "db-capacity:<check>"        (stable per check)
//   • service  = the receiver instance         (so FindOpenProblem(rule,svc)
//                                                returns the one open row)
//   • id       = "db-capacity:<check>:<instance>[:<subkey>]" (stable, so the
//                ReplacingMergeTree collapses re-fires onto the same row)
// One open Problem per (instance, check[, subkey]); refreshed not
// duplicated while breached; auto-resolved when back under threshold.

// Capacity thresholds. Crit at 90%, warn at 85% — the bar Datadog/Dynatrace
// use for "disk/tablespace/connection-pool nearly full". The Redis eviction
// check is a raw rate (no cap), so any positive eviction rate is the signal.
const (
	capacityCritPct = 90.0
	capacityWarnPct = 85.0
	// capacityHysteresisPct (v0.8.320) — an open problem only resolves
	// below warn−this, so a boundary-parked gauge can't churn open/close.
	capacityHysteresisPct = 2.0
)

// ── ETA tahmini (v0.9.1065, Faz 2.4 / G4) ──────────────────────────────
// Sabit %90 eşiği gece 3'te patlar; Davis'in en görünür yeteneği "X saat
// sonra dolacak". Pencere-içi doğrusal regresyon (eğim + R²) mevcut
// metric_points serisi üstünde — yeni tablo yok. Muhafazakâr kapılar:
// zayıf uyum (R²<0.6) ya da uzak ufuk (>24h) tahmin ÜRETMEZ; erken-açma
// yalnız %70+ doluluk ve ≤6h ufukta, warning olarak.
const (
	capacityEtaWindow    = 2 * time.Hour
	capacityEtaMinPoints = 6
	capacityEtaMinSpanS  = 30 * 60
	capacityEtaMinR2     = 0.6
	capacityEtaMaxHours  = 24.0
	capacityEtaMinPct    = 70.0
	capacityEtaOpenHours = 6.0
)

// capacityETA — saf regresyon çekirdeği (tablo-testli). points zaman
// artan sıralı 5dk-ortalama doluluk; limit pozitif. ok=false: tahmin
// yok (kısa seri, düşmeyen/duran eğim, zayıf uyum ya da >24h ufuk).
// ETA, regresyon doğrusunun SON noktadaki değerinden limit'e kalan
// süredir — cari gürültülü örnek yerine uydurulmuş değer.
func capacityETA(points []chstore.CapacityTrendPoint, limit float64) (etaHours, r2 float64, ok bool) {
	n := len(points)
	if n < capacityEtaMinPoints || limit <= 0 {
		return 0, 0, false
	}
	if points[n-1].TSec-points[0].TSec < capacityEtaMinSpanS {
		return 0, 0, false
	}
	// Doğrusal regresyon (x=saniye, taşmaya karşı ilk noktaya göre).
	x0 := points[0].TSec
	var sx, sy, sxx, sxy float64
	for _, p := range points {
		x := float64(p.TSec - x0)
		sx += x
		sy += p.Usage
		sxx += x * x
		sxy += x * p.Usage
	}
	fn := float64(n)
	den := fn*sxx - sx*sx
	if den == 0 {
		return 0, 0, false
	}
	slope := (fn*sxy - sx*sy) / den // birim/saniye
	if slope <= 0 {
		return 0, 0, false
	}
	intercept := (sy - slope*sx) / fn
	// R² = 1 − SSres/SStot.
	meanY := sy / fn
	var ssRes, ssTot float64
	for _, p := range points {
		x := float64(p.TSec - x0)
		fit := intercept + slope*x
		ssRes += (p.Usage - fit) * (p.Usage - fit)
		ssTot += (p.Usage - meanY) * (p.Usage - meanY)
	}
	if ssTot == 0 {
		return 0, 0, false // düz seri — eğim iddiası kurulamaz
	}
	r2 = 1 - ssRes/ssTot
	if r2 < capacityEtaMinR2 {
		return 0, r2, false
	}
	lastFit := intercept + slope*float64(points[n-1].TSec-x0)
	if lastFit >= limit {
		return 0, r2, false // zaten limitte — eşik dalı konuşur
	}
	etaHours = (limit - lastFit) / slope / 3600
	if etaHours > capacityEtaMaxHours {
		return 0, r2, false
	}
	return etaHours, r2, true
}

// capacityPredictiveOpen — erken-açma kapısı (saf): eşik dalı açmadıysa
// bile %70+ doluluk ve ≤6h ufukta warning açılır.
func capacityPredictiveOpen(pct, etaHours float64) bool {
	return pct >= capacityEtaMinPct && etaHours <= capacityEtaOpenHours
}

// capacityCheck describes one saturation check: how to read it and how to
// label the resulting Problem. read() returns the per-instance samples;
// the evaluator turns each into open/resolve. rate=true means the sample's
// Usage is a per-second rate with no Limit (>0 breaches at crit).
type capacityCheck struct {
	id    string // rule_id suffix → "db-capacity:<id>"
	label string // human label in the reason string ("tablespace", "sessions")
	dbsys string // "ORACLE" / "POSTGRES" / … for the reason string
	rate  bool   // raw-rate check (no usage/limit pair)
	// probe gates a DEFENSIVE check: the check reads only when this
	// metric is currently being published (so an install without that
	// receiver never sees spurious Problems). Empty = always run (the
	// Oracle checks — the primary banking-DB integration).
	probe string
	read  func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error)
	// trendMetric/trendAttr (v0.9.1065) — ETA tahmininin okuyacağı
	// doluluk gauge'u. Boş = tahmin yok (rate check'leri: eğimden dolma
	// süresi türetilemez).
	trendMetric string
	trendAttr   string
}

// capacityChecks is the full catalogue. The Oracle checks always run (the
// oracledb receiver is the primary banking-DB integration). The
// Postgres/MySQL/Redis checks are DEFENSIVE — they read only when their
// receiver is actually publishing (see evaluateDBCapacity's metricExists
// gate), so an install without that receiver never sees spurious Problems.
var capacityChecks = []capacityCheck{
	// Oracle tablespace usage — dimensioned by tablespace_name. The #1
	// reason an Oracle instance falls over.
	{id: "oracle-tablespace", label: "tablespace", dbsys: "ORACLE",
		trendMetric: "oracledb.tablespace_size.usage", trendAttr: "tablespace_name",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.DimensionedUsageLimit(ctx,
				"oracledb.tablespace_size.usage", "oracledb.tablespace_size.limit", "tablespace_name")
		}},
	// Oracle sessions usage/limit.
	{id: "oracle-sessions", label: "sessions", dbsys: "ORACLE",
		trendMetric: "oracledb.sessions.usage",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "oracledb.sessions.usage", "oracledb.sessions.limit")
		}},
	// Oracle processes usage/limit.
	{id: "oracle-processes", label: "processes", dbsys: "ORACLE",
		trendMetric: "oracledb.processes.usage",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "oracledb.processes.usage", "oracledb.processes.limit")
		}},

	// ── Defensive (only fire when the receiver is present) ──────────────
	// Postgres backends / max_connections.
	{id: "postgres-connections", label: "connections", dbsys: "POSTGRES", probe: "postgresql.backends",
		trendMetric: "postgresql.backends",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "postgresql.backends", "postgresql.connection.max")
		}},
	// MySQL connections / max_used_connections.
	{id: "mysql-connections", label: "connections", dbsys: "MYSQL", probe: "mysql.connection.count",
		trendMetric: "mysql.connection.count",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "mysql.connection.count", "mysql.max_used_connections")
		}},
	// Redis eviction rate — raw rate, breaches when > 0 (maxmemory-policy
	// is actively evicting keys → memory pressure).
	{id: "redis-evictions", label: "key evictions", dbsys: "REDIS", rate: true, probe: "redis.keys.evicted",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.RateGauge(ctx, "redis.keys.evicted")
		}},
}

// capacityDecision is the PURE threshold core (CLAUDE.md #11 — unit-tested
// in db_capacity_test.go). Given a sample's usage/limit (or a raw rate for
// rate checks) it returns whether a Problem should be OPEN and at what
// severity, plus the percentage for the reason string.
//
//   - usage/limit pair: pct = usage/limit*100; crit ≥ 90, warn ≥ 85,
//     otherwise resolve. A non-positive limit yields (false, "", 0) — the
//     read layer already drops those, this is belt-and-suspenders.
//   - rate check (limit == 0 via the rate flag): any rate > 0 is critical
//     (active eviction); pct carries the rate itself for the reason.
//   - wasOpen (v0.8.320) — hysteresis: an already-open problem stays open
//     until pct drops below warn−2pp. Fire and clear at the same 85.0 had
//     a boundary-parked gauge open→notify→resolve→re-open (re-notifying)
//     every tick; this path has no ForSec/Cooldown kit, so the band IS the
//     anti-noise mechanism.
func capacityDecision(usage, limit float64, rate, wasOpen bool) (open bool, severity string, pct float64) {
	if rate {
		if usage > 0 {
			return true, "critical", usage
		}
		return false, "", usage
	}
	if limit <= 0 {
		return false, "", 0
	}
	pct = usage / limit * 100
	switch {
	case pct >= capacityCritPct:
		return true, "critical", pct
	case pct >= capacityWarnPct:
		return true, "warning", pct
	case wasOpen && pct >= capacityWarnPct-capacityHysteresisPct:
		// Inside the hysteresis band: don't resolve yet, don't fire fresh.
		return true, "warning", pct
	default:
		return false, "", pct
	}
}

// capacityReason builds the operator-facing reason string, e.g.
// "ORACLE tablespace SYSAUX at 92% on corebank-scan.prod" or
// "REDIS key evictions at 14.3/s on cache-01".
func capacityReason(c capacityCheck, instance, subkey string, pct float64) string {
	var b strings.Builder
	b.WriteString(c.dbsys)
	b.WriteString(" ")
	b.WriteString(c.label)
	if subkey != "" {
		b.WriteString(" ")
		b.WriteString(subkey)
	}
	if c.rate {
		fmt.Fprintf(&b, " at %.1f/s on %s", pct, instance)
	} else {
		fmt.Fprintf(&b, " at %.0f%% on %s", pct, instance)
	}
	return b.String()
}

// capacityReasonETA — capacityReason + tahmin eki (v0.9.1065). Tahmin
// yoksa çıktı bayt-bayt eski.
func capacityReasonETA(c capacityCheck, instance, subkey string, pct, etaHours float64, etaOK bool) string {
	r := capacityReason(c, instance, subkey, pct)
	if etaOK {
		r += fmt.Sprintf(" — projected full in ~%.1fh", etaHours)
	}
	return r
}

// capacityRuleID / capacityProblemID build the stable dedup keys. rule_id
// is per-check; the Problem id additionally carries instance + subkey so a
// re-fire on the next tick collapses onto the same ReplacingMergeTree row.
func capacityRuleID(checkID string) string { return "db-capacity:" + checkID }
func capacityProblemID(checkID, instance, subkey string) string {
	id := "db-capacity:" + checkID + ":" + instance
	if subkey != "" {
		id += ":" + subkey
	}
	return id
}

// capacityService is the value stored in the Problem.service column — the
// receiver instance, optionally qualified by the dimension subkey so a
// per-tablespace Problem shows "corebank-scan.prod·SYSAUX" in the inbox and
// FindOpenProblem(rule, service) dedups to exactly one open row per
// (instance, check, subkey).
// v0.9.402 (v0.9.401'in kardeşi) — service alanı artık YALNIZ instance:
// "corebank.prod·SYSAUX" birleşimi UI'nin service sözleşmesini runtime
// problemleriyle AYNI şekilde kırıyordu (listede kırık ad + sahte
// servise tıklama). Tablespace/keyspace alt-anahtarı problemID'de
// (per-subkey dedup oradan) ve reason'da yaşar.
//
// v0.9.1338 (entity-model Faz 4b) — 402 yarım kalmıştı. Alan artık
// birleşik ad taşımıyordu ama TAŞIDIĞI ŞEY de bir servis adı değildi:
// `corebank-scan.prod` hiçbir servis kataloğunda, hiçbir spans satırında
// yok. UI onu servis sanmaya devam etti, yani "sahte servise tıklama"
// kusuru bir biçim değişikliğiyle GİZLENDİ, çözülmedi. Özne artık
// TÜRÜYLE birlikte yazılıyor: DBSubjectID + Problem.Kind = ProblemKindDB.
//
// dbsys ÖNEMLİ: capacityCheck.dbsys 'ORACLE' (reason dizgisi büyük harfli
// okunsun diye), DBSubjectID küçük harfe indirir — tüm span/topoloji
// tarafı 'oracle' yazıyor ve iki yazım aynı veritabanı için iki ayrı özne
// üretirdi.
//
// Kodlanamayan durum (bir bileşen boş) HAM instance'a düşer: DBSubjectID
// boş dönerse bu fonksiyon v0.9.402'nin çıktısını bayt-bayt verir ve
// çağıran Kind'ı da 'service' bırakır — çözülmemiş dal = bugünkü dal.
func capacityService(instance, _ string) string {
	return instance
}

// capacitySubject — (özne dizgisi, özne türü) çifti. Tek fonksiyon
// çünkü ikisi AYRILDIĞINDA ayrışıyorlar: bir dalda db biçimi yazılıp
// Kind'ın 'service' kalması, o satırı hem çıkmaz link hem de
// çözümlenemez ad yapardı — v0.9.1029'un topoloji tarafında ölçtüğü
// kusurun ta kendisi (aynı düğüm hem kind:"service" hem ham önekli ad).
//
// SAF: chstore.DBSubjectID dışında hiçbir şeye dokunmuyor, tablo-testli.
func capacitySubject(dbsys, instance string) (subject, kind string) {
	if id := chstore.DBSubjectID(dbsys, instance); id != "" {
		return id, chstore.ProblemKindDB
	}
	return capacityService(instance, ""), chstore.ProblemKindService
}

// evaluateDBCapacity is the new evaluateAll pass. One bounded read per
// check (each grouped by instance, so no per-instance fan-out), then
// open/refresh/resolve each sample exactly like evaluateOne. Runs on the
// leader tick — no new goroutine, no new route.
func (e *Evaluator) evaluateDBCapacity(ctx context.Context) {
	// v0.9.522 — açık problemler tik başına BİR kez (runtime_pods ile aynı
	// gerekçe: instance × subkey başına ayrı FindOpenProblemByID, `problems`
	// state tablosu olduğu için hepsi in-order ana bağlantıdan ilk CH
	// node'una gidiyordu). Hata halinde tik ATLANIR — boş kabul etmek açık
	// problemleri yeniden açtırırdı.
	snap, err := e.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		log.Printf("[evaluator] db-capacity: açık problem anlık görüntüsü alınamadı, tik atlanıyor: %v", err)
		return
	}
	for _, c := range capacityChecks {
		// Defensive checks: skip cleanly unless the receiver is
		// actually publishing. Oracle checks have no probe → always run.
		if c.probe != "" {
			present, err := e.store.MetricExists(ctx, c.probe)
			if err != nil {
				log.Printf("[evaluator] db-capacity probe %s: %v", c.probe, err)
				continue
			}
			if !present {
				continue
			}
		}
		samples, err := c.read(ctx, e.store)
		if err != nil {
			log.Printf("[evaluator] db-capacity read %s: %v", c.id, err)
			continue
		}
		// v0.9.1065 — ETA tahmini: YALNIZ bir örnek %70+'a dayandıysa
		// trend serisi çekilir (tik başına check başına en çok bir okuma;
		// sakin filoda sıfır ek maliyet). Rate check'leri tahminsiz.
		var trend map[string][]chstore.CapacityTrendPoint
		if c.trendMetric != "" && !c.rate {
			for _, s := range samples {
				if s.Limit > 0 && s.Usage/s.Limit*100 >= capacityEtaMinPct {
					if tr, terr := e.store.UsageTrend(ctx, c.trendMetric, c.trendAttr, capacityEtaWindow); terr == nil {
						trend = tr
					} else {
						log.Printf("[evaluator] db-capacity trend %s: %v", c.id, terr)
					}
					break
				}
			}
		}
		for _, s := range samples {
			var etaH float64
			etaOK := false
			if trend != nil && s.Limit > 0 {
				if pts := trend[chstore.CapacityTrendKey(s.Instance, s.Subkey)]; len(pts) > 0 {
					etaH, _, etaOK = capacityETA(pts, s.Limit)
				}
			}
			e.reconcileCapacity(ctx, c, s, snap, etaH, etaOK)
		}
	}
}

// reconcileCapacity opens / refreshes / resolves the Problem for one
// sample, mirroring evaluateOne's open/refresh/resolve switch. Dedup is by
// (rule_id, service) via FindOpenProblem + a stable Problem id.
func (e *Evaluator) reconcileCapacity(ctx context.Context, c capacityCheck, s chstore.CapacitySample, snap *chstore.OpenProblems, etaHours float64, etaOK bool) {
	ruleID := capacityRuleID(c.id)
	service, kind := capacitySubject(c.dbsys, s.Instance)

	// Look up the open problem FIRST — the decision is hysteresis-aware
	// (v0.8.320): an open problem holds until pct clears the band.
	// v0.9.402 — dedup deterministik ID'den (per-subkey granülerlik
	// service alanından taşınamaz artık); ID formatı değişmedi → prod'un
	// açık eski satırları bulunur, refresh'te service kendini onarır.
	existing := snap.ByID(capacityProblemID(c.id, s.Instance, s.Subkey))
	hasOpen := existing != nil && existing.ID != ""
	open, sev, pct := capacityDecision(s.Usage, s.Limit, c.rate, hasOpen)
	// v0.9.1065 (Faz 2.4) — TAHMİNSEL erken-açma: eşik dalı açmadıysa ama
	// eğim ≤6h'te dolmayı gösteriyorsa warning aç. Eşik dalı her zaman
	// kazanır (critical'ı tahmin düşüremez); resolve histerezisi aynen.
	if !open && etaOK && capacityPredictiveOpen(pct, etaHours) {
		open, sev = true, "warning"
	}

	switch {
	case open && !hasOpen:
		now := time.Now()
		p := chstore.Problem{
			ID:          capacityProblemID(c.id, s.Instance, s.Subkey),
			RuleID:      ruleID,
			RuleName:    "DB capacity · " + c.dbsys + " " + c.label,
			Severity:    sev,
			Service:     service,
			Kind:        kind,
			Metric:      "db.capacity",
			Value:       pct,
			Threshold:   capacityThreshold(c, sev),
			Status:      "open",
			Description: capacityReasonETA(c, s.Instance, s.Subkey, pct, etaHours, etaOK),
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] db-capacity open %s/%s: %v", ruleID, service, err)
			return
		}
		e.countOpened() // v0.9.550 — kalp atışı sayacı
		log.Printf("[evaluator] PROBLEM OPENED (db.capacity): %s", p.Description)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] db-capacity incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case open && hasOpen:
		// Refresh live value + severity (a warning can worsen into a
		// critical without re-opening). Keep the original StartedAt.
		// v0.8.309 — clamp to the age-based escalation floor: this line
		// used to rewrite escalateStaleProblems' critical back to the
		// gauge-derived warning every tick, and the sweep re-escalated +
		// re-paged 60s later — the storm (87% tablespace = a critical
		// page per minute, for hours).
		// v0.9.402 — service self-heal (401 emsali): eski birleşik satır
		// gerçek instance adıyla yeniden yazılır.
		// v0.9.1338 — self-heal artık TÜRÜ de taşıyor. Bu satır olmadan
		// prod'un AÇIK db-capacity problemleri ham instance adında ve
		// kind='service'te takılı kalırdı: yeni özne yalnız bir sonraki
		// AÇILIŞTA görünürdü ve açık bir kapasite problemi günlerce açık
		// kalabilir. ReplacingMergeTree bütün-satır replace olduğu için
		// refresh turu iki alanı da yerine oturtuyor.
		existing.Service = service
		existing.Kind = kind
		existing.Value = pct
		existing.Severity = effectiveSeverity(sev, time.Since(time.Unix(0, existing.StartedAt)), e.escalationCfg(ctx))
		existing.Threshold = capacityThreshold(c, sev)
		existing.Description = capacityReasonETA(c, s.Instance, s.Subkey, pct, etaHours, etaOK)
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] db-capacity refresh %s/%s: %v", ruleID, service, err)
		}

	case !open && hasOpen:
		// v0.9.977 — kapanışta doluluk oranı EZİLMİYOR: "%%91'e çıkmıştı"
		// bilgisi kapanmış satırın tek kanıtı.
		chstore.MarkResolved(existing, time.Now().UnixNano())
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] db-capacity resolve %s/%s: %v", ruleID, service, err)
		} else {
			e.countResolved() // v0.9.550 — kalp atışı sayacı
			log.Printf("[evaluator] PROBLEM RESOLVED (db.capacity): %s %s on %s",
				c.dbsys, c.label, s.Instance)
		}
	}
}

// capacityThreshold reports the % threshold that the firing severity
// crossed, so the Problem row carries a meaningful threshold for the
// P1/P2/P3 breach-ratio logic. Rate checks use 0 (any rate breaches).
func capacityThreshold(c capacityCheck, severity string) float64 {
	if c.rate {
		return 0
	}
	if severity == "critical" {
		return capacityCritPct
	}
	return capacityWarnPct
}
