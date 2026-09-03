package evaluator

// alert_target.go — v0.10.331: hedefli kural (AlertRule.Target = DB ifadesi).
// evaluateAll döngüsünde LogQuery/Watcher gibi ayrı dal; servis hedefleri
// yerine TEK özne: DBSubjectID(db_system, db_name) (Kind=db → mevcut kanallar
// + DB sahibi/SRE maili). Ölçü db_statement_summary_5m'den (tüm çağıranlar),
// eşik/karşılaştırıcı/pencere/süreklilik/cooldown/taban sıradan kuralla aynı
// sözleşme (breachStart / resolvedAt damgaları, openSnap dedup).

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// targetSubject — Problem öznesi: DB kimliği kurulabiliyorsa Kind=db; değilse
// ifadeyi çalıştıran ilk servis (Kind=service) — hiç boş kalmaz.
func targetSubject(t chstore.RuleTarget, services []string) (subject, kind string) {
	if id := chstore.DBSubjectID(t.DBSystem, t.DBName); id != "" {
		return id, chstore.ProblemKindDB
	}
	if len(services) > 0 && services[0] != "" {
		return services[0], chstore.ProblemKindService
	}
	return "db-statement:" + t.StmtHash, chstore.ProblemKindService
}

func fmtMsShort(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.2f s", ms/1000)
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// describeTargetProblem — gerekçe: kural adı + ölçü + eşik + pencere + yürütme +
// çağıranlar + örnek SQL (kısaltılmış). Saf.
func describeTargetProblem(r chstore.AlertRule, st chstore.StatementWindowStats, value float64) string {
	label := strings.TrimSuffix(strings.TrimPrefix(r.Metric, "db_stmt_"), "_ms")
	sample := ""
	if r.Target != nil && r.Target.Sample != "" {
		sample = r.Target.Sample
	} else {
		sample = st.Sample
	}
	sample = strings.Join(strings.Fields(sample), " ")
	if len(sample) > 200 {
		sample = sample[:200] + "…"
	}
	callers := strings.Join(st.Services, ", ")
	if callers == "" {
		callers = "—"
	}
	return fmt.Sprintf("%s — statement %s %s, threshold %s %s over %ds window (%d executions; callers: %s): %s",
		r.Name, label, fmtMsShort(value), r.Comparator, fmtMsShort(r.Threshold), r.WindowSec, st.Count, callers, sample)
}

func (e *Evaluator) evaluateTargetRule(ctx context.Context, r chstore.AlertRule, openSnap *chstore.OpenProblems) {
	if r.Target == nil || r.Target.Kind != chstore.RuleTargetDBStatement {
		return
	}
	window := time.Duration(r.WindowSec) * time.Second
	st, err := e.store.StatementWindowStats(ctx, *r.Target, window)
	if err != nil {
		log.Printf("[evaluator] target rule %s (%s): %v", r.ID, r.Name, err)
		return
	}
	subject, kind := targetSubject(*r.Target, st.Services)
	key := breachKey{RuleID: r.ID, Service: subject}
	now := time.Now()
	if r.MinSamples > 0 && st.Count < uint64(r.MinSamples) {
		e.clearBreach(ctx, key)
		return
	}
	value := chstore.TargetMetricValue(st, r.Metric)
	breached := st.Count > 0 && compare(value, r.Comparator, r.Threshold)
	if breached && r.ForSec > 0 {
		first, existing := e.breachStart(ctx, key, now, r.ForSec)
		if !existing || now.Sub(first) < time.Duration(r.ForSec)*time.Second {
			return
		}
	}
	if !breached {
		e.clearBreach(ctx, key)
	}
	var open *chstore.Problem
	if openSnap != nil {
		open = openSnap.ByKey(r.ID, subject)
	} else {
		open, err = e.store.FindOpenProblem(ctx, r.ID, subject)
		if err != nil {
			log.Printf("[evaluator] target rule %s open lookup: %v", r.ID, err)
			return
		}
	}
	hasOpen := open != nil && open.ID != ""
	switch {
	case breached && !hasOpen:
		if r.CooldownSec > 0 {
			if rt, seen := e.resolvedAt(ctx, key); seen && now.Sub(rt) < time.Duration(r.CooldownSec)*time.Second {
				return
			}
		}
		p := chstore.Problem{
			ID: newID(), RuleID: r.ID, RuleName: r.Name, Severity: r.Severity,
			Service: subject, Kind: kind, Metric: r.Metric, Value: value,
			Comparator: r.Comparator, Threshold: r.Threshold, Status: "open",
			Description: describeTargetProblem(r, st, value),
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] target rule open %s: %v", r.ID, err)
			return
		}
		e.countOpened()
		log.Printf("[evaluator] PROBLEM OPENED (db statement rule): %s", p.Description)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] target rule incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}
	case breached && hasOpen:
		open.Value = value
		open.Threshold = r.Threshold
		open.Severity = effectiveSeverity(r.Severity, time.Since(time.Unix(0, open.StartedAt)), e.escalationCfg(ctx))
		open.Description = describeTargetProblem(r, st, value)
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] target rule refresh %s: %v", r.ID, err)
		}
	case !breached && hasOpen:
		chstore.MarkResolved(open, now.UnixNano())
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] target rule resolve %s: %v", r.ID, err)
			return
		}
		e.countResolved()
		e.stampResolved(ctx, key, now, r.CooldownSec)
		log.Printf("[evaluator] PROBLEM RESOLVED (db statement rule): %s on %s", r.Name, subject)
	}
}
