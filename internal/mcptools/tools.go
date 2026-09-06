// Package mcptools wires Coremetry's telemetry surfaces as MCP
// tools (v0.6.5). Lives in its own package — not inside `mcp` —
// so the protocol layer stays storage-agnostic and we don't risk
// a chstore↔mcp import dance.
//
// Each tool closes over a Deps struct containing the chstore +
// logstore handles. Register(srv, deps) is called once at boot
// after the MCP server is constructed, before SetMCP is called
// on the api.Server.
//
// Design choices:
//
//   - Args are decoded into a typed struct per tool. JSON Schema
//     in the registration matches that struct field-for-field so
//     a Claude Desktop-style inspector renders the right form.
//
//   - Time windows are expressed as `range_s` (seconds back from
//     now) instead of from/to nanoseconds. LLMs are notoriously
//     bad at constructing big nanosecond integers; "give me the
//     last 30 minutes" → range_s=1800 is a much more reliable
//     prompt for them than two unix-nano timestamps.
//
//   - Every tool caps Limit at a tool-specific sane default. The
//     LLM can ask for 10 or 100 but not 10000 — context windows
//     are precious and an oversized list_problems response
//     trashes downstream reasoning. Server-side cap is the
//     backstop.
//
//   - Errors are returned as Go errors; the mcp package wraps
//     them into MCP isError=true content. No need to format
//     them here.
//
//   - Yetki (v0.9.1136, AI Faz 3.1): her tool mcp.Tool.MinRole
//     taşır ve BUGÜN 24'ünün tamamı "" (viewer tabanı) — hepsi
//     salt-okunur ve hepsinin REST eşi viewer'a açık. Tek sapma
//     list_anomalies↔GET /api/anomalies/active idi (REST editor,
//     tool kapısız); A7 kararıyla REST viewer'a indi, tool ""
//     kaldı — sapma sıfır.
//     YENİ TOOL EKLERKEN: REST eşinin kapısına bak. auth.RequireRole
//     /RequireAnyRole ile sarılıysa MinRole'ü aynı role AYARLA;
//     yazma tool'u eklenirse (bu tasarımda yok) MinRole en az
//     "editor" + audit satırı şart. Zorlama api/mcp_gate.go'daki
//     kapıdadır; buradaki alan tek gerçek kaynak olduğu için MCP
//     dispatch'i ve in-app sohbet spec listesi ayrışamaz.
//
//   - Kompakt görünüm (v0.9.1230): her tool AYRICA bir
//     mcp.Tool.ShortDescription taşır — 2-3 cümlelik TÜRKÇE sözleşme.
//     Aynı kayıt defterinin iki tüketicisinin bağlam bütçesi zıt: dış
//     MCP istemcisi kataloğu bir kez okur (tam İngilizce metni görür,
//     tools/list DEĞİŞMEDİ), in-app sohbetteki gemma4 ise onu HER TUR
//     yeniden yutuyordu — ölçüm: 33 tool = 24.268 B açıklama +
//     17.672 B şema, döngü 5 tura kadar. Sohbet yolu artık
//     ChatDescription()'ı okur (kompakt toplam 5.671 B).
//     YENİ TOOL EKLERKEN: kompakt metni de yaz — ikisi aynı literal'de
//     yan yana durur ve short_desc_test.go boş bırakılmasına izin
//     vermez. Şemalar bilinçli olarak KISALTILMAZ: arg adı/varsayılanı
//     doğru çağrının koşulu, orada kazanılan bayt yanlış argümanla
//     harcanan bir tura değmez.
//
// Tool catalogue (40 tools; v0.10.469 — 39 → 40: resolve_entity.go; v0.10.468 — 36 → 39: entity_catalog.go list_namespaces / list_workloads / list_pods; sayım v0.9.1050'de düzeltildi — blok
// v0.6.5'te kalmıştı, get_problem_root_cause/render_chart sayılmıyordu;
// v0.9.1227'de get_operation_health ile 33; v0.9.1233'te
// get_exception_samples ile 34; v0.9.1244'te list_teams +
// get_team_services ile 36; v0.9.1141'ta beş keşif
// tool'uyla 19 → 24; v0.9.1142'de
// find_trace_by_request_id ile 25; v0.9.1146'da üç analiz tool'uyla 28;
// v0.9.1147'de dört guided-parite tool'uyla 32):
//   - list_services
//   - get_service_health
//   - get_operation_health (v0.9.1227 — endpoint-bazlı RED, spanmetrics_1m)
//   - list_problems
//   - get_problem_root_cause (v0.9.160)
//   - list_anomalies
//   - search_logs
//   - get_trace
//   - search_traces (v0.9.1087 — id'siz giriş, zincirin ilk halkası)
//   - list_slo_status (v0.9.1089 — durum + deterministik yörünge)
//   - query_metric
//   - list_metric_names (v0.9.1090 — query_metric'in eşi)
//   - list_exception_groups (v0.9.1091 — Exceptions sayfasının okuması)
//   - get_exception_samples (v0.9.1233 — grubun stack'i + trace pivotu;
//     zincirin ikinci yarısı, tarama penceresi GRUBUN kendi ömrü)
//   - get_correlated_changes (v0.9.1092 — MV'li "başka ne değişti")
//   - get_deploy_diff (v0.9.1092 — deploy önce/sonra RED kıyası)
//   - render_chart (v0.9.520)
//
// Keşif tool'ları (v0.9.1141, AI Faz 3.2 — discovery.go). Hepsi bir
// ARG'ın eşi: arg'ı kabul edip listesini vermeyen tool = kimlik
// uydurmaya davet (v0.9.1087-1092 dalgasının aynı gerekçesi):
//   - list_operations (render_chart'ın `operation` arg'ı; katalog, pencere YOK)
//   - list_environments (list_services/get_service_health/list_problems `env`)
//   - list_clusters (search_logs'un `cluster` arg'ı; 1h sabit pencere)
//   - list_deploys ("dün gece ne çıktı" + get_deploy_diff'e sürüm)
//   - find_trace_by_span (yapıştırılan 16-hex span id → trace id)
//
// Yapıştırılan KURUMSAL kimlik (v0.9.1142, find_trace_by_request_id.go):
//   - find_trace_by_request_id (sabit yapılı istek numarası → trace id;
//     pencere kimliğin İÇİNDEKİ damgadan gelir, o yüzden range_s YOK)
//
// Analiz tool'ları (v0.9.1146, AI Faz 3.3 — analysis.go). Bunlar bir
// ARG'ın değil bir SORUNUN eşi: model üçüne de cevap üretemiyordu ve
// üçü de olay anlatısının merkezinde. Hepsi mevcut okuyucuyu köprüler
// (yeni SQL yok):
//   - get_topology ("yukarımda/aşağımda ne var" — topology_edges_5m MV)
//   - get_blast_radius ("bu bozulursa kim bozulur" — service_callers_5m)
//   - get_log_histogram ("log hacmi zamanla nasıl" — severity kırılımı)
//
// Guided-parite tool'ları (v0.9.1147, AI Faz 3.4 — guided_parity.go). D6:
// guided router 16 intent tanıyordu ve yedisinin MCP karşılığı YOKTU.
// Dördü indi ve guided AYNI veri katmanını çağırıyor (ReadX → yapısal
// veri; tool JSON'a, guided Türkçe metne çeviriyor — okumanın ikinci
// kopyası kalmadı):
//   - get_db_health ("hangi db yavaş" — db_summary_5m)
//   - get_messaging_health ("kuyruk tarafı nasıl" — messaging_summary_5m)
//   - get_pod_health ("hangi pod'un heap'i dolu" — OTel runtime)
//   - list_problem_window_events ("dün gece neler oldu" — açılan+ÇÖZÜLEN)
//
// Takım/sahiplik (v0.9.1244, team_ownership.go). D6'nın kalan üç
// intent'inden ikisi: guided sahiplik sorularını cevaplıyordu ama
// katalogda TAKIM okuyan tek bir tool yoktu, yani guided regex'lerinin
// dışına düşen her sahiplik sorusu çıkmazdı. Çözümleme guided ile ORTAK
// (TeamCatalogue / TeamServiceNames / ReadTeamServicesRED — api artık
// buradan besleniyor, iki uygulama yok):
//   - list_teams ("hangi takımlar var" — service_metadata kataloğu)
//   - get_team_services ("ödeme takımı nasıl" — takımın servisleri + RED)
//
// KİMLİK KAPSAM DIŞI: "benim servislerim" guided'da (CallMeta → users →
// User.Team) çalışmaya devam ediyor; MCP'de karşılığı YOK ve bilinçli —
// cmk_ token'ı ROL taşır, kullanıcı değil. Ayrıntı team_ownership.go
// başlığında.
//
// Cross-signal pivot tools (v0.8.333, pivots.go):
//   - get_logs_for_trace
//   - get_exemplar_traces
//   - get_linked_traces
//   - get_metrics_for_span
//
// CoSRE Faz-2 (structured chart cards):
//   - render_chart — the model PICKS which live RED chart to show;
//     the chat server emits the deterministic ```chart``` block
//     (copilot_chat.go), the UI draws it from real telemetry.
//
// Env-awareness (v0.8.398, AI audit): list_services, get_service_health
// and list_problems accept an OPTIONAL `env` arg (deployment
// environment, spans.deploy_env — int/uat/prep style values) because
// their underlying reads already support it (GetServicesFilteredIn's
// env conjunct v0.8.385; ProblemFilter.Env service-scoped semantics
// v0.8.387). Results echo the applied env. The other tools stay
// env-less ON PURPOSE: search_logs/list_anomalies/query_metric reads
// carry no env path yet (env-separation Phase 4 pending) and
// get_trace/pivot tools are id-anchored point lookups — no silent
// half-support.
//
// v0.9.1141 — keşif tool'larında env kararı tool başına gözden geçirildi:
// list_environments/list_clusters env boyutunun KENDİSİNİ listeler (arg
// alamaz); list_operations'ın okuması operation_summary_5m'dir ve o MV'de
// deploy_env YOK (operationsUseMV env'i görünce MV'yi diskalifiye eder) →
// env arg'ı yok; list_deploys'un deploy işaretçileri env taşımıyor;
// find_trace_by_span id-çapalı. Gerekçeler discovery.go başlığında.
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// Deps bundles the data-access handles concrete tools close over.
// Kept here (rather than passed through Register) so test setups
// can construct a Deps with mocks for just the surfaces under
// test instead of building a full chstore.
type Deps struct {
	Store    *chstore.Store
	LogStore logstore.Store
	// RuntimePods — v0.10.374 (VM dilim 3c): JVM heap / GC pod reads;
	// nil → Store. main.go hands vmetrics.RuntimePodsOr so a
	// VictoriaMetrics-primary install's pod-health tool is not empty.
	RuntimePods chstore.RuntimePodReader
	// Metrics — v0.9.1150. The METRIC read router (ClickHouse or an
	// external VictoriaMetrics), for the two tools whose reads the
	// operator can repoint from Settings: query_metric and
	// list_metric_names.
	//
	// A narrow OPTIONAL field rather than turning Store into an
	// interface: ~30 other tools read *chstore.Store for span-derived
	// data that has no VM equivalent and never will, so widening Store
	// would be a large mechanical change that buys nothing. See
	// MetricSource for the nil contract.
	Metrics MetricSource
	// v0.10.468 (Faz 2, F2-1) — varlık kataloğu tool'ları için: etkin Remote
	// Cluster'lar (id/ad/span değerleri) ve entity_layer bayrağı; nil = kapalı /
	// yapılandırılmamış (tool dürüst disabled döner). api/mcp_deps.go doldurur.
	Clusters      func() []ClusterRef
	EntityEnabled func() bool
}

