package mcptools

// Keşif tool'ları (v0.9.1141, AI Faz 3.2) — "arg kabul eden ama listesini
// vermeyen tool" sayısını sıfıra indiren beş köprü.
//
// v0.9.1087-1092 dalgası (search_traces, list_metric_names) kimlik
// UYDURMASINI kesmek için vardı: model bir ID/ad istendiğinde onu bulmanın
// yolunu görmezse ezberden ad üretiyor ve boş sonuca bakıp "sorun yok"
// diyor. O dalgadan sonra beş arg keşifsiz kalmıştı:
//
//   - `operation` (render_chart) → list_operations
//   - `env` (list_services, get_service_health, list_problems) → list_environments
//   - `cluster` (search_logs) → list_clusters
//   - deploy sürümü (get_deploy_diff sürümü KENDİ seçiyordu; model
//     "dün gece ne çıktı"yı soramıyordu) → list_deploys
//   - yapıştırılan 16-hex span id (in-app guidedSpanBundle'da çalışıyor,
//     MCP'de karşılığı yoktu) → find_trace_by_span
//
// PENCERE DÜRÜSTLÜĞÜ — üç okumanın window semantiği FARKLI ve tool'lar
// bunu taklit eder, gizlemez (skill: "don't lie in the tool description"):
//
//   - ListOperationNames: pencere argümanı YOK. operation_summary_5m
//     üzerinde DISTINCT, yani MV'nin TTL'i (90 gün) kadar geriye bakan bir
//     KATALOG. Dolayısıyla list_operations'ta range_s YOK — koysaydık
//     sessizce yok sayılan bir arg olurdu.
//   - ListEnvironments: taramayı store-side 1 saate (arama varsa 24 saate)
//     kelepçeler (envEnumWindow, v0.8.389). Bilinçli sabit pencere →
//     range_s YOK; efektif pencere window_s ile İFŞA edilir.
//   - ListClusters: pencere alır ama clampClusterFrom onu son 1 saate
//     kelepçeler (v0.8.188 prod timeout'u). range_s VAR ama şemadaki
//     maximum 3600 — LLM'e kelepçeyi keşfettirmek yerine şemada söylüyoruz.
//
// ENV ARG KARARI (v0.8.398 sözleşmesi, tool başına gözden geçirildi):
// list_environments/list_clusters env boyutunun KENDİSİNİ listeler, arg
// alamaz. list_operations'ın okuması operation_summary_5m'dir ve o MV'de
// deploy_env kolonu YOKTUR (chstore.operationsUseMV env'i gördüğünde MV'yi
// diskalifiye eder — operation_env_test.go); dolayısıyla env arg'ı
// uygulanamayacak bir filtre vaat ederdi → YOK. list_deploys: deploy
// işaretçileri env boyutu taşımıyor (guidedDeployBundle bunu açıkça
// söyler) → YOK. find_trace_by_span id-çapalı nokta aramasıdır → YOK.
//
// Beşi de salt-okunur ve REST eşleri viewer'a açık (/api/environments,
// /api/clusters, /api/operations, /api/services/{name}/deploys) →
// MinRole "" (viewer tabanı), her tool'da AÇIKÇA yazılı.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/mcp"
)

// clusterEnumRangeS — list_clusters'ın range_s kelepçesi. chstore
// clampClusterFrom taramayı zaten son clusterScanWindow'a (1 saat)
// indiriyor; burada AYNI tavanı uygulayıp window_s'i gerçek pencereyle
// döndürüyoruz, yoksa "24 saate baktım" diyen bir cevap üretirdik.
// Saf — tablo testli.
const clusterEnumRangeS = 3600

// clusterEnumCap — ListClusters'ın SQL LIMIT'i. Sonuç tam bu sayıda
// geldiyse kesme İHTİMALİ vardır ve truncated ile ifşa edilir (store
// total döndürmüyor; sessiz tavan bırakmaktan iyisi bu).
const clusterEnumCap = 200

