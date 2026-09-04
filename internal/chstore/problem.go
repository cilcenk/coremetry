package chstore

// v0.9.507 — bu dosya artık %100 STATE okuyor (problems, alert_rules).
// Telemetri okumaları (deploys zenginleştirme + CalleesOf)
// problem_telemetry.go'ya ayrıldı; sebep orada yazılı. hash/fnv ve sort
// onlarla birlikte gitti.
import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AlertRule defines an evaluator condition. metric is one of:
//
//	error_rate    — % of error spans  (operand: percentage)
//	p99_ms / p95_ms / avg_ms / p50_ms — latency in ms
//	request_rate  — spans per second  (typically used with `<` to detect drops)
//	error_count   — number of error spans (absolute)
type AlertRule struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Service    string  `json:"service"`    // empty = applies to all services
	Metric     string  `json:"metric"`     // error_rate | p99_ms | request_rate | …
	Comparator string  `json:"comparator"` // > | >= | < | <=
	Threshold  float64 `json:"threshold"`
	WindowSec  uint32  `json:"windowSec"` // sliding window size
	Severity   string  `json:"severity"`  // info | warning | critical
	Enabled    bool    `json:"enabled"`
	BuiltIn    bool    `json:"builtIn"`
	// ForSec is the sustained-breach gate (v0.5.126): the
	// threshold must stay breached for this long before a
	// problem opens. Prometheus-style `for:` — kills single-
	// sample spike noise without changing the threshold. 0 =
	// open immediately (current behaviour).
	ForSec uint32 `json:"forSec"`
	// MinSamples is the sample-count floor (v0.5.128). When > 0
	// the evaluator only fires if the window saw at least this
	// many requests — kills low-traffic flapping on rate /
	// percentile metrics (a 1-request window with 1 error = 100%
	// error_rate, which is meaningless). 0 = no floor.
	MinSamples uint32 `json:"minSamples"`
	// CooldownSec is the post-resolution silence window (v0.5.129).
	// After a problem auto-resolves the evaluator suppresses re-
	// opens on the same (rule, service) for this many seconds —
	// kills threshold-jitter flapping where the value oscillates
	// at the boundary. 0 = re-open immediately.
	CooldownSec uint32 `json:"cooldownSec"`
	// RunbookURL — optional link an oncall reaches when the
	// rule fires. Surfaces on Problem detail + alert
	// notifications. Empty = no runbook configured.
	RunbookURL string `json:"runbookUrl,omitempty"`
	// LogQuery (v0.5.242) — KQL/Lucene clause that defines a
	// "saved-search alert". When set, the evaluator counts log
	// matches via the logstore in the rule's window and compares
	// to Threshold via Comparator instead of running the
	// span-derived Metric path. Service/Metric are still set
	// (service="" + metric="log_query" by convention) so the
	// rules table renders consistently. The OTel-canonical
	// shorthand (level:error, pod:my-pod) is rewritten by the
	// ES backend's expandShorthand so the same query works
	// against any shipping pipeline.
	LogQuery string `json:"logQuery,omitempty"`
	// WatcherJSON (v0.9.x) — the verbatim ES Watcher definition
	// (PUT _watcher/watch body) this rule was imported from. When
	// set, the evaluator runs the watcher path (internal/watcher
	// parse → logstore.RawSearch count → compare) instead of the
	// span-metric or log-query paths. Stored raw so the definition
	// round-trips byte-identical regardless of what the parser
	// models. Empty for every native rule. Metric="watcher" by
	// convention so the rules table renders consistently.
	WatcherJSON string `json:"watcherJson,omitempty"`
	// Target — v0.10.331: hedefli kural (alert_target.go); alert_rules.target_json.
	Target    *RuleTarget `json:"target,omitempty"`
	CreatedAt int64       `json:"createdAt"` // unix nanoseconds
}

// Problem ÖZNE TÜRLERİ (v0.9.1338, entity-model Faz 4b).
//
// `service` kolonu bugüne dek sorgusuz bir SERVİS ADI sayılıyordu ve bu
// hiçbir zaman doğru olmadı: db_capacity.go v0.9.402'den beri oraya bir
// receiver instance adı (`corebank-scan.prod`), monitor/runner.go ise
// monitor ADINI yazıyor. Otuz küsur okuma yolu o değeri servis sanıp
// çıkmaz link kuruyor ya da boş kova gösteriyor — hata değil CEVAPSIZLIK.
// Kind o soruyu YAPISAL olarak cevaplıyor.
const (
	// ProblemKindService — özne bir servis. VARSAYILAN: CH kolonu
	// `DEFAULT 'service'`, boş değer de buraya normalize edilir, yani
	// 18 üreticinin 17'si ve tüm geçmiş satırlar bugünkü anlamını
	// birebir korur.
	ProblemKindService = "service"
	// ProblemKindDB — özne bir veritabanı örneği; `service` kolonu
	// DBSubjectID biçiminde (`db:<system>@<instance>`).
	ProblemKindDB = "db"
	// ProblemKindExternal (v0.10.228, Influx D3) — dış metrik kaynağı özneleri
	// `ext:<kaynak>/<grup değerleri>`: servis değil, env şeridine girmez,
	// servis sayfası linki yok (db emsali).
	ProblemKindExternal = "external"
)

// ProblemSubjectKind — bir satırın özne türü, boş değeri normalize eder.
// Boş İKİ yoldan gelir ve İKİSİ de "servis" demek: (a) kolonun eklendiği
// boot'ta probe false okur ve SELECT kolonu atlar (iki-boot sözleşmesi),
// (b) v0.9.1338 öncesi yazılmış satırlar. Okuyucular bu fonksiyona
// gitsin, `p.Kind == ""` yazmasın — o karşılaştırma üçüncü bir dal doğurur.
func ProblemSubjectKind(kind string) string {
	if kind == "" {
		return ProblemKindService
	}
	return kind
}