// MetricSource is the metric-read half of Deps, satisfied by
// *chstore.Store itself AND by internal/api's backend router. Declared
// HERE so the import direction stays api → mcptools (mcptools must never
// import api; that cycle is why /topology's hidden-pattern matcher could
// not move — see analysis.go).
type MetricSource interface {
	ListMetricNames(ctx context.Context, service, pattern string, limit, offset int) ([]chstore.MetricInfo, int, error)
	QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error)
}

// metrics returns the metric read source, falling back to Store.
//
// The fallback is safe because *chstore.Store IS the default backend —
// a nil Metrics yields byte-identical reads to pre-v0.9.1150, which is
// what test setups and any direct Deps construction want. It is NOT a
// fail-open that could silently un-apply the operator's choice: the API
// has exactly one Deps constructor (mcp_deps.go) and it always sets
// Metrics, pinned by a test there. If a second construction site ever
// appears, that test is the thing that has to be argued with.
func (d Deps) metrics() MetricSource {
	if d.Metrics != nil {
		return d.Metrics
	}
	return d.Store
}

// Register installs every v0.6.5/v0.6.6 tool and resource on
// the given MCP server. Idempotent — calling twice overwrites
// with the latest closures, but that's a logic-error pattern
// the mcp package logs about.
// ToolList returns the full telemetry tool set as plain mcp.Tool values
// closed over the given Deps, WITHOUT registering them on an MCP
// server. v0.6.53 — the in-app chatbot reuses this exact set as its
// function-calling backend: it maps each tool's Name / Description /
// InputSchema into a copilot.ToolSpec for the LLM, and invokes
// Handler(ctx, args) directly (the handler signature is transport-
// agnostic — no JSON-RPC envelope needed). Register() delegates here
// so the MCP server and the chatbot can never drift to different
// tool sets.
func ToolList(d Deps) []mcp.Tool {
	return []mcp.Tool{
		listServicesTool(d),
		// v0.9.1244 (D6 kalanı) — SAHİPLİK ekseni, filo listesinin hemen
		// yanında: "ne var" sorusunun ardından gelen "kimin" sorusu.
		// list_teams, get_team_services'in `team` arg'ının keşif eşi
		// (Faz 3.2 doktrini), o yüzden ikisi bitişik.
		listTeamsTool(d),
		getTeamServicesTool(d),
		getServiceHealthTool(d),
		// v0.9.1227 (CoSRE denetimi) — servis-seviyesi RED'in hemen
		// ardındaki İÇERİ kazı: "bu servisin HANGİ endpoint'i?". Bugüne
		// dek katalogda sayı taşıyan endpoint okuması yoktu
		// (list_operations bilinçli ad kataloğu).
		getOperationHealthTool(d),
		// v0.9.1147 (Faz 3.4) — servis RED'inin hemen ardındaki ALTYAPI
		// sorusu: "trafik tamam, pod'lar ne durumda". Guided'da zaten
		// service_health'in komşusuydu (aynı soru dizisi).
		getPodHealthTool(d),
		// v0.9.1146 (Faz 3.3) — servis kazısının iki YAPISAL sorusu, tek
		// servisin RED'inin hemen ardında: grafiğin kendisi ve yukarı-akış
		// etkisi. İkisi yan yana çünkü iş bölümleri birbirine referans
		// veriyor (downstream get_topology'de, upstream burada).
		getTopologyTool(d),
		getBlastRadiusTool(d),
		// v0.9.1147 (Faz 3.4) — topolojinin database/queue DÜĞÜMLERİNİN
		// sağlık ikizleri, grafın hemen ardında: model kenarı görüp
		// "peki o db nasıl" diye sorduğunda cevabı yanında bulsun.
		getDBHealthTool(d),
		getMessagingHealthTool(d),
		// v0.9.1141 (Faz 3.2) — yukarıdaki üçlünün + list_problems'in
		// `env` arg'ının keşif eşi; hemen yanında duruyor ki model
		// katalogda arg'ı görünce listesini de görsün.
		listEnvironmentsTool(d),
		listProblemsTool(d),
		// v0.9.1147 (Faz 3.4) — list_problems'in ZAMAN ikizi: o ŞU ANKİ
		// kümeyi verir (varsayılan status=open), bu pencerede açılan VE
		// çözülenleri. Yan yana durmaları şart, yoksa model kapanmış bir
		// olayı "yok" sanır (vardiya sorusunun sessiz yanlış cevabı).
		listProblemWindowEventsTool(d),
		getProblemRootCauseTool(d),
		listAnomaliesTool(d),
		searchLogsTool(d),
		// v0.9.1141 (Faz 3.2) — search_logs'un `cluster` arg'ının eşi;
		// env keşfi list_services üçlüsünün yanında, cluster keşfi TEK
		// tüketicisinin yanında duruyor.
		listClustersTool(d),
		// v0.9.1146 (Faz 3.3) — search_logs SATIR döndürür, bu ŞEKİL:
		// "hata ne zaman başladı" sorusu log ailesinin içinde dursun.
		getLogHistogramTool(d),
		// v0.10.468 (Faz 2, F2-1) — varlık kataloğu: list_clusters'ın ailesi
		// (log ailesi araya girmesin diye histogramdan sonra) — namespace →
		// workload → pod yürüyüşü (entity_catalog.go).
		listNamespacesTool(d),
		listWorkloadsTool(d),
		listPodsTool(d),
		// v0.10.469 (Faz 2, F2-2) — serbest metin → aday (resolve_entity.go); kataloğun hemen ardında.
		resolveEntityTool(d),
		getTraceTool(d),
		// v0.9.1087 (Faz 4) — id'siz giriş: trace ID'yi BULMANIN yolu.
		searchTracesTool(d),
		// v0.9.1141 (Faz 3.2) — trace ID'ye İKİNCİ giriş: yapıştırılan
		// çıplak span id. in-app guided yolda (v0.9.548) çalışıyordu,
		// MCP'de karşılığı yoktu.
		findTraceBySpanTool(d),
		// v0.9.1142 — trace ID'ye ÜÇÜNCÜ giriş: operatörün kurumsal
		// YAPILANDIRILMIŞ request kimliği. Elde olan kimlik bir span/trace
		// id değil; pencereyi kimliğin kendi damgası veriyor.
		findTraceByRequestIDTool(d),
		// v0.9.1089 (Faz 4) — SLO durum+yörünge; tükenme uydurması biter.
		listSLOStatusTool(d),
		queryMetricTool(d),
		// v0.9.1090 (Faz 4) — query_metric'in eşi: ad uydurmayı bitirir.
		listMetricNamesTool(d),
		// v0.9.1141 (Faz 3.2) — katalog ikizi: render_chart'ın
		// `operation` arg'ının (ve "hangi endpoint'ler var" sorusunun) eşi.
		listOperationsTool(d),
		// v0.9.1091 (Faz 4) — parmak-izine gruplu istisnalar.
		listExceptionGroupsTool(d),
		// v0.9.1233 — grubun DEVAMI: stack + trace pivotu. Zincir burada
		// bitiyordu (grup satırında ne stacktrace ne trace id var), o
		// yüzden hemen yanında: model kataloğu okurken "sayıyı gördüm,
		// peki neden" sorusunun cevabını komşu satırda bulsun.
		getExceptionSamplesTool(d),
		// v0.9.1092 (Faz 4) — "o anda başka ne değişti" (MV okuması).
		getCorrelatedChangesTool(d),
		// v0.9.1092 (Faz 4) — deploy önce/sonra RED kıyası.
		getDeployDiffTool(d),
		// v0.9.1141 (Faz 3.2) — get_deploy_diff'in eşi: sürümü tool
		// kendisi seçiyordu, model "dün gece ne çıktı"yı soramıyordu.
		listDeploysTool(d),
		// v0.8.333 — cross-signal pivot tools (pivots.go, pivot Phase 4):
		// trace↔log↔metric moves at MCP/copilot parity with the UI.
		getLogsForTraceTool(d),
		getExemplarTracesTool(d),
		getLinkedTracesTool(d),
		getMetricsForSpanTool(d),
		// CoSRE Faz-2 — structured chart cards (chart selection is the
		// model's, rendering is the server's — see copilot_chat.go).
		renderChartTool(d),
	}
}

