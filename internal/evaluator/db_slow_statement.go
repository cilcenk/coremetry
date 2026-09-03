package evaluator

// db_slow_statement.go — v0.10.325: yavaş SQL dedektörü. Operatör isteği
// (2026-09-03, "Gerekiyor" / spec "Uygun"): bir statement'ın 5 dk p95'i
// eşiği (vars. 1 s) ≥ForBuckets ardışık kovada, yürütme tabanının üstünde
// aşınca Problem açılır; Kind=db özne (DBSubjectID) → notify mevcut takım
// yönlendirmesiyle sahibi + SRE'ye mail atar. db_capacity.go şablonu:
// lider-kilitli tick, OpenProblemsSnapshot dedup, ReplacingMergeTree
// problems, incident-attach, escalation.
//
// Karar SAF (slowStmtDecide) ve tablo-testli; I/O yalnız
// evaluateDBSlowStatements'ta.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const dbSlowStmtRuleID = "db-slow-stmt"

type slowStmtKey struct {
	DBSystem, DBName, Service string
	Hash                      uint64
}

type slowStmtFinding struct {
	Key      slowStmtKey
	Severity string
	P95Ms    float64
	P99Ms    float64
	Count    uint64
	Errors   uint64
	Buckets  int
	Sample   string
	Exemplar string
	Latest   time.Time
}

func slowStmtProblemID(k slowStmtKey) string {
	return fmt.Sprintf("%s:%s:%s:%d", dbSlowStmtRuleID, strings.ToLower(k.DBSystem), k.DBName, k.Hash)
}

// slowStmtDecide — kova satırları (zaten eşik+taban süzülmüş) → bulgular.
// Bir statement, FARKLI kova sayısı ≥ cfg.ForBuckets ise bulgu; ölçüler
// EN SON kovadan, şiddet son kovanın p95'ine göre. Çıktı deterministik
// (anahtar sırası) — testler ve dedup için.
func slowStmtDecide(rows []chstore.SlowStatementBucket, cfg chstore.DBSlowQueryConfig) []slowStmtFinding {
	type acc struct {
		buckets map[time.Time]struct{}
		latest  chstore.SlowStatementBucket
	}
	byKey := map[slowStmtKey]*acc{}
	for _, r := range rows {
		if r.Count < cfg.MinExecutions || r.P95Ms < cfg.ThresholdMs {
			continue
		}
		k := slowStmtKey{DBSystem: r.DBSystem, DBName: r.DBName, Service: r.Service, Hash: r.StmtHash}
		a := byKey[k]
		if a == nil {
			a = &acc{buckets: map[time.Time]struct{}{}, latest: r}
			byKey[k] = a
		}
		a.buckets[r.Bucket] = struct{}{}
		if r.Bucket.After(a.latest.Bucket) {
			a.latest = r
		}
	}
	need := cfg.ForBuckets
	if need <= 0 {
		need = 1
	}
	out := []slowStmtFinding{}
	for k, a := range byKey {
		if len(a.buckets) < need {
			continue
		}
		sev := "warning"
		if cfg.CriticalMs > 0 && a.latest.P95Ms >= cfg.CriticalMs {
			sev = "critical"
		}
		out = append(out, slowStmtFinding{
			Key: k, Severity: sev, P95Ms: a.latest.P95Ms, P99Ms: a.latest.P99Ms,
			Count: a.latest.Count, Errors: a.latest.Errors, Buckets: len(a.buckets),
			Sample: a.latest.Sample, Exemplar: a.latest.Exemplar, Latest: a.latest.Bucket,
		})
	}
	sort.Slice(out, func(i, j int) bool { return slowStmtProblemID(out[i].Key) < slowStmtProblemID(out[j].Key) })
	return out
}

