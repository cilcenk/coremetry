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
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/vmetrics"
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

// evaluateTargetRule — hedefli kural dağıtımı (evaluator.go tek dal): kind'a göre.
func (e *Evaluator) evaluateTargetRule(ctx context.Context, r chstore.AlertRule, openSnap *chstore.OpenProblems) {
	if r.Target == nil {
		return
	}
	switch r.Target.Kind {
	case chstore.RuleTargetDBStatement:
		e.evaluateDBStatementTargetRule(ctx, r, openSnap)
	case chstore.RuleTargetKafkaClient: // v0.10.554
		e.evaluateKafkaTargetRule(ctx, r, openSnap)
	case chstore.RuleTargetHTTPRoute: // v0.10.705
		e.evaluateHTTPRouteTargetRule(ctx, r, openSnap)
	}
}

func (e *Evaluator) evaluateDBStatementTargetRule(ctx context.Context, r chstore.AlertRule, openSnap *chstore.OpenProblems) {
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
	e.settleTargetBreach(ctx, r, key, subject, kind, value, breached, now,
		func() string { return describeTargetProblem(r, st, value) }, openSnap, "db statement rule")
}

// settleTargetBreach — hedefli kuralın ortak ihlal/for/cooldown/aç-yenile-kapat
// yarısı (v0.10.554'te DB ifadesi gövdesinden çıkarıldı; davranış bayt-bayt aynı,
// yalnız açıklama üreticisi ve log etiketi parametre). Her hedef türü değeri
// kendi kaynağından okur, buraya "value + breached" ile gelir.
func (e *Evaluator) settleTargetBreach(ctx context.Context, r chstore.AlertRule, key breachKey, subject, kind string,
	value float64, breached bool, now time.Time, describe func() string, openSnap *chstore.OpenProblems, tag string) {
	var err error
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
			Description: describe(),
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] target rule open %s: %v", r.ID, err)
			return
		}
		e.countOpened()
		log.Printf("[evaluator] PROBLEM OPENED (%s): %s", tag, p.Description)
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
		open.Description = describe()
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
		log.Printf("[evaluator] PROBLEM RESOLVED (%s): %s on %s", tag, r.Name, subject)
	}
}

// ── Kafka istemci hedefi (v0.10.554, Messaging Kafka Faz 5) ─────────────────
//
// Değer VM seam'inden: vmetrics.KafkaQuery (katalog toplaması: lag → max,
// üretici hata oranı → sum; gauge'a rate() yok) tek seri, pencere = kural
// penceresi, kova ≈ 1 dk (≤60). Pencere değeri = serilerin EN KÖTÜ noktası;
// MinSamples = nokta (kova) sayısı. Özne = servis (Problem servis altında).
// VM yapılandırılmamışsa kural sessizce atlanmaz: bir kez loglanır.

var kafkaTargetVMWarn sync.Once

func kafkaTargetMaxDataPoints(windowSec uint32) int {
	n := int(windowSec / 60)
	if n < 1 {
		n = 1
	}
	if n > 60 {
		n = 60
	}
	return n
}

// kafkaTargetValue — en kötü nokta + nokta sayısı.
func kafkaTargetValue(series []chstore.SpanMetricSeries) (float64, uint32) {
	var worst float64
	var n uint32
	for _, s := range series {
		for _, p := range s.Points {
			if n == 0 || p.Value > worst {
				worst = p.Value
			}
			n++
		}
	}
	return worst, n
}

func describeKafkaTargetProblem(r chstore.AlertRule, value float64, samples uint32) string {
	scope, label, unit := "", "Kafka istemci metriği", ""
	if t := r.Target; t != nil {
		scope = t.Service
		if t.Topic != "" {
			scope += " · topic " + t.Topic
		}
		if t.ClientID != "" {
			scope += " · istemci " + t.ClientID
		}
	}
	switch r.Metric {
	case "kafka_lag_max":
		label, unit = "istemcinin gördüğü en yüksek lag (partition; consumer group lag'i değil)", "kayıt"
	case "kafka_producer_error_rate":
		label, unit = "gönderim hatası", "kayıt/sn"
	}
	return fmt.Sprintf("%s — %s: %s %.0f %s, eşik %s %.0f, %ds pencere (%d kova)",
		r.Name, scope, label, value, unit, r.Comparator, r.Threshold, r.WindowSec, samples)
}

func (e *Evaluator) evaluateKafkaTargetRule(ctx context.Context, r chstore.AlertRule, openSnap *chstore.OpenProblems) {
	t := r.Target
	if e.vmetrics == nil || !e.vmetrics.Configured() {
		kafkaTargetVMWarn.Do(func() {
			log.Printf("[evaluator] kafka_client rule %s (%s): VictoriaMetrics yapılandırılmamış, kural değerlendirilmiyor", r.ID, r.Name)
		})
		return
	}
	m, ok := vmetrics.KafkaMetricByName(chstore.KafkaTargetMetricName(r.Metric))
	if !ok {
		log.Printf("[evaluator] kafka_client rule %s: metric %q katalogda yok", r.ID, r.Metric)
		return
	}
	window := time.Duration(r.WindowSec) * time.Second
	if window < time.Minute {
		window = time.Minute
	}
	now := time.Now()
	f, err := vmetrics.KafkaQuery(m, vmetrics.KafkaScope{
		Services: []string{t.Service}, Topic: t.Topic, ClientID: t.ClientID,
		From: now.Add(-window), To: now, MaxDataPoints: kafkaTargetMaxDataPoints(r.WindowSec),
	}, nil)
	if err != nil {
		log.Printf("[evaluator] kafka_client rule %s: %v", r.ID, err)
		return
	}
	series, err := e.vmetrics.QueryMetric(ctx, f)
	if err != nil {
		log.Printf("[evaluator] kafka_client rule %s (%s): %v", r.ID, r.Name, err)
		return
	}
	value, samples := kafkaTargetValue(series)
	subject, kind := t.Service, chstore.ProblemKindService
	key := breachKey{RuleID: r.ID, Service: subject}
	if r.MinSamples > 0 && samples < r.MinSamples {
		e.clearBreach(ctx, key)
		return
	}
	breached := samples > 0 && compare(value, r.Comparator, r.Threshold)
	e.settleTargetBreach(ctx, r, key, subject, kind, value, breached, now,
		func() string { return describeKafkaTargetProblem(r, value, samples) }, openSnap, "kafka client rule")
}