func Register(srv *mcp.Server, d Deps) {
	for _, t := range ToolList(d) {
		srv.RegisterTool(t)
	}
	// v0.6.6 — resources: pinned references the LLM can attach
	// to its context or browse. Same data tools surface, but
	// addressable by stable URI so an inspector / Claude Desktop
	// can show "open problems" as a single pin instead of
	// re-issuing the tool call every time.
	registerResources(srv, d)
	// v0.6.7 — prompts: curated system+user message pairs that
	// surface Coremetry's in-app ✨ Explain templates over MCP.
	// Client invokes prompts/get with an id, server fetches the
	// data and returns a complete prompt the LLM can run.
	registerPrompts(srv, d)
}

// registerResources installs concrete + templated resources.
// Concrete URIs are pinned summaries; templates expose per-id
// fetches (one trace, one service, one problem). Reader closures
// share the same Deps the tools do — no new dependency wires.
func registerResources(srv *mcp.Server, d Deps) {
	// ── Static resources ───────────────────────────────────
	srv.RegisterResource(mcp.Resource{
		URI:         "coremetry://services",
		Name:        "Services",
		Description: "All Coremetry services with current RED metrics over the last 30 minutes. Refreshes on each read.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, _ string) (string, error) {
			from, to := rangeWindow(ctx, 1800)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, "", "rps", "desc", 200, 0)
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{"services": rows, "window_s": 1800})
		},
	})
	srv.RegisterResource(mcp.Resource{
		URI:         "coremetry://problems/open",
		Name:        "Open problems",
		Description: "Currently-open Coremetry Problems (alerts that have fired and not been resolved). Sorted by priority then recency.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, _ string) (string, error) {
			rows, err := d.Store.ListProblems(ctx, chstore.ProblemFilter{Status: "open", Limit: 100})
			if err != nil {
				return "", err
			}
			// v0.9.554 — bu kaynağın açıklaması "Sorted by priority then
			// recency" diyor ama öncelik HİÇ HESAPLANMIYORDU: Priority
			// boş kalıyor, omitempty ile JSON'dan düşüyor ve liste
			// yalnız started_at DESC geliyordu. Yani açıklama yanlıştı
			// ve modeli yanlış bilgilendiriyordu. Artık zincir koşuyor
			// ve sıralama gerçekten önceliğe göre.
			rows = d.Store.EnrichProblemsForRead(ctx, rows, mcpDeployLookback)
			rows = chstore.SortProblemsByPriority(rows)
			// v0.8.394 — attach the persisted root-cause hypothesis summary
			// (one batched read, soft-fails to unenriched rows).
			rows = d.Store.EnrichProblemsWithRootCause(ctx, rows)
			return marshalJSON(map[string]any{"problems": rows, "count": len(rows)})
		},
	})
	srv.RegisterResource(mcp.Resource{
		URI:         "coremetry://anomalies/recent",
		Name:        "Recent anomalies",
		Description: "Anomaly events from the last hour — log patterns + trace operations + ML detectors that exceeded their baseline.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, _ string) (string, error) {
			from, _ := rangeWindow(ctx, 3600)
			rows, err := d.Store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
				SinceNs: from.UnixNano(),
				Limit:   100,
			})
			if err != nil {
				return "", err
			}
			return marshalJSON(map[string]any{"anomalies": rows, "count": len(rows)})
		},
	})

	// ── Templated resources ─────────────────────────────────
	// Service detail by name.
	const serviceTpl = "coremetry://service/{name}"
	srv.RegisterResourceTemplate(mcp.ResourceTemplate{
		URITemplate: serviceTpl,
		Name:        "Service detail",
		Description: "RED summary + open-problem count for one service over the last 30 minutes.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, uri string) (string, error) {
			name := mcp.ExtractURITemplateValue(serviceTpl, uri)
			if name == "" {
				return "", fmt.Errorf("missing service name in URI %q", uri)
			}
			from, to := rangeWindow(ctx, 1800)
			rows, err := d.Store.GetServicesFiltered(ctx, 0, from, to, name, "rps", "desc", 1, 0)
			if err != nil {
				return "", err
			}
			if len(rows) == 0 {
				return marshalJSON(map[string]any{"found": false, "service": name})
			}
			probs, _ := d.Store.CountProblems(ctx, chstore.ProblemFilter{Status: "open", Service: name})
			return marshalJSON(map[string]any{
				"found":         true,
				"summary":       rows[0],
				"open_problems": probs,
			})
		},
	})

	// Trace detail by trace_id.
	const traceTpl = "coremetry://trace/{trace_id}"
	srv.RegisterResourceTemplate(mcp.ResourceTemplate{
		URITemplate: traceTpl,
		Name:        "Trace detail",
		Description: "Spans for one trace ID as a waterfall. Bounded exactly like the get_trace tool: at most 200 spans (all ERROR spans first, then the slowest); truncated=true + total_span_count carry the real size when the trace is larger.",
		MimeType:    "application/json",
		Reader: func(ctx context.Context, uri string) (string, error) {
			tid := mcp.ExtractURITemplateValue(traceTpl, uri)
			if tid == "" {
				return "", fmt.Errorf("missing trace_id in URI %q", uri)
			}
			spans, err := d.Store.GetTrace(ctx, tid)
			if err != nil {
				return "", err
			}
			// v0.9.1136 — get_trace ile AYNI gövde üreticisi: bu
			// resource GetTrace'i tavansız döndürüyordu (50k span'e
			// dek), yani tool 200'de kapılıyken URI yan kapısı model
			// bağlamını patlatabiliyordu.
			return marshalJSON(traceBodyPayload(tid, spans))
		},
	})
}