// deployImpactWindowS — ComputeDeployImpact'in (ve get_deploy_diff'in)
// varsayılan ±pencere'si. list_deploys ETKİ HESAPLAMAZ; yalnız "bu
// deploy'un after tarafı dolmuş mu" bayrağını taşır.
const deployImpactWindowS = 600

// serviceDeploysStoreCap — GetServiceDeploys'un SQL LIMIT'i
// (serviceDeploysSQL). Store total döndürmediği için "tam bu sayıda geldi"
// tek kesme sinyalimiz; has_more ile ifşa edilir.
const serviceDeploysStoreCap = 50

// deployEnumFloorS — deploy sorusunun tabanı 6 saat. guidedDeployBundle'ın
// gerekçesi aynen geçerli: "son deploy" nadiren son yarım saattedir, o
// yüzden sohbetin 30dk'lık varsayılanı bu soruyu boş döndürür.
const deployEnumFloorS = 6 * 3600

// spanScanMaxRangeS — find_trace_by_span'in tavanı 24 saat. spans'ta
// span_id İNDEKSLİ DEĞİL (trace_id'de bloom var, span_id'de yok), yani
// bu bir pencere-içi kolon taraması: pencere maliyetin TEK sınırıdır
// (store tarafında ayrıca max_execution_time = 8). 7 günlük genel
// rangeWindow tavanı burada yanlış sinyal olurdu.
const spanScanMaxRangeS = 86400

// envEnumWindowS mirrors chstore.envEnumWindow (unexported): aramasız
// enumerasyon son 1 saat, arama varsa 24 saat. Yalnız RAPORLAMA için —
// kelepçenin sahibi store. Saf, tablo testli. Store'daki eşik değişirse
// BURASI DA değişmeli (aksi hâlde window_s yalan söyler).
func envEnumWindowS(pattern string) int {
	if strings.TrimSpace(pattern) != "" {
		return 24 * 3600
	}
	return 3600
}

// clusterEnumWindowS: range_s → efektif saniye (varsayılan + 1h tavan).
func clusterEnumWindowS(rangeS int) int {
	if rangeS <= 0 || rangeS > clusterEnumRangeS {
		return clusterEnumRangeS
	}
	return rangeS
}

// deployEnumWindowS: range_s → lookback saniye (6h taban, 7g tavan).
func deployEnumWindowS(rangeS int) int {
	if rangeS < deployEnumFloorS {
		return deployEnumFloorS
	}
	if rangeS > 7*86400 {
		return 7 * 86400
	}
	return rangeS
}

// spanScanWindowS: range_s → efektif saniye (30dk varsayılan, 24h tavan).
func spanScanWindowS(rangeS int) int {
	if rangeS <= 0 {
		return 1800
	}
	if rangeS > spanScanMaxRangeS {
		return spanScanMaxRangeS
	}
	return rangeS
}

// ─── gövde üreticileri (saf) ───────────────────────────────────
//
// Her keşif tool'unun cevap gövdesi SAF bir üreticiden çıkar. Sebep
// mimari değil TESTtir: Deps.Store somut *chstore.Store olduğundan
// handler'lar CH olmadan koşamaz, ama dürüstlük zarfları (truncated /
// has_more / window_s / found=false) tam olarak sessizce düşen şeylerdir.
// Üretici saf olunca zarf tablo testiyle pinlenir; handler'ın gerçekten
// bu üreticiden dönmesi de kaynak pini testiyle bağlanır
// (discovery_test.go — TestDiscoveryHandlersReturnViaPurePayloads).

// operationsPayload — list_operations gövdesi. truncated, ListOperationNames'in
// total'ı ile döndürülen sayfayı karşılaştırır (store LIMIT'i sessiz kalmasın).
func operationsPayload(service string, names []string, total int) map[string]any {
	return map[string]any{
		"service":    service,
		"operations": names,
		"count":      len(names),
		"total":      total,
		"truncated":  total > len(names),
	}
}

// environmentsPayload — list_environments gövdesi. window_s efektif
// enumerasyon penceresini (1h / arama varsa 24h) İFŞA eder.
func environmentsPayload(names []string, total uint64, pattern string) map[string]any {
	return map[string]any{
		"environments": names,
		"count":        len(names),
		"total":        total,
		"truncated":    total > uint64(len(names)),
		"window_s":     envEnumWindowS(pattern),
	}
}

