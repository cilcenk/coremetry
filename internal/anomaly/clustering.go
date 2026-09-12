package anomaly

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/correlator"
)

// clustering.go — anomali kümeleme (v0.9.1069, F1.6-R2; spec:
// docs/plans/spec-anomaly-clustering.md, operatör-onaylı seçenekler).
// Paylaşılan bir olay (Oracle, ağ) N servisi vurduğunda N bağımsız
// Problem yerine: propagation-sıralı KAYNAK serviste tek Problem +
// üyeler bağlı kanıt. Bu dosya SAF tespit — hangi adaylar bir küme
// oluşturur; açma/bildirim (R3) scan tarafında.

// clusterMinMembers — kaynak DAHİL en az üye. Spec kararı: ikili
// vakalarda yanlış birleştirme, ayrı kalmaktan pahalı.
const clusterMinMembers = 3

// openCandidate — bu tikte "open" kararı almış bir (servis, metrik).
//
// v0.10.699 (parite #1, dilim A) — Existing: aday bu tikin TAZE açılışı
// değil, son clusterJoinWindow içinde AÇILMIŞ bireysel problem
// (join-on-open). nil = taze. Tespit (detectAnomalyClusters) ikisini
// ayırt etmez; fark yan etkide: taze üye bastırılır, önceden açık üye
// kümeye KATILIR (resolved + "merged into" eki, mergeIntoCluster).
type openCandidate struct {
	Service  string
	Metric   string
	Outcome  anomalyOutcome
	Existing *chstore.Problem
}

// clusterJoinWindow — v0.10.699. Dakikalara yayılan kaskadda ERKEN açılan
// bireysel problemler (db t0, caller t+2, caller t+4) bugüne dek kümeye
// hiç giremiyordu: aday yalnız taze açılıştı, üçüncü servis geldiğinde
// ilk ikisi çoktan kendi satırındaydı → üç ayrı Problem. Artık son 30 dk
// içinde AÇILMIŞ bireysel anomali problemleri de aday. 30 dk = incident
// groupingWindow (aynı olay ölçeği); sabit, ayar değil (spec açık soru 2).
//
// Kayan-pencere dersi (v0.10.199): üyelik problemin GÖZLENMİŞ açılış
// anına (StartedAt) bağlı, pencere kenarına değil — pencereden çıkan
// bir problem zaten ya merge edilmiştir (resolved, aday değil) ya da
// hiç kümelenmemiştir; tikler arası flip üretmez (clustering_join_test).
const clusterJoinWindow = 30 * time.Minute