// marshalJSON keeps the resource Reader closures one-liner-ish.
func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// rangeWindow turns a range_s argument (seconds back from now)
// into a (from, to) pair. Used by every list/query tool below.
// Caps the lookback at 7 days so an over-eager LLM can't ask for
// 90 days of spans and trigger a CH scan timeout.
// cacheNow — v0.10.469: süreç içi CACHE TTL saati (resolve_entity.go katalog
// indeksi). Sorgu penceresi DEĞİL — pencereler rangeWindow'dan geçer; bu
// yüzden yalnız bu dosyada (anchor kapısının muaf tuttuğu tek dosya) yaşar
// ve adı bilerek "pencere" çağrıştırmaz.
func cacheNow() time.Time { return time.Now() }

func rangeWindow(ctx context.Context, rangeS int) (from, to time.Time) {
	// v0.10.50 — pencerenin SONU çıpadan gelir. Operatör geçmişe zoom
	// yaptıysa (mutlak pencere) araçlar da o ana bakmalı; yoksa sunucu
	// operatöre ve modele bir pencere İLAN edip başka bir pencereyi okur.
	to = anchorOf(ctx)
	if to.IsZero() {
		to = time.Now()
	}
	if rangeS <= 0 {
		rangeS = 1800 // default: last 30 min
	}
	if rangeS > 7*86400 {
		rangeS = 7 * 86400
	}
	from = to.Add(-time.Duration(rangeS) * time.Second)
	return
}

// clampLimit makes the LLM's `limit` request stay inside a
// per-tool reasonable band.
func clampLimit(req, defaultLim, maxLim int) int {
	if req <= 0 {
		return defaultLim
	}
	if req > maxLim {
		return maxLim
	}
	return req
}

// ─── list_services ─────────────────────────────────────────────