// clustersPayload — list_clusters gövdesi. Store total döndürmüyor;
// sonuç SQL LIMIT'ine dayandıysa truncated ile ifşa edilir.
func clustersPayload(names []string, windowS int) map[string]any {
	return map[string]any{
		"clusters":  names,
		"count":     len(names),
		"window_s":  windowS,
		"truncated": len(names) >= clusterEnumCap,
	}
}

// deploysPayload — list_deploys gövdesi: sıralama+tavan (deployRows) +
// dürüstlük zarfı. storeCapped okumanın KENDİ tavanına dayanmasıdır.
func deploysPayload(rows []deployRow, now time.Time, windowS, limit int, storeCapped bool) map[string]any {
	out := deployRows(rows, now, limit)
	return map[string]any{
		"deploys":         out,
		"count":           len(out),
		"window_s":        windowS,
		"impact_window_s": deployImpactWindowS,
		"has_more":        storeCapped || len(rows) > len(out),
	}
}

// ─── list_operations ───────────────────────────────────────────

type listOperationsArgs struct {
	Service string `json:"service"`
	Pattern string `json:"pattern,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listOperationsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_operations",
		ShortDescription: "Bir servisin ürettiği operasyon (OTel span) adları KATALOĞU. render_chart'a `operation` vermeden önce çağır. Sayı YOK (onun için get_operation_health) ve zaman penceresi yok.",
		Description: "List the operation names (OTel span names — 'GET /orders/:id', 'SELECT orders', 'consume orders.created') " +
			"a service actually emits, optionally narrowed by a substring pattern. " +
			"ALWAYS use this before passing `operation` to render_chart, and whenever the operator asks 'which endpoints does X expose' — " +
			"a guessed operation name silently produces an empty chart. " +
			"Reads the 5-minute operation pre-aggregate so it is cheap. " +
			"NOT time-windowed: this is the CATALOGUE over the aggregate's retention (~90 days), so an operation retired weeks ago can still " +
			"appear — confirm it is live with render_chart or search_traces before treating it as current. " +
			"`total` + truncated expose how many names matched beyond the returned page.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (from list_services). Required — a fleet-wide operation listing is unusable at 10k services.",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring of the operation name, e.g. \"orders\" or \"GET /\". `*` and `?` work as wildcards. Empty = all.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     500,
					"description": "Max operation names to return. Default 100.",
				},
			},
			"required": []string{"service"},
		},
		// Salt-okunur katalog; REST eşi GET /api/operations viewer'a açık.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listOperationsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			service := strings.TrimSpace(a.Service)
			if service == "" {
				return nil, fmt.Errorf("service zorunlu — önce list_services")
			}
			limit := clampLimit(a.Limit, 100, 500)
			names, total, err := d.Store.ListOperationNames(ctx, service, a.Pattern, limit, 0)
			if err != nil {
				return nil, err
			}
			return operationsPayload(service, names, total), nil
		},
	}
}

// ─── list_environments ─────────────────────────────────────────

type listEnvironmentsArgs struct {
	Pattern string `json:"pattern,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listEnvironmentsTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_environments",
		ShortDescription: "Telemetri üreten deploy ortamlarını (deploy_env) listele. `env` arg'ını list_services / get_service_health / list_problems'e vermeden ÖNCE çağır; uydurma ad sessizce boş döndürür.",
		Description: "List the deployment environments (spans' deploy_env — 'int', 'uat', 'prep', 'prod' style values) that are actually " +
			"emitting telemetry, busiest first. Use this BEFORE passing `env` to list_services, get_service_health or list_problems — " +
			"an invented env name silently narrows those reads to nothing. " +
			"Fixed enumeration window ON PURPOSE (the env set is deploy-stable and this scans spans): the most recent HOUR, widened to 24 hours " +
			"when you pass `pattern`, so a quiet-but-real env (a release branch deployed this morning) is findable by name. " +
			"window_s reports which of the two applied; `total` + truncated expose how many exist beyond the page.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Case-insensitive substring of the env name, e.g. \"uat\" or \"release\". Passing it also widens the scan 1h → 24h. Empty = the busiest envs of the last hour.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     200,
					"description": "Max env names to return. Default 50.",
				},
			},
		},
		// Salt-okunur; REST eşi GET /api/environments viewer'a açık.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listEnvironmentsArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			limit := clampLimit(a.Limit, 50, 200)
			// Sıfır from/to BİLİNÇLİ: pencere kelepçesi store'un
			// (envEnumWindow) — guided yol da (copilot_guided.go) tam
			// olarak böyle çağırıyor, iki yol aynı kümeyi görsün.
			names, total, err := d.Store.ListEnvironments(ctx, time.Time{}, time.Time{}, a.Pattern, limit)
			if err != nil {
				return nil, err
			}
			return environmentsPayload(names, total, a.Pattern), nil
		},
	}
}