func fmtMs(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.2f s", ms/1000)
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// slowStmtReason — Problem açıklaması: ölçü + kapsam + örnek SQL (kısaltılmış).
func slowStmtReason(f slowStmtFinding, cfg chstore.DBSlowQueryConfig) string {
	sample := strings.Join(strings.Fields(f.Sample), " ")
	if len(sample) > 200 {
		sample = sample[:200] + "…"
	}
	return fmt.Sprintf("Slow SQL: p95 %s (p99 %s) over %d executions/5m, %d consecutive buckets ≥ %s — %s/%s via %s: %s",
		fmtMs(f.P95Ms), fmtMs(f.P99Ms), f.Count, f.Buckets, fmtMs(cfg.ThresholdMs), f.Key.DBSystem, f.Key.DBName, f.Key.Service, sample)
}

// slowStmtSubject — özne: DBSubjectID(db_system, db_name) → Kind=db (takım
// yönlendirmesi DB sahibi + SRE); kurulamazsa çağıran servis, Kind=service.
func slowStmtSubject(k slowStmtKey) (service, kind string) {
	if id := chstore.DBSubjectID(k.DBSystem, k.DBName); id != "" {
		return id, chstore.ProblemKindDB
	}
	return k.Service, chstore.ProblemKindService
}

func (e *Evaluator) evaluateDBSlowStatements(ctx context.Context) {
	cfg := e.store.GetDBSlowQuery(ctx)
	if !cfg.Enabled {
		return
	}
	now := time.Now()
	since := now.Truncate(5 * time.Minute).Add(-time.Duration(cfg.ForBuckets-1) * 5 * time.Minute)
	rows, err := e.store.SlowStatementBuckets(ctx, since, cfg.MinExecutions, cfg.ThresholdMs)
	if err != nil {
		log.Printf("[evaluator] db-slow-stmt read: %v", err)
		return
	}
	snap, err := e.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		log.Printf("[evaluator] db-slow-stmt: açık problem anlık görüntüsü alınamadı, tik atlanıyor: %v", err)
		return
	}
	findings := slowStmtDecide(rows, cfg)
	seen := map[string]struct{}{}
	for _, f := range findings {
		id := slowStmtProblemID(f.Key)
		seen[id] = struct{}{}
		service, kind := slowStmtSubject(f.Key)
		existing := snap.ByID(id)
		if existing == nil || existing.ID == "" {
			p := chstore.Problem{
				ID: id, RuleID: dbSlowStmtRuleID,
				RuleName:    "Slow SQL statement · " + f.Key.DBSystem,
				Severity:    f.Severity,
				Service:     service,
				Kind:        kind,
				Metric:      "db.statement_p95_ms",
				Value:       f.P95Ms,
				Threshold:   cfg.ThresholdMs,
				Status:      "open",
				Description: slowStmtReason(f, cfg),
				StartedAt:   now.UnixNano(),
			}
			if err := e.store.UpsertProblem(ctx, p); err != nil {
				log.Printf("[evaluator] db-slow-stmt open %s: %v", id, err)
				continue
			}
			e.countOpened()
			log.Printf("[evaluator] PROBLEM OPENED (db.slow_statement): %s", p.Description)
			if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
				log.Printf("[evaluator] db-slow-stmt incident attach: %v", err)
			}
			if e.notifier != nil {
				go e.notifier.SendProblemAlert(context.Background(), p)
			}
			continue
		}
		existing.Service, existing.Kind = service, kind
		existing.Value = f.P95Ms
		existing.Threshold = cfg.ThresholdMs
		existing.Severity = effectiveSeverity(f.Severity, time.Since(time.Unix(0, existing.StartedAt)), e.escalationCfg(ctx))
		existing.Description = slowStmtReason(f, cfg)
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] db-slow-stmt refresh %s: %v", id, err)
		}
	}
	// Çözüm: bu kuralın açık problemi bulgularda yoksa ve tutma süresi
	// dolduysa kapat (flap önleyici: eşik altına iner inmez değil).
	for _, p := range snap.All() {
		if p == nil || p.RuleID != dbSlowStmtRuleID || p.Status != "open" {
			continue
		}
		if _, ok := seen[p.ID]; ok {
			continue
		}
		if time.Since(time.Unix(0, p.StartedAt)) < time.Duration(cfg.CooldownSec)*time.Second {
			continue
		}
		chstore.MarkResolved(p, now.UnixNano())
		if err := e.store.UpsertProblem(ctx, *p); err != nil {
			log.Printf("[evaluator] db-slow-stmt resolve %s: %v", p.ID, err)
			continue
		}
		e.countResolved()
		log.Printf("[evaluator] PROBLEM RESOLVED (db.slow_statement): %s", p.ID)
	}
}