type listServicesArgs struct {
	NameContains string `json:"name_contains,omitempty"`
	Env          string `json:"env,omitempty"` // v0.8.398 — deploy_env narrowing
	RangeS       int    `json:"range_s,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

func listServicesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_services",
		ShortDescription: "Servisleri canlı RED'iyle listele (rps, hata oranı, p99). Olay araştırmasının giriş noktası: 'şu an hangi servis sağlıksız'. env ile tek ortama daralt.",
		Description:      "List Coremetry services with their current RPS, error rate, and p99 latency. COST: reads the 5-minute pre-aggregate when range_s >= 300 and no env filter is set — cheap, call it freely. With an env filter or a shorter window it falls back to a bounded raw-span scan, which is far more expensive: widen range_s and drop env when you just need the unhealthy-service shortlist. Use this as the entry point when investigating an incident: 'which services are unhealthy right now?'. Pass env to scope the numbers to one deployment environment.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name_contains": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring of the service name. Pass the operator's phrase VERBATIM (e.g. \"mobile bff\"): when the substring matches nothing, the server falls back to token/fuzzy matching against the live catalogue and reports matched_names (resolved exact names) or candidates (ambiguous — ask the operator which one). Empty = all services.",
				},
				"env": map[string]any{
					"type":        "string",
					"description": "Deployment environment to narrow to (spans' deploy_env, e.g. 'int', 'uat', 'prep', 'prod'). RED numbers are then computed from that environment's spans only. Empty = all environments.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"description": "Lookback window in seconds. Default 1800 (30min), max 604800 (7d).",
					"minimum":     0,
					"maximum":     604800,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max services to return. Default 50, max 500.",
					"minimum":     1,
					"maximum":     500,
				},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listServicesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			from, to := rangeWindow(ctx, a.RangeS)
			limit := clampLimit(a.Limit, 50, 500)
			// v0.8.398 — same read, env-capable variant (GetServicesFiltered
			// delegates here with env=""; an env-less call stays byte-
			// identical). Non-empty env adds the typed deploy_env conjunct.
			// v0.10.25 — MV yolu. Açıklama "ön-toplam okur" diyordu ama
			// handler KOŞULSUZ ham spans tarıyordu (services_read.go).
			rows, src, err := readServices(ctx, d, from, to, a.NameContains, a.Env, limit)
			if err != nil {
				return nil, err
			}
			res := map[string]any{"services": rows, "count": len(rows), "source": src, "has_more": len(rows) >= limit} // v0.10.407 — sayfa doluysa "hepsi bu" değil (CoSRE denetimi M3)
			// v0.10.464 (D3) — alt-dize eşleşmedi ("mobile bff" tireli adla asla
			// eşleşmez): canlı katalogda jeton/bulanık eşleşme. Çözülen adlar
			// pencerede trafiksiz olsa bile matched_names'te döner — model tam
			// adı bir sonraki çağrıda kullanır; 2+ aday → candidates (sor).
			if len(rows) == 0 && strings.TrimSpace(a.NameContains) != "" {
				if exact, cands, ferr := resolveServiceFuzzy(ctx, d, a.NameContains); ferr == nil {
					switch {
					case exact != "":
						frows, fsrc, rerr := readServicesIn(ctx, d, from, to, []string{exact}, a.Env, limit)
						if rerr == nil {
							res["services"], res["count"], res["source"], res["has_more"] = frows, len(frows), fsrc, false
						}
						res["matched_by"], res["matched_names"] = "fuzzy", []string{exact}
					case len(cands) > 0:
						res["matched_by"], res["candidates"] = "fuzzy", cands
						res["hint"] = "name_contains birden çok servise oturuyor — operatöre hangisini kastettiğini sor ya da tam adla yeniden çağır"
					}
				}
			}
			if a.Env != "" {
				res["env"] = a.Env // echo the applied narrowing
			}
			return res, nil
		},
	}
}

// ─── get_problem_root_cause ────────────────────────────────────
// v0.9.160 — expose the worker-synthesized RootCauseHypothesis (the same
// correlation intelligence the UI shows on the problem row) to external MCP
// agents AND the in-app copilot (ToolList feeds both). Read-only, one
// pre-computed lookup; never re-synthesizes on read (honest computed:false).

type getProblemRootCauseArgs struct {
	ProblemID string `json:"problem_id"`
}

func getProblemRootCauseTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_problem_root_cause",
		ShortDescription: "Bir Problem'in sentezlenmiş kök-neden hipotezi: baş şüpheli, güven skoru, aday sıralaması, ilgili deploy. checked'de found=false olan sinyali ASLA sebep gösterme.",
		Description:      "Return Coremetry's synthesized root-cause hypothesis for a Problem: the #1 suspect service, a confidence score (0-1, honestly low when evidence is thin), the full ranked list of candidate causes (each with a blended score and the topology hop-path from the anchor), and the recent deploy the correlator weighted (if any). This is the SAME correlation intelligence the UI shows on the problem row — call it after list_problems to answer 'what caused problem X?' without manually cross-referencing deploys, error spikes, and downstream timeouts. Read-only and cheap (one pre-computed lookup). Returns computed=false when the worker has not synthesized a hypothesis for this problem yet.\n\nWhen the anchor was a P1, the result also carries the INVESTIGATION: `checked` lists every signal family the worker actually inspected (pods/saturation, logs, exceptions, business dimensions) with found=true/false, and `business` breaks the failure down by channel/function code. A `checked` entry with found=false means that family was inspected and nothing was found; it is not evidence for a cause. When every entry is found=false the honest answer is 'evidence insufficient'.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"problem_id": map[string]any{
					"type":        "string",
					"description": "The Problem id (the 'id' field from list_problems). Required.",
				},
			},
			// v0.9.1050 — []any → []string: telde aynı ama tools_test'in
			// additive taraması .([]string) assert ediyor ve bu tool
			// sessizce atlanıyordu.
			"required": []string{"problem_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getProblemRootCauseArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			if a.ProblemID == "" {
				return nil, fmt.Errorf("problem_id is required")
			}
			h, err := d.Store.GetHypothesis(ctx, "problem", a.ProblemID)
			if err != nil {
				return nil, err
			}
			if h == nil {
				return map[string]any{
					"problemId": a.ProblemID,
					"computed":  false,
					"note":      "No root-cause hypothesis synthesized yet — the correlator worker computes these shortly after a problem opens. Retry later, or investigate manually with list_problems / get_service_health.",
				}, nil
			}
			out := map[string]any{
				"problemId":    a.ProblemID,
				"computed":     true,
				"service":      h.Service,
				"topSuspect":   h.TopSuspect,
				"topScore":     h.TopScore,
				"confidence":   h.Confidence,
				"candidates":   h.Candidates,
				"recentDeploy": h.RecentDeploy,
				"computedAt":   h.ComputedAt,
			}
			// v0.9.1061 — temsilî trace MCP'ye de iner. v0.9.1057 kolonu
			// ekledi ama bu handler struct'ı serialize etmiyor, map'i elle
			// kuruyor — alan burada anılmadıkça DÜŞÜYOR (canlı doğrulama
			// yakaladı: CH satırında dolu, MCP gövdesinde yok). get_trace
			// ile zincirlenebilsin diye problem satırının anahtarıyla aynı
			// adla.
			if h.ExemplarTraceID != "" {
				out["exemplarTraceId"] = h.ExemplarTraceID
			}
			// v0.9.520 — P1 soruşturmasının (v0.9.510-516) sonucu serbest
			// tool döngüsüne de açılıyor. Bunsuz tanınmayan sorular
			// guided'ın gördüğü kanıtı GÖREMİYORDU: aynı problem, chat
			// yoluna göre farklı derinlikte cevap — operatörün fark
			// edeceği bir tutarsızlık.
			//
			// KOMPAKT izdüşüm, hipotezin tamamı DEĞİL: denetim izi + iş
			// kırılımı + tek smoking-gun exception. Heap örnek dizileri ve
			// log şablonları BİLEREK dışarıda — tool sonucu döngüye geri
			// besleniyor, hacimli diziler bağlamı şişirip asıl sinyali
			// bastırır.
			if h.Deep != nil {
				if len(h.Deep.Checked) > 0 {
					out["checked"] = h.Deep.Checked
				}
				if len(h.Deep.Business) > 0 {
					out["business"] = h.Deep.Business
					if len(h.Deep.CodeMeaning) > 0 {
						out["businessCodeMeaning"] = h.Deep.CodeMeaning
					}
				}
				if len(h.Deep.Exceptions) > 0 {
					e := h.Deep.Exceptions[0]
					out["topException"] = map[string]any{
						"type": e.Type, "message": e.Message, "occurrences": e.Occurrences,
					}
				}
			}
			return out, nil
		},
	}
}

// ─── get_service_health ────────────────────────────────────────

type getServiceHealthArgs struct {
	Service string `json:"service"`
	Env     string `json:"env,omitempty"` // v0.8.398 — deploy_env narrowing
	RangeS  int    `json:"range_s,omitempty"`
}

func getServiceHealthTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_service_health",
		ShortDescription: "TEK servisin RED'i + açık problem sayısı, verilen pencerede. list_services'ten sonraki kazı adımı. env ile tek ortama daralt.",
		Description:      "Get RED metrics (rate, errors, duration p99) + open problem count for one service over a window. Use after list_services to drill into a specific service's recent health. Pass env to scope both to one deployment environment.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name. Required.",
				},
				"env": map[string]any{
					"type":        "string",
					"description": "Deployment environment to narrow to (deploy_env, e.g. 'int', 'uat', 'prep'). The RED summary is computed from that environment's spans; the open-problem count uses service-scoped env semantics (problems carry no env dimension). found=false with env set can mean 'service exists but not in this env'. Empty = all environments.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Lookback window seconds. Default 1800.",
				},
			},
			"required": []string{"service"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getServiceHealthArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.Service == "" {
				return nil, fmt.Errorf("service is required")
			}
			from, to := rangeWindow(ctx, a.RangeS)
			// v0.8.398 — env-capable variant of the same read; env=""
			// stays byte-identical to the old GetServicesFiltered call.
			// v0.10.25 — aynı MV kapısı; tek servis de olsa okuma yolu
			// list_services ile AYNI olmalı, yoksa aynı soru iki farklı
			// maliyetle cevaplanır.
			rows, _, err := readServices(ctx, d, from, to, a.Service, a.Env, 1)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				res := map[string]any{"found": false, "service": a.Service}
				if a.Env != "" {
					res["env"] = a.Env
				}
				return res, nil
			}
			probs, _ := d.Store.CountProblems(ctx, chstore.ProblemFilter{
				Status:  "open",
				Service: a.Service,
				Env:     a.Env, // v0.8.398 — service-scoped env semantics (env_members.go)
			})
			res := map[string]any{
				"found":         true,
				"summary":       rows[0],
				"open_problems": probs,
			}
			if a.Env != "" {
				res["env"] = a.Env // echo the applied narrowing
			}
			return res, nil
		},
	}
}

// ─── list_problems ─────────────────────────────────────────────

type listProblemsArgs struct {
	Status   string `json:"status,omitempty"`
	Service  string `json:"service,omitempty"`
	Env      string `json:"env,omitempty"` // v0.8.398 — service-scoped env narrowing
	Severity string `json:"severity,omitempty"`
	Priority string `json:"priority,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func listProblemsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_problems",
		ShortDescription: "Problem'leri (patlamış alarmlar) listele; varsayılan status=open, priority=P1 en acil. Satır rootCause taşıyorsa birincil kök-neden sinyali odur, tahmin etme.",
		Description:      "List Coremetry Problems (alerts that fired). Default filters to status=open. Use priority=P1 for the most urgent. Each problem has rule_id + service + severity + first_seen + a priority reason explaining why it's at its current P1/P2/P3 tier. When Coremetry's deterministic correlation engine has synthesized a root-cause verdict for a problem, the row also carries rootCause {topSuspect, topScore, confidence} — treat it as the primary root-cause signal instead of guessing. Pass env to narrow to one deployment environment (service-scoped: problems of services that ran in that env).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":  map[string]any{"type": "string", "enum": []string{"", "open", "resolved"}, "description": "Default 'open'."},
				"service": map[string]any{"type": "string", "description": "Filter to one service."},
				"env": map[string]any{
					"type":        "string",
					"description": "Deployment environment filter (e.g. 'int', 'uat', 'prep'). Problems carry no env dimension, so this is SERVICE-scoped: keeps problems whose service emitted spans in that env within the last hour, plus global service-less rules. Empty = all environments.",
				},
				"severity": map[string]any{"type": "string", "enum": []string{"", "critical", "warning", "info"}, "description": "Severity filter. Empty = all severities."},
				"priority": map[string]any{"type": "string", "enum": []string{"", "P1", "P2", "P3"}, "description": "Triage tier. P1=handle now."},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Default 25."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listProblemsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			status := a.Status
			if status == "" {
				status = "open"
			}
			// v0.9.576 — SAYFA BOYUTU ile CH TARAMASI ayrı.
			//
			// Öncesi: f.Limit = sayfa boyutu ve öncelik daraltması Go'da
			// SONRA yapılıyordu. Yani priority=P1 + limit=25 istemek "en
			// yeni 25 problemi tara, içinden P1'leri tut" demekti —
			// filoda yüzlerce P1 varken SIFIR sonuç dönebiliyordu.
			// make audit CHECK 8'in kovaladığı "LIMIT'ten sonra
			// filtrele" sınıfı; sayfa yolu bunu zaten doğru yapıyordu,
			// bu araç v0.9.554'te tuzağa düştü.
			page := clampLimit(a.Limit, 25, 200)
			f := chstore.ProblemFilter{
				Status:   status,
				Service:  a.Service,
				Env:      a.Env, // v0.8.398 — service-scoped env semantics (env_members.go)
				Severity: a.Severity,
				Limit:    page,
			}
			// v0.9.583 — öncelik daraltması AÇIKÇA iki adım. Eskiden
			// ProblemFilter.Priority alanına yazılıyordu ama mağaza o
			// alana HİÇ bakmıyordu; alan silindi (bkz. problem.go).
			var prios []string
			if a.Priority != "" {
				prios = []string{a.Priority}
				// Daraltma Go'da olduğu için tarama genişler (kanonik
				// kural, sayfa yoluyla AYNI fonksiyondan).
				f.Limit = chstore.ProblemScanLimit(page, true)
			}
			rows, err := d.Store.ListProblems(ctx, f)
			if err != nil {
				return nil, err
			}
			// v0.9.554 — ÖNCELİK ZENGİNLEŞTİRMESİ. Öncesi: bu yol yalnız
			// EnrichProblemsWithRootCause çağırıyordu, yani dönen
			// satırlarda Priority BOŞ kalıyor ve omitempty ile JSON'dan
			// düşüyordu. Araç açıklaması ise modele P1/P2/P3
			// tier'larından bahsediyor — model de tier'ı UYDURUYORDU.
			// Operatörün "P1 alertleri karıştırıyor" şikâyetinin en
			// doğrudan kolu buydu: uydurulan tier hiçbir yüzeyle
			// eşleşmiyor, çünkü hiçbir yerden gelmiyor.
			rows = d.Store.EnrichProblemsForRead(ctx, rows, mcpDeployLookback)
			// priority argümanı SQL'de UYGULANMAZ (öncelik okuma anı
			// hesabı, CH satırında yok — problem.go:594-605). Daraltma
			// Go'da yapılmazsa argüman sessizce yok sayılır.
			rows = chstore.FilterProblemsByPriority(rows, prios)
			rows = chstore.SortProblemsByPriority(rows)
			// Genişletilmiş tarama SAYFAYA kırpılır — model 25 satır
			// istediyse 125 satır almasın.
			hasMore := len(rows) > page
			if len(rows) > page {
				rows = rows[:page]
			}
			// v0.8.394 (AI audit A1) — attach the persisted deterministic
			// root-cause hypothesis summary (rootCause {topSuspect, topScore,
			// confidence}) so the chat/MCP caller sees the same verdict the
			// /problems ribbon shows. One batched GetHypotheses read;
			// soft-fails to unenriched rows. Additive, omitempty field.
			rows = d.Store.EnrichProblemsWithRootCause(ctx, rows)
			res := map[string]any{"problems": rows, "count": len(rows), "has_more": hasMore} // v0.10.407 — sayfa doluysa "hepsi bu" değil (CoSRE denetimi M3)
			if a.Env != "" {
				res["env"] = a.Env // echo the applied narrowing (v0.8.398)
			}
			return res, nil
		},
	}
}