// ─── list_clusters ─────────────────────────────────────────────

type listClustersArgs struct {
	RangeS int `json:"range_s,omitempty"`
}

func listClustersTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_clusters",
		ShortDescription: "Telemetride görülen k8s/OpenShift cluster adları. search_logs'a `cluster` vermeden önce çağır — tahmin edilen ad sessizce sıfır log döndürür. Pencere bilinçli 1 saat.",
		Description: "List the k8s / OpenShift cluster names observed in telemetry. Use this before passing `cluster` to search_logs, " +
			"and to answer 'which clusters are we running in' — a guessed cluster name silently returns zero logs. " +
			"Enumeration is deliberately bounded to the most recent HOUR even if you ask for more (the cluster set is infra-stable, and a wider " +
			"derive blew the query budget in production) — window_s reports the window actually used. " +
			"truncated=true means the 200-name cap was hit; type-narrow in the UI rather than trusting the list as complete.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     clusterEnumRangeS,
					"description": "Lookback seconds. Default and maximum 3600 (1h) — the store clamps anything wider anyway.",
				},
			},
		},
		// Salt-okunur; REST eşi GET /api/clusters viewer'a açık.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listClustersArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			windowS := clusterEnumWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			names, err := d.Store.ListClusters(ctx, from, to)
			if err != nil {
				return nil, err
			}
			return clustersPayload(names, windowS), nil
		},
	}
}

// ─── list_deploys ──────────────────────────────────────────────

// deployRow is one listed rollout. Deliberately NOT chstore.Deploy /
// RecentDeployEntry: the two store shapes differ (Version+TimeUnixNs vs
// Service+FirstSeenNs) and the model needs ONE shape with a readable
// timestamp.
type deployRow struct {
	Service string `json:"service"`
	Version string `json:"version"`
	TimeISO string `json:"time_iso"`
	// TimeUnixNs — get_deploy_diff/get_metrics_for_span'e kopyalanacak
	// çapa; model kendi tarih aritmetiğini kurmasın.
	TimeUnixNs int64 `json:"time_unix_ns"`
	SpanCount  int64 `json:"span_count"`
	// ImpactReady — deploy'un ÜSTÜNDEN ölçüm penceresi kadar zaman geçti
	// mi. false = get_deploy_diff'in "after" tarafı henüz yarım dolu,
	// deltalar yanıltır. Burada ETKİ HESAPLANMAZ (o get_deploy_diff'in
	// işi); bu yalnız "şimdi sormak anlamlı mı" bayrağıdır.
	ImpactReady bool `json:"impact_ready"`
}

// deployRows finishes the listing: newest first, limit applied, ISO
// timestamp + impact_ready derived from `now`. Saf — tablo testli
// (sıralama + tavan + eşik sınırı).
func deployRows(in []deployRow, now time.Time, limit int) []deployRow {
	out := append([]deployRow{}, in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeUnixNs > out[j].TimeUnixNs })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		t := time.Unix(0, out[i].TimeUnixNs)
		out[i].TimeISO = t.UTC().Format(time.RFC3339)
		out[i].ImpactReady = !now.Before(t.Add(deployImpactWindowS * time.Second))
	}
	return out
}