type Problem struct {
	ID       string `json:"id"`
	RuleID   string `json:"ruleId"`
	RuleName string `json:"ruleName"`
	Severity string `json:"severity"`
	// Service — problemin ÖZNESİ. Kind'a göre okunur: kind=service ise
	// bir servis adı, kind=db ise bir DBSubjectID. Kolonun adı `service`
	// KALIYOR — özne kimliği hiçbir ORDER BY / shard / cache anahtarına
	// girmediği gibi, doğal anahtarın yerini de almaz (entity-model §7.4).
	Service string `json:"service"`
	// Kind (v0.9.1338) — özne TÜRÜ. Boş = service (bkz.
	// ProblemSubjectKind). scanProblemRow normalize eder, yani telde
	// hiçbir zaman boş görünmez.
	Kind      string  `json:"kind,omitempty"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	// Comparator (v0.9.976) — ihlali TANIMLAYAN yön, kuralın kendisinden
	// (AlertRule.Comparator) problem AÇILIRKEN kopyalanır: "<" / "<=" =
	// değer düştükçe kötüleşen aile (uptime, başarı oranı, sağlıklı pod),
	// ">" / ">=" = değer yükseldikçe kötüleşen aile (hata oranı, gecikme).
	//
	// computePriority'nin ters-çevirme kolu BUNA bakar. Alan yokken kol
	// "oran 1'in altındaysa ters çevir" diyordu, yani eşiğin ALTINDA kalan
	// her ">" problemi (3.922 / eşik 15) 3.8× "büyük ihlal" sayılıp P1
	// oluyordu — problemin gerçekte eşiğe UZAK olduğu durum.
	//
	// BOŞ = ters çevirme YOK. Eski satırlar, anomali/monitor/runtime
	// üreticileri ve elle kurulmuş Problem'ler bu daldan geçer; hepsi ">"
	// ailesi (değer yükseldikçe kötü) ya da tam-kayıp koluyla (Value 0)
	// zaten doğru sıralanıyor.
	Comparator string `json:"comparator,omitempty"`
	Status     string `json:"status"` // open | resolved
	// Pod (v0.9.403) — runtime pod denetimlerinde alarmın pod kimliği;
	// diğer üreticilerde boş. Service GERÇEK servis kalır (v0.9.401).
	Pod         string `json:"pod,omitempty"`
	Description string `json:"description"`
	// Assignee (v0.5.209) — free-form string, two flavours:
	//   • team name auto-set from service_metadata.owner_team
	//     when the problem opens (so "payments" surfaces without
	//     an operator action)
	//   • email of a specific operator after manual claim/assign
	// Empty = unassigned. Operator-editable via PATCH
	// /api/problems/{id}/assignee.
	Assignee   string `json:"assignee,omitempty"`
	StartedAt  int64  `json:"startedAt"` // unix ns
	ResolvedAt *int64 `json:"resolvedAt,omitempty"`
	// RunbookURL — composed at read time from the firing
	// alert rule (preferred) or the service catalog metadata
	// (fallback). Not stored on the problems table; the URL
	// is operator-curated and likely to change between when
	// the problem opened and when an oncall reads it. NEVER
	// scanned from CH — populated by EnrichProblems.
	RunbookURL string `json:"runbookUrl,omitempty"`
	// Clusters — k8s/openshift cluster names this problem's
	// service was active in around the time of the alert.
	// Populated at READ time from recent span activity (NOT
	// stored on the problems table). Empty when the service
	// hasn't carried a cluster attribute. Multi-cluster
	// services typically list 2-3 names; the UI renders
	// chips so the oncall sees "this fires on eu-west AND
	// eu-central" at a glance.
	Clusters []string `json:"clusters,omitempty"`
	// OwnerTeam / SRETeam (v0.8.290) — the owning + reliability
	// team for the firing service, pulled from the operator-
	// curated service catalog at READ time (NOT stored on the
	// problems row — a catalog edit reflects on the next refresh
	// without rewriting history). Empty when the service has no
	// catalog entry. Populated by EnrichProblemsWithTeams; powers
	// the owner/SRE team filters on /problems, mirroring the inbox.
	OwnerTeam string `json:"ownerTeam,omitempty"`
	SRETeam   string `json:"sreTeam,omitempty"`
	// TeamsVia (v0.9.1345) — takımlar DOLAYLI çözüldüyse, hangi servisin
	// katalog satırından geldikleri. BOŞ = doğrudan: özne bir servis ve
	// takımlar kendi satırından geliyor (bugüne kadarki tek hal).
	//
	// Doldurulduğu tek yer db-konulu problemler (kind=db): onların
	// `service` alanı bir DBSubjectID ve kataloğda satırı YOK, o yüzden
	// sahiplik "bu veritabanını EN ÇOK ÇAĞIRAN servisin takımı" kuralıyla
	// türetiliyor (operatör kararı 2026-08-24, chstore/db_ownership.go).
	//
	// Alan KANIT taşıdığı için var: türetim db_system KAPSAMINDA yapılıyor
	// (HAT A/HAT B kimlik uzayları kesişmiyor — identity.go'daki uyarı),
	// yani bir YAKLAŞIKLIK. Yalnız takım adını göstermek o yaklaşıklığı
	// görünmez kılar ve kesin bir atıf gibi okunurdu. Yüzey bu adı
	// kullanarak "en çok çağıran X üzerinden türetildi" diyor.
	TeamsVia string `json:"teamsVia,omitempty"`
	// RecentDeploy — most recent observed service.version
	// transition for this service in the window leading up
	// to the problem firing, or nil. The AI explain /
	// runbook prompts use this signal to ask "did a deploy
	// just happen?", and the UI surfaces it as a small
	// "deployed v1.2.3 · 6 min before" tag next to the
	// problem row. Populated at READ time from spans (NOT
	// stored on the problems table) — the deploy might be
	// confirmed retroactively after the row was written.
	RecentDeploy *RecentDeploy `json:"recentDeploy,omitempty"`
	// Priority (v0.5.210) — computed at read time from severity +
	// breach magnitude + deploy proximity. Three buckets:
	//   • P1 — handle now (critical + significant overshoot
	//     OR critical + just deployed). Top of the triage queue.
	//   • P2 — handle today (criticals at minor overshoot, OR
	//     warnings hit by recent deploy / 2x threshold breach).
	//   • P3 — handle when convenient (steady warnings, info-
	//     level rules). Filterable out of the default view.
	// Not stored on the problems table — recomputed every read
	// so a fresh deploy or a worsening value flips the bucket
	// without requiring a re-write of the row.
	Priority string `json:"priority,omitempty"`
	// PriorityReason — short human string explaining the bucket
	// pick ("critical + deploy 4m before", "2.5x threshold").
	// Driven by the same logic that sets Priority; surfaces in
	// the UI tooltip so the rule is auditable, not magic.
	PriorityReason string `json:"priorityReason,omitempty"`
	// AISummary (v0.5.254) — short LLM-generated context blurb
	// answering "why did this fire + what to look at first". Filled
	// asynchronously by the problemExplainer goroutine within ~30s
	// of problem open (critical severity only by default). Empty
	// when the explainer hasn't run yet OR the AI Copilot isn't
	// configured. AISummaryAt is the unix-ns timestamp of the last
	// generation; lets the UI show "AI insight · 12s ago".
	AISummary   string `json:"aiSummary,omitempty"`
	AISummaryAt int64  `json:"aiSummaryAt,omitempty"`
	// RootCause — compact top-suspect summary of the persisted
	// root-cause hypothesis the worker synthesized for this problem
	// (rc #3 of the anomaly → root-cause feature). Attached at READ
	// time by the /problems list handler via a single batch
	// GetHypotheses join (NO per-row fetch); nil when the worker
	// hasn't synthesized a hypothesis for this anchor yet. The
	// RootCauseRibbon renders the collapsed chip from this; the
	// expand fetches the full /rootcause fan-out on demand.
	RootCause *RootCauseSummary `json:"rootCause,omitempty"`
}

// RecentDeploy is the compact deploy signal attached to a
// firing Problem. AgeSeconds = problem.StartedAt - deploy.time,
// rounded — positive means deploy was BEFORE the problem
// (the typical correlate-with-incident case).
type RecentDeploy struct {
	Version    string `json:"version"`
	TimeUnixNs int64  `json:"timeUnixNs"`
	AgeSeconds int64  `json:"ageSeconds"`
	// Impact (v0.9.1059, Faz 1.4) — deploy'un önce/sonra RED kıyası.
	// YALNIZ kök-neden hipotez yolu doldurur (worker, deploy adayı
	// varken tek sınırlı okuma); problems listesi enrichment'ı
	// doldurmaz — omitempty, eski JSON bayt-bayt aynı.
	Impact *DeployImpact `json:"impact,omitempty"`
}

// EnrichProblemsWithPriority computes the P1/P2/P3 triage bucket
// for every problem in the slice. Pure function over already-
// loaded fields — no CH round-trip — so this is the last step
// in the enrichment chain and runs against the post-runbook /
// cluster / deploy values. Doesn't mutate stored rows; the
// bucket lives only on the wire so a worsening metric or a
// fresh deploy flips the rank on the next read.
//
// Blend formula (transparent on purpose — operator sees it in
// PriorityReason and can argue with it):
//
//	P1 (drop-everything) when ANY of:
//	  • severity = critical AND value ≥ Nx threshold     (significant breach)
//	  • severity = critical AND value tamamen kayıp      (0/X, v0.9.825)
//	  • severity = critical AND open ≥ N hours           (stale critical)
//
//	P2 (today) when ANY of:
//	  • severity = critical                              (criticals default to P2)
//	  • severity = warning  AND value ≥ Nx threshold     (significant warning)
//
//	P3 otherwise — steady warnings, info-level rules.
//
// v0.9.838 — iki "N" artık AYARLANABİLİR (ProblemPriorityConfig,
// system_settings anahtarı "problem_priority"): ihlal katı (varsayılan
// 2.0×) ve bayat-critical saati (varsayılan 4h, 0 = kapalı). Sabitleri
// bir çentik ötelemek exception tarafında üç kez çalışmadı (v0.9.775);
// aynı hatayı burada tekrarlamıyoruz. Varsayılanlar v0.9.838 öncesinin
// birebir aynısı, yani vidaya dokunmayan kurulum aynı sıralamayı görür.
//
// "Value above threshold" only makes sense when comparator is
// >= / >; for < comparators we use the inverse ratio. info
// severity always pins to P3.
//
// Reason string is the FIRST trigger that fired — so the
// tooltip surfaces the most relevant signal (e.g. "deploy 4m
// ago" beats "1.8x threshold" when both apply, because the
// former is the more actionable correlate).
func EnrichProblemsWithPriority(problems []Problem) []Problem {
	// v0.9.838 — vida BİR KEZ okunuyor (atomic load, CH değil) ve tüm
	// dilime aynı config uygulanıyor: aynı okumanın satırları farklı
	// merdivenlerden geçemez. Bildirim hattı (notify/alert_title.go)
	// bu fonksiyonu çağırdığı için kopya kural YOK — aynı vida.
	cfg := CurrentProblemPriority()
	now := time.Now().UnixNano()
	for i := range problems {
		p := &problems[i]
		p.Priority, p.PriorityReason = computePriority(*p, now, cfg)
	}
	return problems
}

// computePriority SAF kalır: config parametre olarak gelir, global
// okumaz. Test edilebilirliğin yanında asıl sebep bu — bildirim hattı
// ile okuma hattı aynı fonksiyonu aynı girdilerle çağırıyor, yani
// e-postadaki P1 ile ekrandaki P1 aynı hesabın sonucu.
func computePriority(p Problem, nowNs int64, cfg ProblemPriorityConfig) (string, string) {
	// Kelepçe BURADA da uygulanıyor: elle kurulmuş bir config ile
	// çağıran bir test/çağıran, BigBreachRatio 0'da `ratio >= 0`
	// yüzünden HER problemi büyük ihlal yapardı.
	cfg = NormalizeProblemPriority(cfg)
	sev := p.Severity
	if sev == "info" {
		return "P3", "info"
	}

	// v0.9.1194 — FIRTINA tanım gereği P1 (operatör-bildirimli + spec
	// onayı). Genel merdiven burada YANLIŞ cevap verir: Value=9 servis /
	// Threshold=5 eşik → 1.8× < BigBreachRatio(2.0) → P2 çıkardı. Oysa
	// dokuz servisin eş-zamanlı patlaması "şimdi"nin ta kendisi — dedektör
	// zaten eşiği (StormMinServices) geçmeden bu satırı hiç açmıyor, yani
	// satırın VARLIĞI ihlalin kanıtı; üstüne bir de oran kapısı koymak
	// aynı eşiği iki kez, ikincisinde daha katı sormak olurdu. Gerekçe
	// sayıları taşır ki operatör vidayı (exception_triage) görebilsin.
	if p.RuleID == "exception-storm" {
		return "P1", fmt.Sprintf("fırtına: %.0f servis (eşik %.0f)", p.Value, p.Threshold)
	}

	// Breach magnitude. If threshold is 0 we can't compute a
	// ratio — fall back to severity alone. v0.8.321 — the FLIPPED
	// ratio also feeds the reason strings below: they used to
	// re-derive the raw Value/Threshold, so a "<" rule (uptime 40
	// vs threshold 99) correctly ranked P1 but told the operator
	// "critical + 0.4x threshold" instead of ~2.5x.
	bigBreach := false
	// totalLoss — "<" sınıfı bir kuralda değer TAM SIFIR (v0.9.825).
	// Ters çevrilecek bir oran yok; ihlal sonsuz. Ayrı bir bayrak,
	// çünkü gerekçe metni de ayrı olmalı: "%.1fx threshold" burada
	// ya +Inf ya da (eski hâlde) 0.0x yazardı.
	totalLoss := false
	ratio := 0.0
	if p.Threshold != 0 {
		ratio = p.Value / p.Threshold
		// v0.9.976 (operatör-raporlu SAHTE P1) — ters çevirme artık
		// kuralın COMPARATOR'ına bakıyor.
		//
		// Kapı `ratio < 1 && ratio > 0` idi ve yorumu "'<' kuralları için"
		// diyordu, ama kuralın comparator'ı HİÇ okunmuyordu — Problem o
		// alanı taşımıyordu bile. Sonuç: eşiğin ALTINDA kalan her ">"
		// problemi ters çevrilip "büyük ihlal" sayılıyordu.
		//
		// Canlı vaka: sms-service error_rate 3.922, eşik 15 (">" kuralı).
		// Oran 0.26 → 1/0.26 = 3.82× → critical + büyük ihlal → P1.
		// Halbuki değer eşiğin ÜÇTE BİRİ; problem kapanmaya yakın.
		// Ölçüm (5717 satır / 11 gün): oran kaynaklı critical P1'lerin
		// 299'u ters çevrilmişti, 60'ı SAF SAPMA (error_rate + db_p99 +
		// http_p99 — hepsi ">" ailesinden).
		//
		// BOŞ comparator'da ÇEVİRMİYORUZ (ölçülmüş karar): bu veride tek
		// bir "<" kural yok, yani boşta çevirmek yalnız sahte P1 üretir.
		// Eski satırlar + comparator taşımayan üreticiler (anomali,
		// monitor, runtime, db kapasitesi, exception) hep ">" ailesi.
		if isBelowRule(p.Comparator) && ratio < 1 && ratio > 0 {
			ratio = 1 / ratio
		}
		if ratio >= cfg.BigBreachRatio {
			bigBreach = true
		}
	}

	// v0.9.825 (operatör-raporlu) — MONITOR DOWN KALICI P2'YE KİLİTLİYDİ.
	//
	// Yukarıdaki ters-çevirme `ratio < 1 && ratio > 0` diyor. TAM SIFIR
	// bu aralığın DIŞINDA: 0 > 0 yanlış. Yani ratio 0'da kalıyor,
	// `ratio >= 2` hiç tutmuyor ve bigBreach false oluyordu.
	//
	// Sonuç: monitor DOWN problemi (runner.go — Value 0, Threshold 1,
	// critical) bigBreach OLAMIYOR ve "default: P2" dalına düşüyordu.
	// Bir servis TAMAMEN ERİŞİLEMEZ durumdayken 4 saat boyunca P2
	// kalıyor, ancak stale-critical kuralıyla P1'e çıkıyordu — yani
	// öncelik listesinde "%50 yavaşlamış" bir servisin ALTINDA.
	//
	// Aynı sınıf her "<" kuralında var: uptime 0/99, başarı oranı 0/95,
	// sağlıklı-pod 0/3. Sıfır, o kural ailesinde ihlalin EN AĞIR hâli —
	// en hafifi gibi sıralanıyordu.
	//
	// Threshold > 0 şartı bilinçli: negatif eşikli bir ">" kuralında
	// (value 0, threshold -5) oran zaten ihlal değildir ve sıfır orada
	// "tam kayıp" anlamına gelmez.
	//
	// SÜRÜM 4'ün ÖN KOŞULU: minPriority=P1 olan bir kanal, bu düzeltme
	// olmadan DOWN bildirimlerini süzüp atardı.
	if p.Value == 0 && p.Threshold > 0 {
		bigBreach = true
		totalLoss = true
	}

	// v0.9.612 (operatör kararı): TAZE DEPLOY artık öncelik BELİRLEMİYOR.
	//
	// Önceki kural "critical + son 5 dk içinde deploy → P1" idi. Prod'da
	// deploy sıklığı yüksek olduğu için bu tetikleyici sürekli ateşliyor
	// ve P1 kavramını sulandırıyordu: her dağıtım penceresinde açılan
	// kritik problemler otomatik P1 oluyordu.
	//
	// DEPLOY BİLGİSİ KAYBOLMUYOR — yalnız ÖNCELİĞE karışmıyor.
	// RecentDeploy zenginleştirmesi (v0.9.552-554) duruyor ve
	// ProblemDetail'de DeployBox olarak GÖRÜNÜYOR. Ayrım bilinçli:
	// bilgi vermek ile sıraya sokmak farklı işler. Operatör deploy'u
	// görüp kendi bağlantısını kurar; sistem onun yerine karar vermez.
	//
	// Geriye kalan P1 kapıları: 2× eşik ihlali, ve 4+ saat açık kalmış
	// kritik. İkisi de problemin KENDİ şiddetinden türüyor, yakınındaki
	// bir olaydan değil.

	// Stale-critical: still open after N hours of operator inactivity.
	// v0.9.838 — N ayarlanabilir; 0 terfiyi TAMAMEN kapatır ("uzun süre
	// açık" = ilgilenilmedi, "şu an daha acil" değil — operatörün P1
	// selini kesmek için isteyebileceği ilk vida).
	openHours := float64(nowNs-p.StartedAt) / float64(time.Hour)
	staleCritical := cfg.StaleCriticalHours > 0 &&
		p.Status != "resolved" && openHours >= cfg.StaleCriticalHours

	// breachReason — ihlal büyüklüğünün operatöre görünen hâli. Tam
	// kayıpta oran yazmak yanlış olurdu (0.0x ya da +Inf); "tamamen
	// kayıp" ihlalin ne olduğunu doğrudan söylüyor.
	breachReason := func(prefix string) string {
		if totalLoss {
			return prefix + " + tamamen kayıp (0/" + trimFloat(p.Threshold) + ")"
		}
		return fmt.Sprintf("%s + %.1fx threshold", prefix, ratio)
	}

	if sev == "critical" {
		switch {
		case bigBreach:
			return "P1", breachReason("critical")
		case staleCritical:
			return "P1", fmt.Sprintf("critical open %.1fh", openHours)
		default:
			return "P2", "critical"
		}
	}

	// severity = warning
	switch {
	case bigBreach:
		return "P2", breachReason("warning")
	default:
		return "P3", "warning steady"
	}
}

// MarkResolved — bir problemi kapatır: Status + ResolvedAt yazılır,
// İHLAL DEĞERİNE (Value) DOKUNULMAZ.
//
// v0.9.977 (operatör-raporlu sahte P1'lerin ikinci yarısı) — altı kapanış
// yolu (kural, sayım-alarmı, anomali, db kapasitesi, runtime, SLO burn)
// kapanış anında `open.Value = <toparlanmış değer>` yazıyordu. Sonuçları:
//
//  1. İHLAL KAYBOLUYORDU. Satır zaten "resolved" ve ResolvedAt damgalı;
//     kapanış anındaki değeri Value'ya yazmak, problemin NEDEN açıldığını
//     anlatan tek sayıyı siliyordu. Post-mortem'de "error_rate 22 idi,
//     eşik 15" yerine "error_rate 3.9, eşik 15" görünüyordu — yani
//     problemin hiç ihlal etmediği gibi bir tablo.
//  2. ÖNCELİK ASİMETRİSİ. Priority okuma anında hesaplanıyor: aynı satır
//     açılışta P1, kapanışta bambaşka bir basamak oluyordu. v0.9.976
//     öncesinde bu, ters-çevirme hatasıyla birleşip kapanmış problemleri
//     SAHTE P1'e çeviriyordu (değer eşiğin altına indiği an oran <1 olur,
//     çevrilir, "büyük ihlal" sayılırdı).
//
// Kapanış anındaki değer için AYRI bir alan AÇILMADI: ResolvedAt zaten
// var, açıklama metinleri (appendResolveSuffix / anomali desc) toparlanma
// bilgisini taşıyor ve yeni bir kolon, kimsenin okumadığı bir sayı için
// şema borcu olurdu.
//
// SAF — tablo testli. Problem kapatan HER yol buradan geçmeli (pin:
// evaluator/resolve_value_test.go).
func MarkResolved(p *Problem, resolvedAtNs int64) {
	if p == nil {
		return
	}
	p.Status = "resolved"
	p.ResolvedAt = &resolvedAtNs
}

// isBelowRule — comparator "değer DÜŞTÜKÇE kötüleşen" aileden mi?
//
// SAF + tablo-testli. Yalnız iki dizgi ters çevirmeyi açar; boşluk
// toleranslı çünkü comparator operatörün kural formundan geliyor ve
// " < " gibi bir değer CH'ye aynen yazılabilir. Bilinmeyen/boş değer
// "hayır" der — v0.9.976'nın merkezi kararı: emin değilsek ÇEVİRMEYİZ,
// çünkü yanlış çevirme sahte P1 üretiyor, çevirmemek yalnız gerçek bir
// "<" ihlalini bir basamak aşağı koyuyor.
func isBelowRule(comparator string) bool {
	switch strings.TrimSpace(comparator) {
	case "<", "<=":
		return true
	}
	return false
}

// trimFloat — eşiği gereksiz sıfırlar olmadan yazar (1 → "1",
// 99.5 → "99.5"). Gerekçe metni operatörün gözüyle okunuyor; "0/1.00"
// gürültüdür.
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// EnrichProblemsWithClusters fills each problem's Clusters
// field from the recent service-to-cluster map. One batch
// query covers every problem in the slice. Soft-fails: if
// the lookup errors we return the slice unchanged rather
// than blocking the page on a transient CH blip.
func (s *Store) EnrichProblemsWithClusters(ctx context.Context, problems []Problem, since time.Duration) []Problem {
	if len(problems) == 0 {
		return problems
	}
	m, err := s.GetServiceClusterMap(ctx, since)
	if err != nil {
		return problems
	}
	for i := range problems {
		if cs, ok := m[problems[i].Service]; ok && len(cs) > 0 {
			problems[i].Clusters = cs
		}
	}
	return problems
}

// EnrichIncidentsWithClusters mirrors EnrichProblemsWithClusters
// for the Incidents list. Same single-batch lookup, same soft-
// fail behaviour.
func (s *Store) EnrichIncidentsWithClusters(ctx context.Context, incidents []Incident, since time.Duration) []Incident {
	if len(incidents) == 0 {
		return incidents
	}
	m, err := s.GetServiceClusterMap(ctx, since)
	if err != nil {
		return incidents
	}
	for i := range incidents {
		if cs, ok := m[incidents[i].Service]; ok && len(cs) > 0 {
			incidents[i].Clusters = cs
		}
	}
	return incidents
}

// EnrichAnomaliesWithClusters mirrors the pair above for
// AnomalyEvent. Same one-shot map lookup keyed by service.
func (s *Store) EnrichAnomaliesWithClusters(ctx context.Context, events []AnomalyEvent, since time.Duration) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	m, err := s.GetServiceClusterMap(ctx, since)
	if err != nil {
		return events
	}
	for i := range events {
		if cs, ok := m[events[i].Service]; ok && len(cs) > 0 {
			events[i].Clusters = cs
		}
	}
	return events
}

// deploysCacheEntry is one cached fetchDeploysByService result
// (v0.8.359). The deploy list per service is READ-ONLY once cached —
// the enrich matching loop only walks it.
type deploysCacheEntry struct {
	at        time.Time
	byService map[string][]spanDeploy
}

// EnrichProblemsWithRunbooks resolves each problem's
// RunbookURL from (a) the alert rule that fired or (b) the
// service catalog metadata as a fallback. Two single-shot
// queries (alert_rules + service_metadata) joined in-memory
// against the problems slice — N+1 free regardless of the
// problem count. Safe to call on resolved problems too;
// the URL is contextual to the rule / service, not the
// status.
func (s *Store) EnrichProblemsWithRunbooks(ctx context.Context, problems []Problem) []Problem {
	if len(problems) == 0 {
		return problems
	}
	// Alert rule runbooks — keyed by rule id.
	ruleBooks := map[string]string{}
	if rules, err := s.ListAlertRules(ctx); err == nil {
		for _, r := range rules {
			if r.RunbookURL != "" {
				ruleBooks[r.ID] = r.RunbookURL
			}
		}
	}
	// Service catalog runbooks — keyed by service name.
	svcBooks := map[string]string{}
	if mds, err := s.ListServiceMetadata(ctx); err == nil {
		for svc, md := range mds {
			if md.RunbookURL != "" {
				svcBooks[svc] = md.RunbookURL
			}
		}
	}
	for i := range problems {
		if u, ok := ruleBooks[problems[i].RuleID]; ok {
			problems[i].RunbookURL = u
			continue
		}
		if u, ok := selfHealthRunbooks[problems[i].RuleID]; ok {
			problems[i].RunbookURL = u
			continue
		}
		if u, ok := svcBooks[problems[i].Service]; ok {
			problems[i].RunbookURL = u
		}
	}
	return problems
}

// selfHealthRunbooks — v0.9.1279. Self-health kuralları (`self-*`)
// SENTETİKTİR: alert_rules'ta satırları yoktur, dolayısıyla operatörün
// runbook_url alanı da yoktur ve yukarıdaki ruleBooks onları asla
// bulamaz. Oysa müdahale adımları ÜRÜNÜN İÇİNDE: spool runbook'u
// v0.9.1191'de /admin/stats'taki spool kartına girdi (gönderici başlat /
// zorla boşalt düğmeleri), disk doluluğu aynı sayfanın 0. adımı,
// kanallar Settings → Channels.
//
// Sıra bilinçli: operatörün alert_rules'a elle koyduğu bir URL bu
// gömülü haritayı EZER (yukarıdaki dal önce koşar). Gömülü harita bir
// varsayılan, bir kilit değil.
//
// URL'ler ÜRÜN İÇİ ve gerçek rotalar (kontrol edildi: /settings
// path-param'lı — `/settings/channels`, query değil; /admin/stats'ta
// çapa id'si YOK, o yüzden uydurma bir `#spool` fragment'i yazılmadı —
// çalışmayan bir bağlantı, bağlantı olmamasından kötüdür).
var selfHealthRunbooks = map[string]string{
	"self-spool-depth":    "/admin/stats",
	"self-disk-eta":       "/admin/stats",
	"self-ingest-stall":   "/admin/stats",
	"self-channel-broken": "/settings/channels",
	// v0.9.1294 — hacim sıçramasının müdahale ekranı kardinalite
	// panelidir: hangi servis/metrik/attr anahtarı yazma yolunu yiyor
	// sorusunun cevabı orada. DOĞRUDAN rota yazıldı (/system/cardinality);
	// üstteki /admin/* biçimleri eski yer imleri için duran bir
	// yönlendirme (App.tsx AdminRedirect) ve yeni bir girdinin araya bir
	// atlama koyması için sebep yok.
	"self-volume-spike": "/system/cardinality",
}

// EnrichProblemsWithTeams attaches each problem's owning team
// (OwnerTeam) + reliability team (SRETeam) from the service
// catalog. One batch ListServiceMetadata lookup covers every
// problem in the slice — N+1 free, the same shape as the Clusters
// / Runbooks enrichers. Read-time only: team ownership lives on
// the operator-curated catalog, NOT the problems row, so a catalog
// edit reflects on the next refresh without rewriting history.
// Soft-fails: a catalog read error (or empty catalog) returns the
// slice unchanged rather than blanking the team chips on a
// transient CH blip. (v0.8.290 — powers the owner/SRE team filters
// on /problems, mirroring the inbox enrichment so the two pages
// agree on which team owns a firing service.)
// v0.9.1345 — db-konulu problemler (kind=db) bu eşleşmeden HİÇ
// geçemiyordu: `service` alanları bir DBSubjectID
// (`db:oracle@corebank-scan.prod`) ve kataloğun anahtarı servis adı, yani
// mds[...] her zaman ıskalıyordu. Sonuç sessiz: satır takımsız geliyor,
// /problems takım filtresi onu hiçbir takıma göstermiyor ve ekip-maili
// alıcı çözemiyor (v0.9.1344'ün ölçtüğü `unmatched` kusurunun yarısı).
//
// Sahiplik artık operatörün kuralıyla türetiliyor: "bir db öznesinin
// sahibi, onu EN ÇOK ÇAĞIRAN servisin takımıdır" (db_ownership.go, orada
// kapsam/pencere/yaklaşıklık gerekçeleriyle). Türetimin KANITI TeamsVia
// alanında satırla birlikte gidiyor.
//
// Çağıran anlık görüntüsü döngünün DIŞINDA, yalnız gerçekten db konusu
// varsa alınıyor: db-konulu problemi olmayan bir kurulum bu okumanın
// bedelini HİÇ ödemez.
func (s *Store) EnrichProblemsWithTeams(ctx context.Context, problems []Problem) []Problem {
	if len(problems) == 0 {
		return problems
	}
	mds, err := s.ListServiceMetadata(ctx)
	if err != nil || len(mds) == 0 {
		return problems
	}
	var dbCallers map[string][]DBCaller
	if anyDBSubject(problems) {
		// Soft-fail, kardeş zenginleştiricilerle aynı: okuma düşerse
		// db satırları BUGÜNKÜ hâlinde (takımsız) kalır, servis
		// satırları etkilenmez.
		dbCallers, _ = s.DBCallersBySystem(ctx)
	}
	for i := range problems {
		if ProblemSubjectKind(problems[i].Kind) == ProblemKindDB {
			system, _, ok := ParseDBSubjectID(problems[i].Service)
			if !ok || len(dbCallers[system]) == 0 {
				continue
			}
			own, ok := ResolveDBOwner(dbCallers[system], mds)
			if !ok {
				continue
			}
			problems[i].OwnerTeam = own.OwnerTeam
			problems[i].SRETeam = own.SRETeam
			problems[i].TeamsVia = own.Caller
			continue
		}
		if md, ok := mds[problems[i].Service]; ok {
			problems[i].OwnerTeam = md.OwnerTeam
			problems[i].SRETeam = md.SRETeam
		}
	}
	return problems
}

// anyDBSubject — dilimde en az bir db konusu var mı. Ayrı fonksiyon
// çünkü tek işi filo-geneli okumayı GEREKSİZ yere tetiklememek ve bu
// koşul satır-içi yazıldığında ilk refaktörde kaybolur.
func anyDBSubject(problems []Problem) bool {
	for i := range problems {
		if ProblemSubjectKind(problems[i].Kind) == ProblemKindDB {
			return true
		}
	}
	return false
}

// ── Alert rules ───────────────────────────────────────────────────────────────

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	// Serve from the in-process cache when fresh (v0.8.x — load-test
	// finding: this FINAL scan ran 1000+×/4min). The cached slice is
	// REPLACED, never mutated in place, so a reader holding the old
	// snapshot stays consistent without copying.
	s.alertRulesMu.RLock()
	if s.alertRulesVal != nil && time.Since(s.alertRulesAt) < alertRulesCacheTTL {
		out := s.alertRulesVal
		s.alertRulesMu.RUnlock()
		return out, nil
	}
	s.alertRulesMu.RUnlock()

	rows, err := s.conn.Query(ctx, `
		SELECT id, name, service, metric, comparator, threshold, window_sec,
		       severity, enabled, built_in, runbook_url, for_sec, min_samples,
		       cooldown_sec, log_query, watcher_json, `+s.alertRuleTargetSelect()+`, toUnixTimestamp64Nano(created_at)
		FROM alert_rules FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var enabled, builtIn uint8
		var targetJSON string
		if err := rows.Scan(&r.ID, &r.Name, &r.Service, &r.Metric, &r.Comparator,
			&r.Threshold, &r.WindowSec, &r.Severity, &enabled, &builtIn,
			&r.RunbookURL, &r.ForSec, &r.MinSamples, &r.CooldownSec,
			&r.LogQuery, &r.WatcherJSON, &targetJSON, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Target = decodeRuleTarget(targetJSON)
		r.Enabled = enabled == 1
		r.BuiltIn = builtIn == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// out can be nil on an empty table; store an empty non-nil slice so the
	// freshness check above still treats it as cached (not a cold miss).
	cached := out
	if cached == nil {
		cached = []AlertRule{}
	}
	s.alertRulesMu.Lock()
	s.alertRulesVal = cached
	s.alertRulesAt = time.Now()
	s.alertRulesMu.Unlock()
	return out, nil
}

// InvalidateAlertRulesCache — exported wrapper (v0.9.196): the watcher
// history endpoint's 404 miss-path drops this pod's 30s rule cache once
// before answering "not found", so a fresh import served by ANOTHER pod
// doesn't 404 here for up to a cache TTL.
func (s *Store) InvalidateAlertRulesCache() { s.invalidateAlertRules() }

// invalidateAlertRules clears the cache so the next ListAlertRules re-reads
// after a write (Upsert / Delete / SetEnabled). Cheap; called off the
// operator-action path, not the hot read path.
func (s *Store) invalidateAlertRules() {
	s.alertRulesMu.Lock()
	s.alertRulesVal = nil
	s.alertRulesMu.Unlock()
}

func (s *Store) UpsertAlertRule(ctx context.Context, r AlertRule) error {
	enabled := uint8(0)
	if r.Enabled {
		enabled = 1
	}
	builtIn := uint8(0)
	if r.BuiltIn {
		builtIn = 1
	}
	// Explicit column list — alert_rules grew runbook_url in
	// a later migration. Without naming the columns, the
	// ordinal arg shape would mismatch the table layout on
	// upgraded installs.
	// v0.10.331 — hedefli kural: kolon yoksa (küme kipi, ertelenmiş DDL) kayıt
	// REDDEDİLİR; hedefsiz kurallar eski INSERT şekliyle yazılmaya devam eder.
	// v0.10.334 — kolon boot probunda yoktu ama ertelenmiş DDL bu arada
	// inmiş olabilir: reddetmeden önce BİR kez yeniden probe (tek restart).
	if r.Target != nil && !s.hasAlertRuleTargetCol.Load() && !s.probeAlertRuleTargetCol(ctx) {
		return ErrRuleTargetColumnMissing
	}
	cols := `(id, name, service, metric, comparator, threshold, window_sec,
		 severity, enabled, built_in, runbook_url, for_sec, min_samples,
		 cooldown_sec, log_query, watcher_json, created_at, version)`
	if s.hasAlertRuleTargetCol.Load() {
		cols = `(id, name, service, metric, comparator, threshold, window_sec,
		 severity, enabled, built_in, runbook_url, for_sec, min_samples,
		 cooldown_sec, log_query, watcher_json, target_json, created_at, version)`
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO alert_rules `+cols)
	if err != nil {
		return err
	}
	args := []any{r.ID, r.Name, r.Service, r.Metric, r.Comparator,
		r.Threshold, r.WindowSec, r.Severity, enabled, builtIn,
		r.RunbookURL, r.ForSec, r.MinSamples, r.CooldownSec, r.LogQuery,
		r.WatcherJSON}
	if s.hasAlertRuleTargetCol.Load() {
		args = append(args, encodeRuleTarget(r.Target))
	}
	args = append(args, time.Now().UTC(), uint64(time.Now().UnixNano()))
	if err := batch.Append(args...); err != nil {
		return err
	}
	if err := batch.Send(); err != nil {
		return err
	}
	s.invalidateAlertRules()
	return nil
}