// ─── list_anomalies ────────────────────────────────────────────

type listAnomaliesArgs struct {
	Service string `json:"service,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listAnomaliesTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_anomalies",
		ShortDescription: "Son anomali olayları — 'baseline'a göre bir şey değişti' bildirimleri, sert alarm değil. Servis filtresi YAKLAŞIK: boş sonuç 'bu serviste yok' demek DEĞİLDİR.",
		Description:      "List recent anomaly events (log-pattern + trace-op + ML detectors). Anomalies are 'something changed against baseline' notices — not hard alerts. Use this when investigating a service whose RED metrics look normal but the operator suspects a behavior shift. NOTE: the service filter is APPROXIMATE — a wider fleet slice is fetched and post-filtered, so an empty result for one service means 'none in the fetched slice', not a proven zero; do not state 'this service has no anomalies' as certain.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{"type": "string", "description": "Exact service name. Approximate filter (see tool description). Empty = whole fleet."},
				"range_s": map[string]any{"type": "integer", "minimum": 0, "maximum": 604800, "description": "Lookback seconds. Default 3600 (1h)."},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Default 25."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listAnomaliesArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			rs := a.RangeS
			if rs == 0 {
				rs = 3600
			}
			from, _ := rangeWindow(ctx, rs)
			lim := clampLimit(a.Limit, 25, 200)
			// ListAnomalyEventsFilter has no Service slot — when
			// the LLM asks for one we fetch a wider slice (4x the
			// asked limit, capped) and post-filter. Sufficient for
			// the use case (one service inside a 1h window rarely
			// produces more than the cap).
			fetchLim := lim
			if a.Service != "" {
				fetchLim = clampLimit(lim*4, 100, 200)
			}
			rows, err := d.Store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
				SinceNs: from.UnixNano(),
				Limit:   fetchLim,
			})
			if err != nil {
				return nil, err
			}
			if a.Service != "" {
				filtered := rows[:0]
				for _, r := range rows {
					if r.Service == a.Service {
						filtered = append(filtered, r)
						if len(filtered) >= lim {
							break
						}
					}
				}
				rows = filtered
			}
			return map[string]any{"anomalies": rows, "count": len(rows), "has_more": len(rows) >= lim}, nil // v0.10.407 — sayfa doluysa "hepsi bu" değil (CoSRE denetimi M3)
		},
	}
}

// ─── search_logs ───────────────────────────────────────────────

type searchLogsArgs struct {
	Query       string `json:"query,omitempty"`
	Service     string `json:"service,omitempty"`
	Cluster     string `json:"cluster,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
	SeverityMin int    `json:"severity_min,omitempty"`
	RangeS      int    `json:"range_s,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func searchLogsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "search_logs",
		ShortDescription: "Loglarda tam metin + yapısal arama (CH ya da ES). trace_id ile bir trace'in tüm satırları, severity_min=17 yalnız hatalar. partial / envUnapplied bayraklarını cevaba taşı.",
		Description:      "Full-text + structured search across logs. Routes to whichever backend Coremetry is configured for (ClickHouse or Elasticsearch). Use trace_id to pull every log line belonging to one trace. Use severity_min=17 for errors only (OTel severity number; 17=ERROR, 21=FATAL). HONESTY ENVELOPE: the response may carry partial=true (soft timeout / failed shards — rows are a SUBSET of the true answer), shardsFailed>0, totalIsLowerBound=true (total is 'at least', not exact) and envUnapplied=true (an env filter was requested but could not be applied — results are env-UNFILTERED). Never present a partial or env-unfiltered result as complete/narrowed; say so.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":        map[string]any{"type": "string", "description": "Free-text or structured query (ES query_string when ES is the backend)."},
				"service":      map[string]any{"type": "string", "description": "Exact service name filter. Empty = all services."},
				"cluster":      map[string]any{"type": "string", "description": "k8s cluster name from resource attrs."},
				"trace_id":     map[string]any{"type": "string", "description": "Pull all logs for one trace."},
				"severity_min": map[string]any{"type": "integer", "minimum": 0, "maximum": 24, "description": "OTel severity number floor. 17=ERROR."},
				"range_s":      map[string]any{"type": "integer", "minimum": 0, "maximum": 604800, "description": "Lookback seconds. Default 1800."},
				"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Default 50."},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a searchLogsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			from, to := rangeWindow(ctx, a.RangeS)
			page, err := d.LogStore.Search(ctx, logstore.Filter{
				Service:     a.Service,
				Cluster:     a.Cluster,
				Search:      a.Query,
				TraceID:     a.TraceID,
				From:        from,
				To:          to,
				SeverityMin: uint8(a.SeverityMin),
				Limit:       clampLimit(a.Limit, 50, 500),
			})
			if err != nil {
				return nil, err
			}
			return page, nil
		},
	}
}

// ─── get_trace ─────────────────────────────────────────────────

type getTraceArgs struct {
	TraceID string `json:"trace_id"`
}

// getTraceSpanCap — get_trace'in gövde tavanı (v0.9.1050, Faz 0.10).
// GetTrace LIMIT 50000'e dek span döndürür ve her span attr+resource
// map'leri + events JSON'u taşır: tek çağrı LLM bağlamını patlatabilir
// (setteki tek "get_* body bounded" ihlaliydi). 200 span tavanı ≈ makul
// bağlam; seçim capTraceSpans'te (hata-koruyan — v0.9.414 dersi: düz
// head-cap derindeki hatayı düşürür).
const getTraceSpanCap = 200

// capTraceSpans — saf, tablo-testli. len<=max ise aynen döner. Aşınca:
// ÖNCE tüm hata span'leri (waterfall'ın anlattığı şey), sonra süresi en
// uzun span'ler; sonuç başlangıç zamanına göre yeniden sıralanır ki
// kırpılmış şelale kronolojik okunur kalsın.
func capTraceSpans(spans []chstore.SpanRow, max int) ([]chstore.SpanRow, bool) {
	if max <= 0 || len(spans) <= max {
		return spans, false
	}
	picked := make([]chstore.SpanRow, 0, max)
	for _, sp := range spans {
		if sp.StatusCode == "error" {
			picked = append(picked, sp)
			if len(picked) == max {
				break
			}
		}
	}
	if len(picked) < max {
		rest := make([]chstore.SpanRow, 0, len(spans))
		for _, sp := range spans {
			if sp.StatusCode != "error" {
				rest = append(rest, sp)
			}
		}
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].DurationMs > rest[j].DurationMs })
		need := max - len(picked)
		if need > len(rest) {
			need = len(rest)
		}
		picked = append(picked, rest[:need]...)
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].StartTime < picked[j].StartTime })
	return picked, true
}

// traceBodyPayload — get_trace tool'unun VE coremetry://trace/{id}
// resource'unun ortak gövde üreticisi (v0.9.1136). Tek üretici =
// iki yol tavanda/alan adlarında asla ayrışmaz; resource'un tavansız
// kaldığı yan kapı bu yüzden vardı.
func traceBodyPayload(traceID string, spans []chstore.SpanRow) map[string]any {
	total := len(spans)
	capped, truncated := capTraceSpans(spans, getTraceSpanCap)
	return map[string]any{
		"trace_id":         traceID,
		"spans":            capped,
		"span_count":       len(capped),
		"total_span_count": total,
		"truncated":        truncated,
	}
}

func getTraceTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_trace",
		ShortDescription: "Bir trace ID'sinin span şelalesi (servis, operasyon, süre, hata, parent_span_id). En çok 200 span; truncated=true iken 'tüm trace'i gördüm' deme.",
		Description:      "Fetch the spans of one trace ID as a waterfall: service, operation, duration, error status, parent_span_id. Use after search_logs surfaces a trace ID, or directly from a problem's correlated traces. Bounded: at most 200 spans are returned — all ERROR spans are kept first, then the slowest ones; when the trace is larger, truncated=true and total_span_count carries the real size, so never claim to have seen the whole trace while truncated is true.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"trace_id": map[string]any{"type": "string", "description": "32-char hex trace ID."},
			},
			"required": []string{"trace_id"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getTraceArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.TraceID == "" {
				return nil, fmt.Errorf("trace_id is required")
			}
			spans, err := d.Store.GetTrace(ctx, a.TraceID)
			if err != nil {
				return nil, err
			}
			return traceBodyPayload(a.TraceID, spans), nil
		},
	}
}

// ─── query_metric ──────────────────────────────────────────────

type queryMetricArgs struct {
	Name        string `json:"name"`
	Service     string `json:"service,omitempty"`
	Aggregation string `json:"aggregation,omitempty"`
	GroupBy     string `json:"group_by,omitempty"`
	RangeS      int    `json:"range_s,omitempty"`
	StepS       int    `json:"step_s,omitempty"`
}

func queryMetricTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "query_metric",
		ShortDescription: "Ingest edilmiş OTel metriklerinde zaman kovalı sorgu → {time, value} serileri. p99 gecikme histogramı, sum sayaç, avg gauge için.",
		Description:      "Run a time-bucketed query against ingested OTel metrics. Returns one or more series of {time, value} points. Use aggregation='p99' for latency histograms, 'sum' for counters, 'avg' for gauges. Pair with the OTel semantic conventions (e.g. http.server.request.duration → p99 / ms).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "OTel metric name."},
				"service":     map[string]any{"type": "string", "description": "Exact service name filter. Empty = all services."},
				"aggregation": map[string]any{"type": "string", "enum": []string{"avg", "sum", "min", "max", "last", "p50", "p95", "p99"}, "description": "Default 'avg'."},
				"group_by":    map[string]any{"type": "string", "description": "Comma-separated attribute keys (e.g. 'http.route,http.status_code')."},
				"range_s":     map[string]any{"type": "integer", "minimum": 0, "maximum": 604800, "description": "Lookback seconds. Default 1800."},
				"step_s":      map[string]any{"type": "integer", "description": "Bucket size seconds. 0 = auto."},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a queryMetricArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.Name == "" {
				return nil, fmt.Errorf("name is required")
			}
			agg := a.Aggregation
			if agg == "" {
				agg = "avg"
			}
			from, to := rangeWindow(ctx, a.RangeS)
			var groups []string
			if a.GroupBy != "" {
				for _, p := range splitCSV(a.GroupBy) {
					groups = append(groups, p)
				}
			}
			// v0.9.1150 — metrik okuma ROUTER'ından (CH ya da VM).
			series, err := d.metrics().QueryMetric(ctx, chstore.MetricQueryFilter{
				Name:        a.Name,
				Service:     a.Service,
				Aggregation: agg,
				GroupBy:     groups,
				From:        from,
				To:          to,
				StepSeconds: a.StepS,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"series": series, "count": len(series)}, nil
		},
	}
}

// ─── render_chart ──────────────────────────────────────────────
// CoSRE Faz-2 — MCP-backed structured chart cards. The model calls
// this to show the operator a LIVE chart; the handler only validates
// + acknowledges. The chart itself is emitted server-side: the chat
// loop (internal/api/copilot_chat.go) turns the validated spec into a
// deterministic ```chart``` fence, and the frontend (CosreChart.tsx)
// draws it from real telemetry via spanMetricBatch. A gemma4-class
// small model never formats chart JSON itself.
//
// The metric whitelist MUST stay 1:1 with CosreChart's AGG_META keys
// (rate|error_rate|p50|p95|p99) — an unknown agg silently falls back
// to 'rate' on the frontend.

// renderChartMetrics is the agg whitelist — mirror of CosreChart's
// AGG_META keys.
var renderChartMetrics = map[string]bool{
	"rate": true, "error_rate": true, "p50": true, "p95": true, "p99": true,
}

type renderChartArgs struct {
	Service   string `json:"service"`
	Operation string `json:"operation,omitempty"`
	Metric    string `json:"metric,omitempty"`
	RangeS    int    `json:"range_s,omitempty"`
	// v0.9.1186 (Faz 4.4) — kırılım anahtarı. TEK anahtar ve KISA bir
	// beyaz liste: sohbet balonundaki ~560px kartta iki anahtarlı kırılım
	// seri sayısını çarpar, kırılım ise tam da okunabilirlik için var.
	GroupBy string `json:"group_by,omitempty"`
}

// renderChartGroupKeys — kırılım beyaz listesi.
//
// Motor (chstore.groupKeyExpr) çok daha fazlasını anlıyor (resource.*,
// span.*, op_group…) ama BURASI bir sohbet kartı: listeyi geniş tutmak,
// modele okunmaz bir kırılım seçme fırsatı vermek olurdu. Beşi de "bu
// grafikte NEDEN farklı davranıyor" sorusunun gerçek cevapları:
// hangi endpoint, hangi operasyon, hangi span türü, ok/hata, hangi komşu.
var renderChartGroupKeys = map[string]bool{
	"http.route": true, "name": true, "kind": true,
	"status": true, "peer": true,
}

// normalizeRenderChart applies the metric default + whitelist and the
// range_s clamp (default 30min, cap 7d — same band as rangeWindow).
// Pure — table-tested in tools_test.go. errMsg != "" means the metric
// is not chartable; the caller returns it as {ok:false, error} so the
// LLM can self-correct instead of getting an opaque tool error.
func normalizeRenderChart(a renderChartArgs) (norm renderChartArgs, errMsg string) {
	if a.Metric == "" {
		a.Metric = "error_rate"
	}
	if !renderChartMetrics[a.Metric] {
		return a, fmt.Sprintf("unknown metric %q — must be one of rate, error_rate, p50, p95, p99", a.Metric)
	}
	if a.RangeS <= 0 {
		a.RangeS = 1800
	}
	if a.RangeS > 7*86400 {
		a.RangeS = 7 * 86400
	}
	// v0.9.1186 — kırılım: boş serbest, tanınmayan REDDEDİLİR.
	//
	// Sessizce yok saymak (metric'in aksine, ki o da reddediyor) modelin
	// istediği kırılımı çizmeden "çizdim" demek olurdu ve operatör kartı
	// kırılımlı sanıp okurdu. Hata mesajı geçerli listeyi sayar ki model
	// kendini düzeltebilsin.
	if a.GroupBy != "" && !renderChartGroupKeys[a.GroupBy] {
		return a, fmt.Sprintf("unknown group_by %q — must be one of http.route, name, kind, status, peer", a.GroupBy)
	}
	return a, ""
}

func renderChartTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "render_chart",
		ShortDescription: "Operatöre CANLI grafik göster (istek hızı / hata oranı / gecikme yüzdeliği), servis + isteğe bağlı operasyon. 'Grafiğini göster' dendiğinde çağır; veri noktalarını sen anlatma.",
		Description:      "Show the operator a LIVE chart of one RED metric (request rate, error rate, or a latency percentile) for a service, optionally narrowed to one operation. Call this when the operator asks to SEE a graph ('grafiğini göster', 'çiz', 'plot the error rate') or when a visual trend answers better than numbers. The server validates the service name and acknowledges; the chat UI then draws the chart from live telemetry — do NOT describe the data points or draw ASCII charts yourself. Cheap: one service-name lookup, no span scan. Outside the in-app chat (plain MCP clients) it returns the validated chart spec without rendering.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name to chart. Required. Must be an existing service — check with list_services if unsure.",
				},
				"operation": map[string]any{
					"type":        "string",
					"description": "Optional operation (span name) to narrow the chart to one endpoint, e.g. 'GET /orders/:id'. Empty = whole service.",
				},
				"metric": map[string]any{
					"type":        "string",
					"enum":        []string{"rate", "error_rate", "p50", "p95", "p99"},
					"description": "Which series to chart: rate (req/s), error_rate (%), or a latency percentile (ms). Default error_rate.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Chart window in seconds. Default 1800 (30min), max 604800 (7d).",
				},
				"group_by": map[string]any{
					"type":        "string",
					"enum":        []string{"http.route", "name", "kind", "status", "peer"},
					"description": "Optional breakdown: draw ONE line per distinct value instead of a single total. Use it when the question is 'which one is different' — http.route (which endpoint), name (which operation), kind (server/client/producer/consumer), status (ok vs error), peer (which downstream). Omit for a single total line.",
				},
			},
			"required": []string{"service"},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a renderChartArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
			if a.Service == "" {
				return nil, fmt.Errorf("service is required")
			}
			a, emsg := normalizeRenderChart(a)
			if emsg != "" {
				return map[string]any{"ok": false, "error": emsg}, nil
			}
			// Validate the service EXISTS under its exact name so the UI
			// never renders an empty chart for a hallucinated one. Same
			// picker-backed MV read the ServicePicker uses (cheap DISTINCT);
			// the ILIKE-substring result is exact-matched in Go — "checkout"
			// must not pass on the strength of "checkout-v2" existing.
			names, _, err := d.Store.ListServiceNames(ctx, a.Service, 50, 0)
			if err != nil {
				return nil, err
			}
			found := false
			for _, n := range names {
				if n == a.Service {
					found = true
					break
				}
			}
			if !found {
				// v0.10.464 (D3) — yakın adlar hatanın içinde: model bir sonraki
				// çağrıda tam adı kullanabilsin (tek aday → doğrudan onu öner).
				msg := fmt.Sprintf("service %q not found — check the exact name with list_services", a.Service)
				if exact, cands, ferr := resolveServiceFuzzy(ctx, d, a.Service); ferr == nil {
					if exact != "" {
						msg = fmt.Sprintf("service %q not found — did you mean %q? call again with the exact name", a.Service, exact)
					} else if len(cands) > 0 {
						msg = fmt.Sprintf("service %q not found — candidates: %s (ask the operator which one, then call again with the exact name)", a.Service, strings.Join(cands, ", "))
					}
				}
				return map[string]any{"ok": false, "error": msg}, nil
			}
			// spec keys mirror the frontend CosreChartSpec (service /
			// operation / agg / rangeS) — copilot_chat.go re-parses this
			// exact shape to build the ```chart``` fence.
			spec := map[string]any{"service": a.Service, "agg": a.Metric, "rangeS": a.RangeS}
			if a.Operation != "" {
				spec["operation"] = a.Operation
			}
			if a.GroupBy != "" {
				spec["groupBy"] = a.GroupBy
			}
			return map[string]any{
				"ok":   true,
				"spec": spec,
				"note": "grafik render edildi — operatörün sohbetine canlı veriyle gömülecek; veriyi ayrıca metinle tarif etme, ASCII grafik çizme.",
			}, nil
		},
	}
}

// splitCSV: tiny helper kept private so we don't pull in
// strings just for one Split call further away.
func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// runtimePods — the JVM pod reader, Store when none was injected.
func (d Deps) runtimePods() chstore.RuntimePodReader {
	if d.RuntimePods != nil {
		return d.RuntimePods
	}
	return d.Store
}