type listDeploysArgs struct {
	Service string `json:"service,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func listDeploysTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "list_deploys",
		ShortDescription: "Son deploy'lar: servis, sürüm, ilk görülme (ISO + unix ns), span sayısı, impact_ready. 'Dün gece ne çıktı' + get_deploy_diff'e verilecek sürümü BURADAN al. Etki hesaplamaz.",
		Description: "List recent deployments — per row: service, version, first-seen time (ISO + unix ns to copy verbatim), how many spans that " +
			"version produced, and impact_ready. Fleet-wide by default; pass service to get one service's rollout history. " +
			"This is how you answer 'what shipped last night / what changed recently' and how you FIND the version string to hand get_deploy_diff " +
			"(which otherwise silently picks the newest one for you). " +
			"It does NOT compute before/after impact — call get_deploy_diff for that. impact_ready=false means the deploy is younger than the " +
			"10-minute comparison window, so get_deploy_diff's 'after' side is still filling and its deltas would mislead. " +
			"Placeholder versions (0.0.1-SNAPSHOT, 'latest', 'dev'…) are filtered out store-side, so a pod restart is not listed as a deploy. " +
			"Deploy markers carry NO env dimension — do not claim a rollout was env-scoped. " +
			"Lookback floors at 6h (deploy questions are rarely answered by the last 30 minutes); window_s reports what was used.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (from list_services) for one service's rollout history. Empty = fleet-wide 'what changed'.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Lookback seconds. Default and minimum 21600 (6h); max 604800 (7d). Wider windows cost more CH time.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"description": "Max deploys to return, newest first. Default 20.",
				},
			},
		},
		// Salt-okunur; REST eşleri (GET /api/deploys/history,
		// /api/services/{name}/deploys) viewer'a açık.
		//
		// Filo yolunda GetRecentDeploys (guidedDeployBundle ile aynı
		// okuma) BİLİNÇLİ: range_s her zaman ŞİMDİye çapalı, yani
		// "[şimdi-lookback, şimdi] içinde en yeni N" tam olarak istenen
		// semantik. Geçmişte duran CUSTOM bir pencere sorulabilir olsaydı
		// GetDeploysInWindow gerekirdi (v0.9.435: /deploys sayfası bu
		// yüzden o varyanta geçti) — tool'un böyle bir arg'ı YOK, o yüzden
		// sapma değil.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a listDeploysArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			windowS := deployEnumWindowS(a.RangeS)
			limit := clampLimit(a.Limit, 20, 100)
			lookback := time.Duration(windowS) * time.Second
			now := nowOrAnchor(ctx)
			var rows []deployRow
			// storeCapped — okuma KENDİ tavanına dayandı mı (fleet: benim
			// geçtiğim limit; per-service: serviceDeploysSQL'in LIMIT 50'si).
			// Sessiz tavan bırakmamak için has_more'a girer.
			storeCapped := false
			service := strings.TrimSpace(a.Service)
			if service != "" {
				deps, err := d.Store.GetServiceDeploys(ctx, service, now.Add(-lookback), now)
				if err != nil {
					return nil, err
				}
				storeCapped = len(deps) >= serviceDeploysStoreCap
				for _, dep := range deps {
					rows = append(rows, deployRow{
						Service: dep.Service, Version: dep.Version,
						TimeUnixNs: dep.TimeUnixNs, SpanCount: int64(dep.SpanCount),
					})
				}
			} else {
				deps, err := d.Store.GetRecentDeploys(ctx, lookback, limit)
				if err != nil {
					return nil, err
				}
				storeCapped = len(deps) >= limit
				for _, dep := range deps {
					rows = append(rows, deployRow{
						Service: dep.Service, Version: dep.Version,
						TimeUnixNs: dep.FirstSeenNs, SpanCount: int64(dep.SpanCount),
					})
				}
			}
			return deploysPayload(rows, now, windowS, limit, storeCapped), nil
		},
	}
}

// ─── find_trace_by_span ────────────────────────────────────────

// spanLookupPayload turns a FindTraceIDBySpan result into the tool body.
// BULUNAMAMAK HATA DEĞİL (guidedSpanBundle, v0.9.548): hata döndürsek
// model ya döngüye girer ya "trace yok" diye kesin konuşur; dürüst kanıt
// found=false + olası nedenlerdir — biri de "pencereyi genişlet".
// Saf — tablo testli.
func spanLookupPayload(spanID, traceID string, windowS int) map[string]any {
	if traceID != "" {
		return map[string]any{
			"found":    true,
			"span_id":  spanID,
			"trace_id": traceID,
			"window_s": windowS,
			"next":     "get_trace ile waterfall'ı, get_logs_for_trace ile bu span'in loglarını al (span_id'yi geçir).",
		}
	}
	reasons := []string{
		"the span id was copied wrong or truncated (it must be 16 hex chars)",
		fmt.Sprintf("the span is OUTSIDE the searched window (last %ds) — retry with a larger range_s", windowS),
		"the span aged out of the retention window",
	}
	return map[string]any{
		"found":    false,
		"span_id":  spanID,
		"window_s": windowS,
		"reasons":  reasons,
		"note":     "Say this plainly to the operator; do NOT invent spans, timings or a trace id.",
	}
}

type findTraceBySpanArgs struct {
	SpanID string `json:"span_id"`
	RangeS int    `json:"range_s,omitempty"`
}

func findTraceBySpanTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "find_trace_by_span",
		ShortDescription: "Yapıştırılan çıplak 16-hex SPAN id → trace id. Katalogdaki başka hiçbir tool span id'siyle çalışmaz; önce çöz, sonra kaz. found=false iken trace id UYDURMA.",
		Description: "Resolve a bare SPAN id (16 hex chars) to the trace id that contains it. This is the pasted-id entry point: when the operator " +
			"hands you a span id — from a log line, an exemplar, an error report — nothing else in the catalogue can use it, because get_trace, " +
			"get_logs_for_trace and get_exemplar_traces all key on the TRACE id. Resolve first, then drill. " +
			"COST: span_id has no index, so this is a time-bounded column scan capped server-side at 8 seconds — keep range_s tight and only widen " +
			"after a narrow window came back found=false. " +
			"Not finding it is NOT an error: you get found=false plus the likely reasons (bad copy, outside the window, aged out of retention). " +
			"Never invent a trace id when found=false.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"span_id": map[string]any{
					"type":        "string",
					"description": "The span id as 16 hex chars (a 32-hex id is a TRACE id — pass that to get_trace directly). Required.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     spanScanMaxRangeS,
					"description": "Lookback seconds to scan. Default 1800 (30 min), max 86400 (24h) — this scan is unindexed, so the window IS the cost.",
				},
			},
			"required": []string{"span_id"},
		},
		// Salt-okunur nokta araması; in-app guided yolun (viewer'a açık
		// sohbet) tam olarak bu okumayı yapıyor.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a findTraceBySpanArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			spanID := strings.ToLower(strings.TrimSpace(a.SpanID))
			if spanID == "" {
				return nil, fmt.Errorf("span_id zorunlu (16 hex karakter)")
			}
			// ŞEKİL hatası ile BULUNAMAMA farklı şeyler: şekil hatası
			// tarama bile yapmadan self-correct edilebilir bir çağıran
			// hatasıdır (get_logs_for_trace precedent'i), bulunamama ise
			// dürüst kanıttır.
			if !isHexLen(spanID, 16) {
				if isHexLen(spanID, 32) {
					return nil, fmt.Errorf("span_id 16 hex olmalı — %q 32 hex, yani bir TRACE id; doğrudan get_trace'e geçir", a.SpanID)
				}
				return nil, fmt.Errorf("span_id must be 16 hex chars, got %q", a.SpanID)
			}
			windowS := spanScanWindowS(a.RangeS)
			from, to := rangeWindow(ctx, windowS)
			traceID, err := d.Store.FindTraceIDBySpan(ctx, spanID, from, to)
			if err != nil {
				return nil, err
			}
			return spanLookupPayload(spanID, traceID, windowS), nil
		},
	}
}