// DeleteAlertRule removes the row entirely (ALTER … DELETE)
// rather than the previous soft-disable pattern — operators
// hitting Delete expect the rule to go AWAY from the list, not
// stay around as a disabled tombstone. Matches the precedent
// already established by monitors / status-page components /
// notification channels. Built-in rules are still resurrected
// on next boot from the seed list, so deleting a preset gives
// the operator a clean slate without a permanent "ghost" row.
// Disable-without-delete remains available through
// SetAlertRuleEnabled (toggled by the noisy-rules bulk path).
func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	if err := s.conn.Exec(ctx, `ALTER TABLE alert_rules DELETE WHERE id = ?`, id); err != nil {
		return err
	}
	s.invalidateAlertRules()
	return nil
}

// SetAlertRuleEnabled flips a rule's enabled flag. Used by both the
// disable (DELETE) and re-enable endpoints.
func (s *Store) SetAlertRuleEnabled(ctx context.Context, id string, enabled bool) error {
	r, err := s.GetAlertRule(ctx, id)
	if err != nil {
		return err
	}
	r.Enabled = enabled
	return s.UpsertAlertRule(ctx, *r)
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (*AlertRule, error) {
	var r AlertRule
	var enabled, builtIn uint8
	var targetJSON string
	err := s.conn.QueryRow(ctx, `
		SELECT id, name, service, metric, comparator, threshold, window_sec,
		       severity, enabled, built_in, runbook_url, for_sec, min_samples,
		       cooldown_sec, log_query, watcher_json, `+s.alertRuleTargetSelect()+`, toUnixTimestamp64Nano(created_at)
		FROM alert_rules FINAL WHERE id = ? LIMIT 1`, id).
		Scan(&r.ID, &r.Name, &r.Service, &r.Metric, &r.Comparator, &r.Threshold,
			&r.WindowSec, &r.Severity, &enabled, &builtIn,
			&r.RunbookURL, &r.ForSec, &r.MinSamples, &r.CooldownSec,
			&r.LogQuery, &r.WatcherJSON, &targetJSON, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	r.Target = decodeRuleTarget(targetJSON)
	r.Enabled = enabled == 1
	r.BuiltIn = builtIn == 1
	return &r, nil
}

// ── Problems ─────────────────────────────────────────────────────────────────

type ProblemFilter struct {
	Status string // "open" | "resolved" | ""
	// NotStatuses — statuses to EXCLUDE, applied in SQL so the narrow bites
	// BEFORE the LIMIT. Status (singular) stays for callers that want exactly
	// one bucket; the two AND together if both are set.
	//
	// v0.9.322 — deliberately an EXCLUSION, not an allow-list. The inbox's Go
	// keepers are written as "anything that isn't resolved still needs a
	// human", so a row with an unrecognised (or empty) status survives on
	// purpose. An allow-list in SQL would contradict that and silently drop
	// exactly those rows — and, worse, drop them from the LIST while the
	// badge still counted them.
	NotStatuses []string
	Service     string
	Severity    string
	// RuleIDPrefix narrows to rules whose id starts with the given
	// string — used by the Anomalies page to surface only the
	// anomaly-detector entries (rule_id = "anomaly:…") and skip
	// the rule-driven Problems.
	RuleIDPrefix string
	// RuleID narrows to the problems of ONE rule, exactly (v0.9.196 —
	// the /watchers history drawer: fire/resolve timeline of a single
	// imported watcher rule). Exact match, unlike RuleIDPrefix.
	RuleID string
	// Priority alanı v0.9.583'te KALDIRILDI.
	//
	// Öncelik okuma anında hesaplanır (v0.5.210), CH kolonu değildir —
	// yani SQL'e İNEMEZ. Alan yıllarca burada durdu ama ListProblems
	// gövdesi ona HİÇ bakmadı: sadece "Go'da daraltmayı unutma" notuydu.
	//
	// Bir filtre struct'ında uygulanmayan bir alan tutmak, bir tuzaktır.
	// İki kez ısırdı:
	//   v0.9.342 — buradaki yorum "ListProblems Limit'i bu filtreden
	//              SONRA uygular" diyordu; hiç uygulamadı.
	//   v0.9.576 — MCP list_problems alanı doldurdu, daraltmayı unuttu;
	//              priority=P1 istemek filoda yüzlerce P1 varken SIFIR
	//              sonuç döndürebiliyordu.
	//
	// Artık çağıran AÇIKÇA iki adımı yazıyor ve ikisi de görünür:
	//   f.Limit = chstore.ProblemScanLimit(page, true)   // taramayı aç
	//   rows = chstore.FilterProblemsByPriority(rows, prios)  // daralt
	//
	// Alan olmayınca yanlışlıkla güvenilemez.
	// IDs constrains the result to these problem ids (id IN (…)), applied in
	// SQL. v0.9.343 — the incident explain path resolved attached problems by
	// paging the newest 2000 and keeping the subset it wanted, so an older
	// attached problem silently dropped out of the prompt. Nil = no
	// constraint; empty behaves like Services (constrain to nothing).
	IDs []string
	// Services constrains the result to this set (service IN (…)), applied in
	// SQL so it bites BEFORE the LIMIT. v0.9.342 — the owner/SRE team and
	// cluster filters used to run in Go on the already-capped page; both
	// resolve to a service set up front, so they belong here. Same shape as
	// ExceptionGroupFilter.Services. Nil = no constraint; EMPTY = constrain to
	// nothing (a team with no members must return an empty page, never an
	// unfiltered one).
	Services []string
	// ServicesAllowDBSubjects (v0.9.1345) — Services daraltmasına TEK
	// istisna: db özneli satırlar listede olmasalar da GEÇER.
	//
	// NEDEN: Services listesi katalogdan SERVİS ADLARIYLA kuruluyor
	// (servicesForTeam). Bir db öznesinin `service` alanı ise bir
	// DBSubjectID (`db:oracle@corebank-scan.prod`) ve o listede ASLA
	// olamaz. v0.9.1345 db problemlerine sahiplik verdi; bu bayrak
	// olmadan ürün KENDİ KENDİYLE ÇELİŞİRDİ: satırın çipi
	// "core-banking" yazarken, owner=core-banking süzgeci o satırı
	// gizlerdi — üstelik sessizce.
	//
	// Kesin eşleştirme Go tarafında (matchesTeamFilter, zenginleştirilmiş
	// OwnerTeam/SRETeam üzerinde) zaten yapılıyor. Bu bayrak yalnız SQL
	// daraltmasını gevşetiyor, yani db satırları Go kapısına ULAŞABİLSİN
	// diye. Servis tarafının daraltması AYNEN duruyor (v0.9.342'nin perf
	// kazancı korunuyor) ve db satırları az sayıda — özne başına bir
	// kapasite alarmı.
	ServicesAllowDBSubjects bool
	// SubjectKind (v0.9.1342) — ÖZNE TÜRÜ süzgeci: "service" | "db".
	// Boş = kısıt yok.
	//
	// Adı bilerek `Kind` DEĞİL. InboxItem.Kind bu kod tabanında BAŞKA bir
	// şey (satırın KAYNAĞI: problem/exception/anomaly) ve ikisi de string;
	// v0.9.1339'da tam bu çakışma bir bug üretti, çözümü de ayrı bir ada
	// (subjectKind) taşımaktı. Aynı ayrım burada da korunuyor.
	//
	// SQL'e İNER — ve inmesi şart: /inbox'ın db şeridi bir
	// `WHERE kind = 'db'` sorgusudur. Go tarafında yeniden normalize
	// etmek (ProblemSubjectKind ile) bu kapıyı LIMIT'ten sonraya taşır ve
	// v0.9.322 sınıfını geri getirir. Geçmiş satırların doğru şeritte
	// kalmasını CH'nin `DEFAULT 'service'`i garanti eder —
	// TestProblemKindColumnDefaultsToService o garantiyi pinler.
	SubjectKind string
	// Env (v0.8.387 — env-separation Phase 3) narrows to problems
	// whose SERVICE ran in the given deployment environment within
	// the last hour, per the 60s-cached service→env map. Problems
	// carry no env dimension (state table keyed rule+service, values
	// computed over all-env metrics), so this is the only honest env
	// semantics — see env_members.go. Applied in SQL (service IN …)
	// by ListProblems AND CountProblems so list / count / buckets /
	// sidebar badge agree, and Limit bites AFTER the env narrowing.
	// service='' (global log-query) rows always survive. Empty = off.
	Env   string
	Limit int
}

// CountProblems returns the row count matching the same filter
// shape ListProblems uses, without materialising the actual rows.
// Drives the sidebar badge so the count stays truthful when there
// are >200 open problems — the badge previously fetched 200 rows
// and counted the array, capping the displayed value at 200.
// FINAL on the spans is the same as the list path so the merged
// dedup result is what counts; using a plain count() would
// double-count rows mid-merge.
// CountOpenProblemsByRule — /alerts satırındaki "açık problem N"
// rozeti (v0.9.1109, Faz 5 "alarm→veri yolu"). Kural başına açık
// problem sayısı; problems state tablosu küçük, tek FINAL tarama.
// Kurala eşleşmeyen rule_id'ler (anomaly:*, boş) haritada durur ama
// UI yalnız kendi kural id'lerini okur — fazlalık zararsız.
func (s *Store) CountOpenProblemsByRule(ctx context.Context) (map[string]uint64, error) {
	rows, err := s.conn.Query(ctx, `SELECT rule_id, count() AS n
		FROM problems FINAL
		WHERE status = 'open'
		GROUP BY rule_id
		LIMIT 5000
		SETTINGS max_execution_time = 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]uint64)
	for rows.Next() {
		var id string
		var n uint64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// CountProblemsNotInStatuses — inbox rozetinin "hâlâ insan bekleyen" toplamı.
// v0.9.322: izin listesi yerine DIŞLAMA — liste tarafındaki Go süzgeciyle
// birebir aynı sınıflandırma, yoksa rozet ve liste ayrışıyor.
// TEK FINAL taramasında (v0.8.472 perf dalga-1 #2; önceden iki ayrı
// CountProblems çağrısıydı). Statüler sabit enum, IN bind'li.
// envServices scopes the count to an environment, mirroring the inbox
// list's EnvScopeKeepsRow exactly: a service-LESS (global) row always
// counts, a db-SUBJECT row always counts (v0.9.1358), otherwise the
// service must be an env member. nil = no env constraint;
// a non-nil EMPTY slice means the env resolved to no services, so only
// global (+ db) rows count — the nil-vs-empty distinction is load-bearing
// here the same way it is for the team filter (v0.9.219).
//
// v0.9.1358 — WHERE gövdesi problemCountWhere'e taşındı. Burada elle
// yazılmış bir env SQL kopyası duruyordu; /problems'ın WHERE'i db kaçış
// kapısını alırken bu kopya eski hâlinde kalsaydı, operatörün gördüğü
// ilk şey listenin gösterdiği satırı saymayan bir rozet olurdu.
func (s *Store) CountProblemsNotInStatuses(ctx context.Context, exclude []string, envServices []string) (uint64, error) {
	whereSQL, args := s.problemCountWhere(exclude, envServices)
	row := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM problems FINAL
		WHERE `+whereSQL+`
		SETTINGS max_execution_time = 5`, args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) CountProblems(ctx context.Context, f ProblemFilter) (uint64, error) {
	var wc whereClause
	if f.Status != "" {
		wc.add("status = ?", f.Status)
	}
	if f.Service != "" {
		wc.add("service = ?", f.Service)
	}
	if f.Severity != "" {
		wc.add("severity = ?", f.Severity)
	}
	if f.RuleIDPrefix != "" {
		wc.add("startsWith(rule_id, ?)", f.RuleIDPrefix)
	}
	if f.RuleID != "" {
		wc.add("rule_id = ?", f.RuleID)
	}
	s.envScopeProblems(ctx, &wc, f.Env) // v0.8.387 — same conjunct as ListProblems, badge agrees
	row := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM problems FINAL `+wc.sql()+`
		SETTINGS max_execution_time = 5`, wc.args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// problemSelectExpr — problems satırının TAM okuma listesi, TEK kaynak.
//
// v0.9.976 — yedi okuma yeri (List/Get/FindOpen/FindOpenByID/Snapshot/
// Stale/SimilarResolved) aynı listeyi elle tekrarlıyordu; comparator'ı
// yedi yere ayrı ayrı eklemek, birini unutunca SESSİZ bir hata sınıfı
// açardı: eksik okuyan yol satırı UpsertProblem'e geri verdiğinde
// (ack, refresh, AI özeti) ReplacingMergeTree bütün-satır replace'i
// comparator'ı DEFAULT ”e indirir — yani öncelik hesabı sessizce eski
// hatalı davranışa döner. users.go'daki userSelectExpr/scanUserRow
// ikilisinin birebir emsali.
//
// comparator YALNIZ store kolonu probe ettiyse listede (hasProblemCmpCol):
// küme kipinde DDL arka plana ERTELENİYOR (v0.9.614), yani yeni binary
// kolon henüz yokken okuma yapabilir. Koşulsuz bir SELECT orada her
// problem okumasını "no such column" ile düşürürdü — v0.8.185/186'nın
// prod'u iki kez kıran sınıfı, bu kez okuma tarafında.
// problemCols — bu Store'un `problems` tablosunda GERÇEKTEN gördüğü
// opsiyonel kolonlar. v0.9.1338'de tanıtıldı çünkü ikinci bir probe'lu
// kolon (kind) gelince eski `scanProblemRow(rows, s.hasProblemCmpCol)` deseni
// SESSİZ bir hata sınıfı açıyordu: iki bool'u yan yana geçiren dokuz
// çağrı yerinden birinde sıra karışsa derleyici SUSAR ve satır yanlış
// kolondan okunur. Tek struct'ta taşımak eşleşmeyi YAPISAL yapıyor —
// projeksiyonu kuran değer ile scan'i kuran değer AYNI değerdir.
type problemCols struct {
	Comparator bool
	Kind       bool
}

func (s *Store) problemCols() problemCols {
	return problemCols{Comparator: s.hasProblemCmpCol, Kind: s.hasProblemKindCol}
}

func (s *Store) problemSelectExpr() string { return problemSelectExprFor(s.problemCols()) }

func problemSelectExprFor(c problemCols) string {
	expr := `id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description, assignee, pod,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at,
		       ai_summary, toUnixTimestamp64Nano(ai_summary_at)`
	if c.Comparator {
		expr += `, comparator`
	}
	// v0.9.1338 — comparator'dan SONRA. Sıra scanProblemRow'un append
	// sırasıyla birebir aynı olmak zorunda (pozisyonel Scan) ve ikisi de
	// AYNI problemCols değerinden türüyor.
	if c.Kind {
		expr += `, kind`
	}
	return expr
}

// scanProblemRow — problemSelectExpr'in ürettiği satırı okur. c,
// projeksiyonu kuran değerle AYNI olmak zorunda (bkz. problemCols).
//
// resolved_at: NULL ve sıfır-zaman ikisi de "çözülmemiş" demek. Eskiden
// FindOpenProblemByID sıfırı eliyordu, diğerleri elemiyordu; tek yerde
// birleşince en muhafazakâr davranış (ikisini de ele) kaldı — sıfır
// zamanlı bir ResolvedAt işaretçisi UI'da 1970 damgası demek olurdu.
//
// Kind HER ZAMAN normalize dönüyor (v0.9.1338): kolon yokken de, eski
// satırda boş gelince de ProblemKindService. Böylece üst katmanların
// hiçbiri "boş mu service mi" ayrımını taşımaz.
func scanProblemRow(sc interface{ Scan(...any) error }, c problemCols) (Problem, error) {
	var p Problem
	var resolvedAt *time.Time
	dst := []any{&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
		&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description,
		&p.Assignee, &p.Pod, &p.StartedAt, &resolvedAt, &p.AISummary, &p.AISummaryAt}
	if c.Comparator {
		dst = append(dst, &p.Comparator)
	}
	if c.Kind {
		dst = append(dst, &p.Kind)
	}
	if err := sc.Scan(dst...); err != nil {
		return Problem{}, err
	}
	if resolvedAt != nil && !resolvedAt.IsZero() {
		ns := resolvedAt.UnixNano()
		p.ResolvedAt = &ns
	}
	p.Kind = ProblemSubjectKind(p.Kind)
	return p, nil
}

// problemInsertCols / problemInsertArgs — problems'e yazan HER yolun tek
// kolon+değer kaynağı.
//
// Neden tek kaynak: bu tabloya yazan iki yol var (UpsertProblem ve
// UpsertProblemAISummary) ve İKİSİ de tarihte kolon unutup üretim verisi
// sildi — pod (v0.9.445) ve ai_summary (v0.9.448). ReplacingMergeTree
// bütün-satır replace yapıyor, yani listede olmayan kolon DEFAULT'a iner.
// Ayrı listeler tutmak bu sınıfı canlı tutuyordu; problem_aisummary_test
// pinleri artık bu iki fonksiyonu doğruluyor.
func problemInsertCols(c problemCols) string {
	cols := "id, rule_id, rule_name, severity, service, metric, value, " +
		"threshold, status, description, assignee, pod, started_at, " +
		"resolved_at, updated_at, version, ai_summary, ai_summary_at"
	if c.Comparator {
		cols += ", comparator"
	}
	// v0.9.1338 — kolon YOKKEN listeye girmiyor, yani o boot'ta yazılan
	// satır CH'nin `DEFAULT 'service'`ini alır. Bir db özneli problem o
	// pencerede bugünkü gibi servis özneli görünür — bilinçli ve GÜVENLİ
	// yön (yeni satırın düşmesi değil, eski anlamını koruması).
	if c.Kind {
		cols += ", kind"
	}
	return cols
}

func problemInsertArgs(p Problem, c problemCols) []any {
	startedAt := time.Unix(0, p.StartedAt).UTC()
	var resolvedAt *time.Time
	if p.ResolvedAt != nil {
		t := time.Unix(0, *p.ResolvedAt).UTC()
		resolvedAt = &t
	}
	now := time.Now()
	args := []any{p.ID, p.RuleID, p.RuleName, p.Severity, p.Service, p.Metric,
		p.Value, p.Threshold, p.Status, p.Description, p.Assignee, p.Pod,
		startedAt, resolvedAt, now.UTC(), uint64(now.UnixNano()),
		p.AISummary, time.Unix(0, p.AISummaryAt).UTC()}
	if c.Comparator {
		args = append(args, p.Comparator)
	}
	// Yazarken de normalize: bir üretici Kind'ı hiç set etmediyse (18
	// üreticinin 17'si) satır açıkça 'service' yazar. Boş yazmak
	// LowCardinality kolonda 'service'ten AYRI üçüncü bir değer olurdu ve
	// ProblemSubjectKind'ı SQL tarafında da tekrarlamak gerekirdi.
	if c.Kind {
		args = append(args, ProblemSubjectKind(p.Kind))
	}
	return args
}

func (s *Store) ListProblems(ctx context.Context, f ProblemFilter) ([]Problem, error) {
	var wc whereClause
	if f.Status != "" {
		wc.add("status = ?", f.Status)
	}
	// v0.9.322 — push the "not resolved" narrow into SQL.
	//
	// The inbox used to pass Status:"" (a single-value column can't express
	// two buckets) and drop resolved rows in Go AFTER the LIMIT. On an install
	// with a long history that is a silent scope bug: ORDER BY started_at DESC
	// LIMIT 300 over a table that is 99% resolved returns ~300 resolved rows
	// and two open ones, so the queue showed 2 while the badge — which did
	// narrow in SQL — said 29. The LIMIT has to apply to the rows that will
	// actually be shown.
	if len(f.NotStatuses) > 0 {
		wc.add("status NOT IN ("+chPlaceholders(len(f.NotStatuses))+")", toAnySlice(f.NotStatuses)...)
	}
	if f.Service != "" {
		wc.add("service = ?", f.Service)
	}
	if f.IDs != nil {
		if len(f.IDs) == 0 {
			wc.add("1 = 0")
		} else {
			wc.add("id IN ("+chPlaceholders(len(f.IDs))+")", toAnySlice(f.IDs)...)
		}
	}
	if f.Services != nil {
		wc.add(problemServicesConjunct(len(f.Services), f.ServicesAllowDBSubjects, s.hasProblemKindCol),
			toAnySlice(f.Services)...)
	}
	if f.Severity != "" {
		wc.add("severity = ?", f.Severity)
	}
	if f.RuleIDPrefix != "" {
		wc.add("startsWith(rule_id, ?)", f.RuleIDPrefix)
	}
	if f.RuleID != "" {
		wc.add("rule_id = ?", f.RuleID)
	}
	// v0.9.1342 — özne türü şeridi, SQL'de (LIMIT'ten ÖNCE).
	if sql, arg, ok := problemSubjectConjunct(f.SubjectKind, s.hasProblemKindCol); ok {
		if arg == nil {
			wc.add(sql)
		} else {
			wc.add(sql, arg)
		}
	}
	s.envScopeProblems(ctx, &wc, f.Env) // v0.8.387 — service-scoped env narrowing (env_members.go)
	if f.Limit == 0 {
		f.Limit = 100
	}

	// v0.5.406 — bound the query at 8s. Without this CH could run
	// the FINAL-merge + ORDER BY started_at scan to completion no
	// matter how long it took; on installs with a deep problems
	// table the read regularly tipped past the browser's 60s
	// fetch timeout, the browser sent an AbortController cancel,
	// and the upstream context cancel surfaced as repeated
	// "context deadline cancelled" lines in coremetry logs.
	// 8s is well under the browser ceiling but plenty for a
	// healthy install — and bails out clearly with a CH error
	// instead of stacking up doomed requests.
	rows, err := s.conn.Query(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL `+wc.sql()+`
		ORDER BY started_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 8`, append(wc.args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Problem
	for rows.Next() {
		p, err := scanProblemRow(rows, s.problemCols())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindSimilarResolvedProblems returns up to `limit` resolved
// problems matching the given service + rule_id, ordered most
// recent first. Used by the runbook AI to anchor LLM
// suggestions in actually-resolved-before incidents rather
// than generic SRE wisdom. We require status='resolved' so the
// model sees only outcomes it can learn from — open problems
// have no time-to-resolve signal yet.
func (s *Store) FindSimilarResolvedProblems(ctx context.Context, service, ruleID string, limit int) ([]Problem, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.conn.Query(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL
		WHERE service = ? AND rule_id = ? AND status = 'resolved'
		ORDER BY started_at DESC
		LIMIT ?`, service, ruleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		p, err := scanProblemRow(rows, s.problemCols())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListStaleOpenProblems returns open/acknowledged problems
// whose updated_at is older than `staleCutoff`. v0.5.352 —
// operator-reported: when a service stops emitting, the
// evaluator's measure() returns no data, the resolve path
// is never taken, and the problem stays open forever. This
// list feeds the periodic stale-sweep that auto-closes them.
//
// FINAL on the read so a recently-resolved-but-not-yet-merged
// row doesn't leak into the sweep.
func (s *Store) ListStaleOpenProblems(ctx context.Context, staleCutoff time.Time) ([]Problem, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL
		WHERE status IN ('open', 'acknowledged')
		  AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT 500
		SETTINGS max_execution_time = 10`, staleCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		p, err := scanProblemRow(rows, s.problemCols())
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindOpenProblem returns the latest unresolved problem for
// (rule, service). "Unresolved" covers both `open` (default
// pageable state) and `acknowledged` (operator saw it, muted
// notifications, problem still in flight) — so the v0.5.83
// bulk-ack flow doesn't accidentally make the evaluator open
// a duplicate Problem row on the next tick.
// OpenProblemKey — OpenProblemsSnapshot map anahtarı. Dışa açık:
// evaluator aynı anahtarla lookup yapar (tablo-testli).
func OpenProblemKey(ruleID, service string) string { return ruleID + "|" + service }

// NewOpenProblems (v0.10.228) — dilimden snapshot; openProblemsSnapshotUncached
// ile AYNI indirgeme (reduceLatestProblem: (rule, service) başına en yeni).
// Dış anomali tarayıcısının testi ve store'suz çağıranlar için. Girdi
// KOPYALANIR — çağıranın dilimi snapshot'la işaretçi paylaşmaz (v0.10.156
// dersi: paylaşılan işaretçi sessiz mutasyon).
func NewOpenProblems(ps []Problem) *OpenProblems {
	out := &OpenProblems{byKey: map[string]*Problem{}, byID: map[string]*Problem{}}
	for i := range ps {
		p := ps[i]
		reduceLatestProblem(out.byKey, &p)
		out.byID[p.ID] = &p
		out.all = append(out.all, &p)
	}
	return out
}

// reduceLatestProblem — aynı (rule,service) anahtarına düşen satırlardan
// started_at'i en yeni olan kazanır (FindOpenProblem'ın ORDER BY
// started_at DESC LIMIT 1 semantiğinin map karşılığı). Saf — testli.
func reduceLatestProblem(m map[string]*Problem, p *Problem) {
	k := OpenProblemKey(p.RuleID, p.Service)
	if cur, ok := m[k]; ok && cur.StartedAt >= p.StartedAt {
		return
	}
	m[k] = p
}

// OpenProblems — tek FINAL taramanın İKİ indeksi.
//
// v0.9.575 — bu tip, düz map'in ürettiği SESSİZ bir hatayı kapatıyor.
//
// Snapshot her zaman (rule_id|service) ile anahtarlanıyordu, ama
// deterministik-ID üreten dedektörler (runtime pod denetimleri, DB
// kapasitesi, paylaşılan exception patlaması) problem ID'siyle arıyordu:
//
//	snap["runtime:jvm-gc:odeme-api:pod-abc"]   ← ID, "|" YOK
//	harita anahtarı: "runtime:jvm-gc|odeme-api" ← rule|service
//
// İki uzay kesişmiyor, dolayısıyla hasOpen DAİMA false kalıyordu.
// Sonuçları ağır ve hepsi sessiz:
//   - "aç" dalı HER TİK yeniden koşuyor → dakikada bir PROBLEM OPENED,
//     incident-attach ve BİLDİRİM (sendOne'da dedup yok)
//   - StartedAt her tik sıfırlanıyor → yaş-bazlı eskalasyon ve
//     "4 saattir açık → P1" triyajı hiç ateşlemiyor
//   - histerezis kolu (wasOpen) ölü kod
//   - "kapat" dalı hiç çalışmıyor; satır ancak stale-sweep ile ve
//     YANLIŞ etiketle ("source silent") kapanıyor
//   - acknowledged her tik open'a geri yazılıyor
//
// Kök sebep v0.9.522: FindOpenProblemByID(ctx, xProblemID(...)) çağrıları
// snap[xProblemID(...)] yapıldı ama ANAHTAR ÇEVRİLMEDİ.
//
// Neden iki AYRI map, tek map'e iki anahtar değil: snapshot'ı DOLAŞAN
// kod var (anomali resolve geçişi, emekli heap tahliyesi) ve tek map'e
// çift anahtar koymak her problemi iki kez gösterirdi — bir hatayı
// düzeltirken başka bir hata.
//
// Neden ID indeksi ByKey'in yerine geçmiyor: reduceLatestProblem bir
// servisin TÜM pod'larını tek girdiye çöktürüyor (en yeni kazanır),
// yani per-pod granülerlik orada yok. İki indeks farklı sorulara cevap
// veriyor ve ikisi de gerekli.
type OpenProblems struct {
	byKey map[string]*Problem // rule_id|service — kural-bazlı denetimler
	byID  map[string]*Problem // problem ID     — deterministik-ID üreticiler
	all   []*Problem          // dolaşım için, TEKRARSIZ
}

// ByKey — (kural, servis) araması. nil alıcı güvenli: snapshot hatasında
// çağıranlar nil geçiyor ve "açık problem yok" davranışı korunuyor.
//
// v0.10.156 — KOPYA döner: snapshot 5 s memo ile goroutine'ler arasında
// PAYLAŞILIR (evaluator, monitor runner, anomaly, API); çağıranlar dönen
// satırı MarkResolved/Value/Description ile değiştirip Upsert eder. Paylaşılan
// nesneyi değiştirmek Filter'ın `*p` kopyasıyla veri yarışıydı (inceleme D1).
func (o *OpenProblems) ByKey(ruleID, service string) *Problem {
	if o == nil {
		return nil
	}
	p, ok := o.byKey[OpenProblemKey(ruleID, service)]
	if !ok || p == nil {
		return nil
	}
	q := *p
	return &q
}

// ByID — deterministik problem ID araması (per-pod granülerlik).
func (o *OpenProblems) ByID(id string) *Problem {
	if o == nil {
		return nil
	}
	p, ok := o.byID[id]
	if !ok || p == nil {
		return nil
	}
	q := *p // v0.10.156 — kopya (ByKey ile aynı gerekçe)
	return &q
}

// All — her açık problem TAM BİR KEZ. Dolaşan kod bunu kullanmalı;
// indeks map'lerini dolaşmak çift sayım üretir.
// All — SALT-OKUNUR görünüm (v0.10.156): işaretçiler paylaşılan snapshot'a
// bakar; değiştirmeden önce `q := *p` kopyala (tüm tüketiciler böyle).
func (o *OpenProblems) All() []*Problem {
	if o == nil {
		return nil
	}
	return o.all
}

// Len — açık problem sayısı (tekrarsız).
func (o *OpenProblems) Len() int {
	if o == nil {
		return 0
	}
	return len(o.all)
}

// Filter — v0.10.156: arka plan işlerinin ListProblems(status, severity,
// limit) çağrılarının snapshot karşılığı. status/severity boş = süzme yok;
// started_at DESC (ListProblems ile aynı sıra), limit ≤0 = sınırsız. Kopya
// döner (çağıran satırı değiştirebilir; snapshot paylaşımlı).
func (o *OpenProblems) Filter(status, severity string, limit int) []Problem {
	if o == nil {
		return nil
	}
	out := make([]Problem, 0, len(o.all))
	for _, p := range o.all {
		if status != "" && p.Status != status {
			continue
		}
		if severity != "" && p.Severity != severity {
			continue
		}
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// OpenProblemsSnapshot returns every open/acknowledged problem in ONE
// FINAL scan, indexed BOTH ways (bkz. OpenProblems). v0.8.520 (perf
// raporu #9): the evaluator called FindOpenProblem once per (rule,
// service) pair — ~657 nokta FINAL sorgusu/tick prod'da — hepsi aynı
// küçük state tablosunu okuyor. Tick başında tek snapshot + map lookup.
//
// v0.10.156 — 5 s memo (store.openSnap): lokal ölçüm 2026-08-29'da bu
// tarama + kardeşleri (ListProblems/FindOpenProblem arka planda) 15/dk,
// 2.0 CPU-s/dk idi (%12 app CH CPU). Tick ≥10 s; memo tick-içi tekrarları
// tek taramaya indirir, yazımda düşürülür. Test için `openSnap` nil
// olabilir (doğrudan okuma).
func (s *Store) OpenProblemsSnapshot(ctx context.Context) (*OpenProblems, error) {
	if s.openSnap == nil {
		return s.openProblemsSnapshotUncached(ctx)
	}
	return s.openSnap.Get(ctx, s.openProblemsSnapshotUncached)
}

// openSnapshotTTL — evaluator tick'i (≥10 s) altında; kazanç tick-içi paylaşım.
const openSnapshotTTL = 5 * time.Second

// invalidateOpenSnapshot — problems tablosuna her yazımdan sonra.
func (s *Store) invalidateOpenSnapshot() {
	if s.openSnap != nil {
		s.openSnap.Invalidate()
	}
}

func (s *Store) openProblemsSnapshotUncached(ctx context.Context) (*OpenProblems, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL
		WHERE status IN ('open', 'acknowledged')
		LIMIT 50000
		SETTINGS max_execution_time = 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &OpenProblems{byKey: map[string]*Problem{}, byID: map[string]*Problem{}}
	for rows.Next() {
		p, err := scanProblemRow(rows, s.problemCols())
		if err != nil {
			return nil, err
		}
		// `var p Problem` döngü İÇİNDE tanımlı — her iterasyon ayrı
		// değişken, &p almak güvenli (dışarıda tanımlı olsaydı tüm
		// işaretçiler son satıra aliaslanırdı).
		reduceLatestProblem(out.byKey, &p)
		out.byID[p.ID] = &p
		out.all = append(out.all, &p)
	}
	return out, rows.Err()
}

// FindOpenProblemByID — deterministik ID'li üreticiler (runtime pod
// denetimleri, v0.9.401) için açık/ack problem araması. FindOpenProblem
// (ruleID, service) anahtarı per-pod granülerliği taşıyamaz — pod artık
// service alanında DEĞİL.
func (s *Store) FindOpenProblemByID(ctx context.Context, id string) (*Problem, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL
		WHERE id = ? AND status IN ('open', 'acknowledged')
		ORDER BY started_at DESC LIMIT 1`, id)
	p, err := scanProblemRow(row, s.problemCols())
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) FindOpenProblem(ctx context.Context, ruleID, service string) (*Problem, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL
		WHERE rule_id = ? AND service = ? AND status IN ('open', 'acknowledged')
		ORDER BY started_at DESC LIMIT 1`, ruleID, service)
	p, err := scanProblemRow(row, s.problemCols())
	if err != nil {
		// v0.9.446 — "satır yok" hata DEĞİL (nil/nil, user.go emsali):
		// monitor keep-alive'ı gerçek okuma hatası ile süpürülmüş-satırı
		// ayırt etmek zorunda — hata varken yeniden açmak çift bildirim,
		// yok saymak kaçırılmış alarm üretir. Tüm mevcut çağıranlar
		// `err == nil && open != nil` kalıbında; davranışları değişmez.
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// AcknowledgeProblems flips a batch of problems to status=
// "acknowledged". Idempotent — already-resolved problems
// are silently skipped (you can't ack a resolved row,
// nothing to mute). The evaluator's auto-resolve path
// still flips them to "resolved" once the threshold
// stops firing, which is the right answer for an ack'd
// problem too.
func (s *Store) AcknowledgeProblems(ctx context.Context, ids []string, actor string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// Fetch the rows we want to flip. ListProblems with a
	// bounded Limit is the cheap path; the ids list is
	// bounded by the bulk-action UI cap (50). Filter to
	// non-resolved here so we don't accidentally re-open a
	// resolved row by upsert.
	all, err := s.ListProblems(ctx, ProblemFilter{Limit: 1000})
	if err != nil {
		return 0, err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	count := 0
	for i := range all {
		p := all[i]
		if !want[p.ID] || p.Status == "resolved" || p.Status == "acknowledged" {
			continue
		}
		p.Status = "acknowledged"
		if err := s.UpsertProblem(ctx, p); err != nil {
			return count, err
		}
		count++
	}
	_ = actor // future: audit log entry per acknowledge
	return count, nil
}

// OpenProblemCounts is the per-service open-problem tally
// (v0.5.274). Powers the /services health badge. Counts the
// max severity per service so the badge can flip yellow on a
// warning AND red on a critical without a second query.
type OpenProblemCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// GetOpenProblemCountsByService returns per-service tallies of
// currently-open problems grouped by severity. Single FINAL
// scan over the problems table — bounded by `status =
// 'open'` so the row count is tiny even on a busy install
// (open problems are a triage state, not a historical archive).
func (s *Store) GetOpenProblemCountsByService(ctx context.Context) (map[string]OpenProblemCounts, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT service, severity, count() AS c
		FROM problems FINAL
		WHERE status IN ('open', 'acknowledged')
		GROUP BY service, severity
		SETTINGS max_execution_time = 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]OpenProblemCounts{}
	for rows.Next() {
		var service, severity string
		var c uint64
		if err := rows.Scan(&service, &severity, &c); err != nil {
			return nil, err
		}
		entry := out[service]
		switch severity {
		case "critical":
			entry.Critical += int(c)
		case "warning":
			entry.Warning += int(c)
		default:
			entry.Info += int(c)
		}
		out[service] = entry
	}
	return out, rows.Err()
}

// GetProblem fetches a single problem by id, or nil when no
// row matches. Lighter than ListProblems for the patch-one path.
//
// v0.9.825 — YOK olan bir kayıt artık (nil, nil) döner, sürücünün
// "sql: no rows in result set" hatası değil (depo geneli sözleşme:
// user.go, monitor.go, rootcause_hypothesis.go aynı kalıp). Eskisi
// iki yeri birden bozuyordu: SetProblemAssignee'nin `cur == nil`
// dalı ULAŞILAMAZDI (üstteki `err != nil` her zaman önce dönüyordu,
// yani operatör "not found" yerine ham sürücü hatası görüyordu), ve
// yeni by-id ucu 404 ile 500'ü ayırt edemezdi.
func (s *Store) GetProblem(ctx context.Context, id string) (*Problem, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT `+s.problemSelectExpr()+`
		FROM problems FINAL
		WHERE id = ?
		LIMIT 1`, id)
	p, err := scanProblemRow(row, s.problemCols())
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// SetProblemAssignee overwrites the assignee on a single problem.
// Empty string clears the assignee — operator's explicit "take it
// back to unassigned" action. ReplacingMergeTree handles the
// dedupe; the upsert path is the same as every other write.
func (s *Store) SetProblemAssignee(ctx context.Context, id, assignee string) error {
	cur, err := s.GetProblem(ctx, id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("problem %q not found", id)
	}
	cur.Assignee = assignee
	return s.UpsertProblem(ctx, *cur)
}

func (s *Store) UpsertProblem(ctx context.Context, p Problem) error {
	// Explicit column list (v0.5.254 — was: column-order INSERT).
	//
	// v0.9.448 — ai_summary/ai_summary_at LİSTEDE. v0.5.254'ün "explicit
	// liste dışlar, ReplacingMergeTree eski değere düşer" varsayımı
	// YANLIŞTI: replace bütün-satırdır, dışlanan kolon yeni sürümde
	// DEFAULT ''e iner — her refresh (kural tick'i, anomali tazelemesi,
	// monitor keep-alive) explainer'ın özetini siliyor, explainer boş
	// görüp yeniden üretiyordu (AI maliyet döngüsü + sönüp yanan özet).
	// Her taşıyıcı okuma (FindOpenProblem/Get/List/Snapshot/Stale) iki
	// alanı zaten Scan ediyor; taze-satır açan siteler için boş = doğru.
	//
	// v0.9.976 — liste artık problemInsertCols'ta, TEK kaynak. İki yazma
	// yolunun ayrı elle-yazılmış listeleri tam olarak yukarıdaki iki
	// olayın (pod, ai_summary) sebebiydi.
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO problems ("+problemInsertCols(s.problemCols())+")")
	if err != nil {
		return err
	}
	if err := batch.Append(problemInsertArgs(p, s.problemCols())...); err != nil {
		return fmt.Errorf("append problem: %w", err)
	}
	err = batch.Send()
	s.invalidateOpenSnapshot() // v0.10.156
	return err
}

// UpsertProblemAISummary writes just the AI-explain blurb without
// touching any of the evaluator-owned fields. ReplacingMergeTree
// keeps the highest-version row at read time; this insert wins
// over a same-tick evaluator upsert because the explainer always
// runs after the problem has been opened (later wall-clock = newer
// version).
//
// Reads other fields back from the existing row first so the
// resulting full row is consistent — otherwise the merge'd row
// would have value/threshold/etc collapsing to defaults on the
// "summary-only" version.
func (s *Store) UpsertProblemAISummary(ctx context.Context, problemID, summary string) error {
	row, err := s.GetProblem(ctx, problemID)
	if err != nil || row == nil {
		return err // problem disappeared (resolved + GC'd) — drop silently
	}
	row.AISummary = summary
	row.AISummaryAt = time.Now().UnixNano()

	// v0.9.445 — pod kolonu listeye dahil: eksikken bu yazma DEFAULT ''
	// ile yeni sürüm üretiyor, ReplacingMergeTree bütün-satır replace'te
	// problemin pod bağlamını siliyordu (runtime problemlerinde P1
	// satırının hangi pod'a ait olduğu kayboluyor). GetProblem zaten tam
	// satırı okuyor; taşımamak için hiçbir neden yoktu.
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO problems ("+problemInsertCols(s.problemCols())+")")
	if err != nil {
		return err
	}
	if err := batch.Append(problemInsertArgs(*row, s.problemCols())...); err != nil {
		return fmt.Errorf("append problem ai-summary: %w", err)
	}
	err = batch.Send()
	s.invalidateOpenSnapshot() // v0.10.156
	return err
}

// ── Service backtrace queries ────────────────────────────────────────────────

type ServiceEdgeStats struct {
	Service   string  `json:"service"`
	Calls     uint64  `json:"calls"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
}

// ListProblemWindowEvents — annotation şeridinin alarm olayları (v0.9.394,
// Ş1): pencere içinde TETİKLENEN (started_at ∈ [from,to)) ya da ÇÖZÜLEN
// (resolved_at ∈ [from,to)) problemler, ÇÖZÜLMÜŞLER DAHİL. Hot triage
// yolundaki ProblemFilter'a dokunmamak için ayrı odaklı okuma —
// ReplacingMergeTree FINAL + bounded LIMIT. service "" = tüm servisler
// (global kurallar dahil).
func (s *Store) ListProblemWindowEvents(ctx context.Context, service string, from, to time.Time) ([]Problem, error) {
	conds := []string{
		"(toUnixTimestamp64Nano(started_at) >= ? AND toUnixTimestamp64Nano(started_at) < ?)" +
			" OR (resolved_at IS NOT NULL AND toUnixTimestamp64Nano(resolved_at) >= ? AND toUnixTimestamp64Nano(resolved_at) < ?)",
	}
	args := []any{from.UnixNano(), to.UnixNano(), from.UnixNano(), to.UnixNano()}
	svcSQL := ""
	if service != "" {
		svcSQL = " AND service = ?"
		args = append(args, service)
	}
	args = append(args, 500)
	rows, err := s.conn.Query(ctx, `
		SELECT id, rule_id, rule_name, service, severity, status,
		       toUnixTimestamp64Nano(started_at),
		       toUnixTimestamp64Nano(coalesce(resolved_at, toDateTime64(0, 9)))
		FROM problems FINAL
		WHERE (`+conds[0]+`)`+svcSQL+`
		ORDER BY started_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Problem{}
	for rows.Next() {
		var p Problem
		var resolved int64
		if err := rows.Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Service, &p.Severity,
			&p.Status, &p.StartedAt, &resolved); err != nil {
			return nil, err
		}
		if resolved > 0 {
			p.ResolvedAt = &resolved
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// alertRuleTargetSelect — v0.10.331: kolon henüz yoksa (ertelenmiş DDL) SELECT
// sabit ” okur; okuma yolu hiçbir boot'ta kırılmaz (iki-boot sözleşmesi).
func (s *Store) alertRuleTargetSelect() string {
	if s.hasAlertRuleTargetCol.Load() {
		return "target_json"
	}
	return "''"
}

// probeAlertRuleTargetCol — v0.10.334: alert_rules.target_json var mı? Boot'ta
// ve (kolon yokken) hedefli kural kaydında çağrılır; sonuç atomic bayrağa.
// Küme kipinde ertelenmiş DDL boot'tan sonra iner → tek restart yeter.
func (s *Store) probeAlertRuleTargetCol(ctx context.Context) bool {
	rows, err := s.conn.Query(ctx, `SELECT target_json FROM alert_rules LIMIT 1 SETTINGS max_execution_time = 3`)
	maybeCloseRows(rows, err)
	ok := err == nil
	s.hasAlertRuleTargetCol.Store(ok)
	return ok
}