// recentOpenCandidates — SAF. Snapshot'taki açık BİREYSEL metrik anomali
// problemlerinden (rule_id tam olarak "anomaly:<svc>:<metrik>", metrik
// izlenen listede — küme / external / service_silent satırları dışarıda)
// pencere içinde açılmış ve bu tik çözülmeyenleri aday yapar. Sıralı
// (servis, metrik) → determinizm.
func recentOpenCandidates(all []*chstore.Problem, now time.Time, window time.Duration, resolving map[string]bool, tracked map[string]bool) []openCandidate {
	cutoff := now.Add(-window).UnixNano()
	var out []openCandidate
	for _, p := range all {
		if p == nil || p.ID == "" || p.Status != "open" || p.Service == "" || p.Metric == "" {
			continue
		}
		if !tracked[p.Metric] || p.Kind == chstore.ProblemKindExternal {
			continue
		}
		if p.RuleID != "anomaly:"+p.Service+":"+p.Metric {
			continue
		}
		if p.StartedAt < cutoff || resolving[p.RuleID+"|"+p.Service] {
			continue
		}
		out = append(out, openCandidate{Service: p.Service, Metric: p.Metric, Outcome: outcomeOfProblem(p), Existing: p})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// outcomeOfProblem — açık satırdan aday sonucu: yön comparator'dan
// (v0.9.978: '<' = dropped), değer/baseline satırdaki value/threshold.
func outcomeOfProblem(p *chstore.Problem) anomalyOutcome {
	dir := "spiked"
	if strings.TrimSpace(p.Comparator) == "<" {
		dir = "dropped"
	}
	return anomalyOutcome{Action: "open", Severity: p.Severity, Direction: dir, Current: p.Value, Median: p.Threshold}
}

// mergeTargets — SAF. Kümeye katılan ÖNCEDEN AÇIK bireysel problemler:
// üyelerin Existing'i + KAYNAĞIN kendi aday satırlarının Existing'i
// (kaynak Members dışında tutulduğundan cands'tan bulunur). ID'ye göre
// tekrarsız + sıralı.
func mergeTargets(cl anomalyCluster, cands []openCandidate) []*chstore.Problem {
	seen := map[string]bool{}
	var out []*chstore.Problem
	add := func(p *chstore.Problem) {
		if p == nil || p.ID == "" || seen[p.ID] {
			return
		}
		seen[p.ID] = true
		out = append(out, p)
	}
	for _, m := range cl.Members {
		add(m.Existing)
	}
	for _, c := range cands {
		if c.Service == cl.Source {
			add(c.Existing)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// mergedNote — açıklama eki (saf): kaç önceden-açık satırın katıldığı
// DÜRÜSTÇE yazılır; operatör "bu problemler nereye gitti" sorusunu
// küme satırından cevaplar.
func mergedNote(n int) string {
	if n <= 0 {
		return ""
	}
	return " " + itoaClusters(n) + " previously opened problem(s) were merged into this cluster (join-on-open)."
}

// clusterStartedAt — küme açılışı kaskadın GÖZLENMİŞ başlangıcı: katılan
// en eski bireysel problemin StartedAt'i (Davis: problem start = ilk
// olay). Yalnız AÇILIŞTA; tazelemede mevcut satırın zamanı korunur.
func clusterStartedAt(nowNs int64, targets []*chstore.Problem) int64 {
	start := nowNs
	for _, t := range targets {
		if t != nil && t.StartedAt > 0 && t.StartedAt < start {
			start = t.StartedAt
		}
	}
	return start
}

// anomalyCluster — tespit edilen bir küme. Source aday kümesinin
// İÇİNDEN, üyelerinin propagation-suçladığı servis; Members kaynak
// HARİÇ, suçlayanlar (deterministik sıralı).
type anomalyCluster struct {
	Source      string
	SourceScore float64 // suçlayanların kaynak-skorları toplamı (rapor için)
	Members     []openCandidate
}

// clusterRulePrefix / clusterProblemID — kararlı dedup anahtarları
// (capacityProblemID deseni): kaynak başına TEK satır, tazelemeler
// ReplacingMergeTree'de aynı satıra biner.
const clusterRulePrefix = "anomaly-cluster:"

func clusterProblemID(source string) string { return clusterRulePrefix + source }

// clusterSeverity — üyelerin en yükseği (saf). Kaynağın kendi adayı da
// Members dışında olduğundan kaynak-severity'si çağıranda katılır.
func clusterSeverity(members []openCandidate, sourceSeverity string) string {
	if sourceSeverity == "critical" {
		return "critical"
	}
	for _, m := range members {
		if m.Outcome.Severity == "critical" {
			return "critical"
		}
	}
	return "warning"
}

// clusterDescription — deterministik, operatör-okur açıklama (saf,
// tablo-testli). Üye listesi 10'da kesilir; kesim DÜRÜSTÇE yazılır.
func clusterDescription(cl anomalyCluster) string {
	seen := map[string]bool{}
	var parts []string
	for _, m := range cl.Members {
		label := m.Service + " (" + displayMetric(m.Metric) + " " + m.Outcome.Direction + ")"
		if seen[label] {
			continue
		}
		seen[label] = true
		parts = append(parts, label)
	}
	shown := parts
	extra := 0
	if len(shown) > 10 {
		extra = len(shown) - 10
		shown = shown[:10]
	}
	d := "Correlated incident rooted at " + cl.Source + " — " +
		itoaClusters(len(seen)+1) + " services degraded together (propagation-linked): " +
		joinComma(shown)
	if extra > 0 {
		d += ", +" + itoaClusters(extra) + " more"
	}
	d += ". Member anomalies were folded into this problem instead of opening individually."
	return d
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func itoaClusters(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// detectAnomalyClusters — saf, tablo-testli. Kural:
//   - Aday servislerden her X için RankRootCausesFromEdges(adj, X)
//     koşar; X'in skorlu (>0) nedenlerinden aday kümesinde OLANLAR
//     "X, S'yi suçluyor" kenarı üretir (en yüksek skorlu S seçilir —
//     bir aday tek kümeye girer).
//   - Kaynak S: kendisi de ADAY olan ve kaynak dahil ≥minMembers üye
//     toplayan servis. Spec: skorsuz/kaynaksız vakalarda kümeleme YOK —
//     bugünkü bağımsız davranış sürer.
//   - Determinizm: kaynaklar üye-sayısı desc, sonra skor desc, sonra ad;
//     üyeler servis/metrik sıralı. Aynı girdi aynı çıktı.
func detectAnomalyClusters(cands []openCandidate, weightedAdj []chstore.ServiceEdgePair, minMembers int) []anomalyCluster {
	if minMembers < 2 || len(cands) < minMembers {
		return nil
	}
	inSet := map[string]bool{}
	for _, c := range cands {
		inSet[c.Service] = true
	}

	// Her aday SERVİS için en güçlü aday-kümesi-içi suçlu.
	type blame struct {
		source string
		score  float64
	}
	blames := map[string]blame{} // service -> suçladığı kaynak
	for svc := range inSet {
		best := blame{}
		for _, sc := range correlator.RankRootCausesFromEdges(weightedAdj, svc) {
			if sc.Score <= 0 || sc.Service == svc || !inSet[sc.Service] {
				continue
			}
			if sc.Score > best.score {
				best = blame{source: sc.Service, score: sc.Score}
			}
		}
		if best.source != "" {
			blames[svc] = best
		}
	}

	// Kaynak başına suçlayan servisleri topla.
	bySource := map[string][]string{}
	scoreSum := map[string]float64{}
	for svc, b := range blames {
		bySource[b.source] = append(bySource[b.source], svc)
		scoreSum[b.source] += b.score
	}

	// Kaynak adayları: kendisi de aday + (kaynak dahil) ≥minMembers.
	type srcCand struct {
		source string
		count  int
		score  float64
	}
	var sources []srcCand
	for src, blamers := range bySource {
		if !inSet[src] {
			continue
		}
		if 1+len(blamers) < minMembers {
			continue
		}
		sources = append(sources, srcCand{source: src, count: len(blamers), score: scoreSum[src]})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].count != sources[j].count {
			return sources[i].count > sources[j].count
		}
		if sources[i].score != sources[j].score {
			return sources[i].score > sources[j].score
		}
		return sources[i].source < sources[j].source
	})

	candsByService := map[string][]openCandidate{}
	for _, c := range cands {
		candsByService[c.Service] = append(candsByService[c.Service], c)
	}

	assigned := map[string]bool{} // servis tek kümeye girer
	var out []anomalyCluster
	for _, sc := range sources {
		if assigned[sc.source] {
			continue
		}
		var members []openCandidate
		for _, blamer := range bySource[sc.source] {
			if assigned[blamer] || blames[blamer].source != sc.source {
				continue
			}
			members = append(members, candsByService[blamer]...)
		}
		memberServices := map[string]bool{}
		for _, m := range members {
			memberServices[m.Service] = true
		}
		if 1+len(memberServices) < minMembers {
			continue // atamalar sonrası küme eridi
		}
		sort.Slice(members, func(i, j int) bool {
			if members[i].Service != members[j].Service {
				return members[i].Service < members[j].Service
			}
			return members[i].Metric < members[j].Metric
		})
		assigned[sc.source] = true
		for s := range memberServices {
			assigned[s] = true
		}
		out = append(out, anomalyCluster{Source: sc.source, SourceScore: sc.score, Members: members})
	}
	return out
}

// ── R3: yan etkiler (v0.9.1070) ────────────────────────────────────────

// applyClusters — tespit edilen kümeleri Problem'e çevirir; bastırılan
// servis kümesini ve bu tik kümeye KATILAN (resolved) problem id'lerini
// döndürür. Taze üye bastırılır (bireysel problemi açılmaz); önceden
// açık üye (v0.10.699 join-on-open) küme satırı yazıldıktan SONRA
// mergeIntoCluster ile kapatılır — küme yazılamazsa kimse kapatılmaz.
// Spec kararları: kaynak başına TEK problem + TEK bildirim; yaşam
// döngüsü kaynak sinyaline bağlı (resolveStaleClusters).
func (d *Detector) applyClusters(ctx context.Context, clusters []anomalyCluster, cands []openCandidate, snap *chstore.OpenProblems, cfg chstore.AnomalySensitivityConfig, sourceSeverity map[string]string) (suppressed, merged map[string]bool) {
	suppressed = map[string]bool{}
	merged = map[string]bool{}
	for _, cl := range clusters {
		id := clusterProblemID(cl.Source)
		suppressed[cl.Source] = true
		for _, m := range cl.Members {
			suppressed[m.Service] = true
		}
		targets := mergeTargets(cl, cands)
		desc := clusterDescription(cl) + mergedNote(len(targets))
		sev := clusterSeverity(cl.Members, sourceSeverity[cl.Source])
		memberSvc := map[string]bool{}
		for _, m := range cl.Members {
			memberSvc[m.Service] = true
		}
		if existing := snap.ByID(id); existing != nil && existing.ID != "" {
			existing.Description = desc
			existing.Value = float64(len(memberSvc) + 1)
			existing.Severity = sev
			if err := d.store.UpsertProblem(ctx, *existing); err != nil {
				log.Printf("[anomaly] cluster refresh %s: %v", id, err)
				continue
			}
			d.mergeIntoCluster(ctx, id, targets, merged)
			continue
		}
		p := chstore.Problem{
			ID:          id,
			RuleID:      clusterRulePrefix + cl.Source,
			RuleName:    "Anomaly cluster · " + cl.Source,
			Severity:    sev,
			Service:     cl.Source,
			Metric:      "cluster",
			Value:       float64(len(memberSvc) + 1),
			Threshold:   float64(clusterMinMembers),
			Comparator:  ">",
			Status:      "open",
			Description: desc,
			StartedAt:   clusterStartedAt(time.Now().UnixNano(), targets),
		}
		if err := d.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[anomaly] cluster open %s: %v", id, err)
			continue
		}
		log.Printf("[anomaly] CLUSTER OPENED %s — %d services (score %.2f, %d merged)",
			cl.Source, len(memberSvc)+1, cl.SourceScore, len(targets))
		if cfg.AttachesToIncident() {
			if _, err := d.store.AttachProblemToIncident(ctx, p); err != nil {
				log.Printf("[anomaly] cluster incident attach: %v", err)
			}
		}
		if d.notifier != nil {
			go d.notifier.SendProblemAlert(context.Background(), p)
		}
		d.mergeIntoCluster(ctx, id, targets, merged)
	}
	return suppressed, merged
}

// mergeIntoCluster — v0.10.699. Önceden açık bireysel problemi kümeye
// katar: resolved + açıklama eki "· merged into anomaly-cluster:<src>"
// (kolon yok, spec açık soru 1). Bildirim YOK — bireysel anomali
// resolve'u da bugün bildirim göndermiyor (applyOutcome resolve dalı);
// kümenin tek bildirimi kaynağa gider. merged[id] = true → scan aynı
// tikte bu satırı tazelemez (tam-satır replace onu open'a geri yazardı).
func (d *Detector) mergeIntoCluster(ctx context.Context, clusterID string, targets []*chstore.Problem, merged map[string]bool) {
	now := time.Now().UnixNano()
	for _, t := range targets {
		cp := *t
		cp.Description = strings.TrimRight(cp.Description, " ") + " · merged into " + clusterID
		chstore.MarkResolved(&cp, now)
		if err := d.store.UpsertProblem(ctx, cp); err != nil {
			log.Printf("[anomaly] merge %s into %s: %v", t.ID, clusterID, err)
			continue
		}
		merged[t.ID] = true
		log.Printf("[anomaly] MERGED %s · %s → %s", t.Service, t.Metric, clusterID)
	}
}

// resolveStaleClusters — kaynak sinyaliyle yaşam (spec kararı a):
// kaynak serviste bu tik hiçbir metrik "open" kararı taşımıyorsa küme
// problemi çözülür. Üyeler hâlâ kötüyse sonraki tiklerde bastırma
// kalkmış olur ve kendi problemlerini açarlar — kaynak iyileşti,
// kalan dertler kendi satırlarında dürüstçe yaşar.
func (d *Detector) resolveStaleClusters(ctx context.Context, snap *chstore.OpenProblems, openServices map[string]bool) {
	for _, p := range snap.All() {
		if !strings.HasPrefix(p.RuleID, clusterRulePrefix) {
			continue
		}
		if openServices[p.Service] {
			continue // kaynak hâlâ anomalili — küme yaşıyor
		}
		cp := *p
		chstore.MarkResolved(&cp, time.Now().UnixNano())
		if err := d.store.UpsertProblem(ctx, cp); err != nil {
			log.Printf("[anomaly] cluster resolve %s: %v", p.ID, err)
			continue
		}
		log.Printf("[anomaly] CLUSTER RESOLVED %s (source recovered)", p.Service)
	}
}
