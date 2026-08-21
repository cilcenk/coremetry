package mcptools

// Analiz tool'ları (v0.9.1146, AI Faz 3.3) — kataloğun üç BÜYÜK boşluğu.
//
// Faz 3.2 (v0.9.1141/1142) "arg kabul edip listesini vermeyen tool"
// sayısını sıfıra indirdi. Kalan boşluk arg değil SORU'ydu: model
// elindeki tool'larla üç soruya hiç cevap üretemiyordu ve üçü de bir
// olayın anlatısının merkezinde duruyor —
//
//   - "bu servisin YUKARISINDA/AŞAĞISINDA ne var" → get_topology
//     (servis grafiği; /topology + /servicegraph'ın okuduğu MV)
//   - "bu bozulursa BAŞKA KİM bozulur" → get_blast_radius
//     (yukarı-akış çağıranlar + cascade bayrağı)
//   - "log hacmi ZAMANLA nasıl değişti" → get_log_histogram
//     (severity kırılımlı kovalar; search_logs SATIR döndürür, ŞEKİL değil)
//
// Üçü de MEVCUT okuyucuyu köprüler — yeni SQL YOK:
//   ReadServiceTopologyAgg / ReadServiceTopologyAggForFocus (topology.go),
//   GetServiceBlastRadius (blast_radius.go),
//   logstore.Store.Histogram(…, "severity") (guidedLogErrorsBundle ile
//   AYNI çağrı). Böylece uç, guided yol ve MCP aynı sayıları konuşur.
//
// PENCERE DÜRÜSTLÜĞÜ — üç okumanın pencere semantiği farklı ve şema
// bunu BAŞTAN söyler (kabul-edilip-yok-sayılan arg YASAK, Faz 3.2
// doktrini):
//
//   - get_topology: topology_edges_5m 5 DAKİKALIK kovalarda yazılır ve
//     okuma pencereyi kova sınırlarına DIŞARI doğru yuvarlar
//     (toStartOfFiveMinute(from) … toStartOfFiveMinute(to)+5dk). Yani
//     efektif pencere istenenden iki uçta ~5dk geniş olabilir; range_s
//     bu yüzden kova boyutunun (300s) ALTINA düşürülemez — daha küçük
//     bir pencere vaat etmek yalan olurdu.
//   - get_blast_radius: GetServiceBlastRadius bucketStart'ı
//     from.Truncate(5dk) yapar, yani pencere BAŞA doğru kovaya oturur.
//     Tavan 24 saat — canlı REST ucunun (getServiceBlastRadius) tavanı
//     aynen; MV küçük ama çağıran kümesi 25'te kapılı.
//   - get_log_histogram: pencereyi çağıran verir, ama KOVA SAYISI
//     sınırlıdır (≤60) ve genişlik pencereden TÜRETİLİR. Bu, v0.9.287'nin
//     dersi: bucketSec'in DEĞERİNİ sınırlamak kova SAYISINI sınırlamaz
//     (1s × 30 gün = 2.6M kova) — sınırlanması gereken sayıdır.
//
// ENV ARG KARARI (v0.8.398 sözleşmesi) — tool BAŞINA, üçü de YOK ama
// üç FARKLI sebeple:
//
//   - get_topology: topology_edges_5m'de parent_env/child_env VAR ama
//     GÖSTERİM amaçlı — env ORDER BY'da olmadığı için aynı adlı servis
//     farklı ortamlarda ReplacingMergeTree dedup'ında BİRLEŞİR
//     (ServiceTopologyEdge yorumu, v0.5.410). Okuma env'e göre
//     SÜZEMEDİĞİ için arg uygulanamayacak bir filtre vaat ederdi;
//     annotation kenar satırında ifşa edilir, filtre olarak DEĞİL.
//   - get_blast_radius: service_callers_5m'in kolonları arasında env
//     YOK (store.go şeması) — süzülecek boyut mevcut değil.
//   - get_log_histogram: logstore.Filter'ın Env alanı VAR (v0.8.400) ama
//     ES tarafında alan keşfi başarısızsa filtre SESSİZCE uygulanmaz ve
//     "uygulanmadı" bayrağı Page'de taşınır — Histogram'ın dönüş şekli
//     ([]LogSeries) ise bayrak taşıyacak yer BIRAKMIYOR (v0.9.288 aynı
//     gerekçeyle partial'ı da taşıyamıyor). Yani env arg'ı burada
//     "süzdüm" diyip süzmemenin RAPOR EDİLEMEZ hâli olurdu.
//
// MinRole "" (viewer tabanı) — üçü de salt-okunur ve REST eşleri
// kapısız: GET /api/servicegraph, GET /api/services/{name}/blast-radius,
// GET /api/logs/timeseries. Her tool'da AÇIKÇA yazılı.
//
// BİLİNÇLİ SAPMA (get_topology): /topology sayfasının operatör-gizli
// gürültü kalıpları (v0.8.241, varsayılan kafka:log*/kafka:bsa*)
// UYGULANMIYOR. Kalıp yükleyici + matcher api paketinde yaşıyor
// (topology.go: topologyHiddenPatterns/hiddenNodeMatcher) ve api →
// mcptools import'u var, yani ters yön import DÖNGÜSÜ olur; matcher'ı
// buraya kopyalamak da aynı kuralın DÖRDÜNCÜ yazılışı olurdu
// (v0.9.1029'un dersi). Gizlemek yerine SÖYLÜYORUZ: tool açıklaması
// gürültü düğümlerinin görünebileceğini bildirir.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

// ─── paylaşılan sayı hijyeni ───────────────────────────────────

// mcpFloat — gövdeye giden ondalık: 2 haneye yuvarlanır ve SONLU
// OLMAYAN değer 0'a düşer.
//
// Yuvarlama bağlam bütçesi içindir (0.0033333333333 yerine 0). İkinci
// yarısı ise savunma: encoding/json NaN/±Inf'i marshal etmeyi REDDEDER,
// yani tek bozuk satır TÜM tool çağrısını hataya çevirir — bir kenarın
// p99'u yüzünden modelin elinden bütün graf gider. Bu depoda benzeri
// yandı (dizeye gömülü +Inf hem panele hem prompt'a gitmişti); orada
// PARÇAYI düşürmek doğru cevaptı, burada da alanı düşürüyoruz, cevabı
// değil. Saf — tablo testli.
func mcpFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// ─── get_topology ──────────────────────────────────────────────

// topoBucketS — topology_edges_5m'in kova boyutu. range_s'in TABANI:
// bundan küçük bir pencere okuma tarafında yine bir tam kovaya
// yuvarlanır, dolayısıyla "60 saniyeye baktım" demek yalan olurdu.
const topoBucketS = 300

// topoDefaultRangeS — /topology sayfasının varsayılanı (parseFromTo'nun
// time.Hour'u). Sohbetin 30dk'sı yerine bunu seçiyoruz: kenar kümesi
// deploy-kararlıdır ve 30dk'lık pencerede seyrek bir bağımlılık
// (gecelik batch, nadir bir kuyruk tüketicisi) sessizce kaybolur.
const topoDefaultRangeS = 3600

// topoEdgeCap — tool'un kenar tavanı. MV okuması 20000'e dek kenar
// döndürebilir; bu bir LLM bağlamı için anlamsız. 200 şema tavanı,
// 40 varsayılan.
const topoEdgeCap = 200

// topoFocusHops — odaklı okumada YÜRÜYÜŞ DERİNLİĞİ. Sabit 1 ve arg
// YOK: 1 hop, "upstream = beni çağıranlar / downstream = benim
// bağımlılıklarım" ayrımını TAM tutar. hops=2 ile gelen kenarların bir
// kısmı odağa hiç dokunmaz (komşunun komşusu) ve o iki kova artık
// yön anlamı taşımaz — modele iki anlamı karışmış bir liste vermek
// yerine "bir komşuya odaklanıp yeniden çağır" diyoruz.
const topoFocusHops = 1

// topoLabelCap — kenar başına taşınan operasyon etiketi sayısı. MV 5
// tutuyor; 3'ü modele "bu kenardan ne geçiyor" sorusunu cevaplatmaya
// yeter, distinct_labels kaçını görmediğini söyler.
const topoLabelCap = 3

// topologyWindowS — range_s → efektif saniye (varsayılan 1h, taban
// 300 = kova, tavan 7 gün). Saf, tablo testli.
func topologyWindowS(rangeS int) int {
	if rangeS <= 0 {
		return topoDefaultRangeS
	}
	if rangeS < topoBucketS {
		return topoBucketS
	}
	if rangeS > 7*86400 {
		return 7 * 86400
	}
	return rangeS
}

// topologyEdgeRow — gövdedeki bir kenar. chstore.ServiceTopologyEdge
// AYNEN geçirilmiyor: o şekil UI için 16 alan taşıyor (prior* kıyas
// alanları, extDisplay/extKind, distinctLabels…) ve alan adları
// camelCase. Model için gereken alt küme + snake_case + yuvarlanmış
// ondalık.
type topologyEdgeRow struct {
	Source string `json:"source"`
	Target string `json:"target"`
	// TargetKind — service | database | queue | external | internal.
	// MV'nin ham sözlüğü ('db') DEĞİL, api katmanının grafikte kullandığı
	// OTel sözlüğü ('database') — iki yüzey aynı düğüm için iki farklı
	// kelime kullanırsa model "db" ile "database"i ayrı şeyler sanır.
	TargetKind     string   `json:"target_kind"`
	Protocol       string   `json:"protocol,omitempty"`
	Calls          uint64   `json:"calls"`
	Errors         uint64   `json:"errors"`
	ErrorRatePct   float64  `json:"error_rate_pct"`
	AvgMs          float64  `json:"avg_ms"`
	P99Ms          float64  `json:"p99_ms"`
	TopLabels      []string `json:"top_labels,omitempty"`
	DistinctLabels uint64   `json:"distinct_labels,omitempty"`
	// SourceEnv/TargetEnv — ANNOTATION (v0.5.410). Filtre değil: aynı
	// adlı servis farklı ortamlarda MV dedup'ında birleşir, o yüzden
	// buradaki değer "bu kenarı bu ortam üretti" demez.
	SourceEnv string `json:"source_env,omitempty"`
	TargetEnv string `json:"target_env,omitempty"`
	// QueueCluster — yalnız kuyruk düğümü taşıyan kenarlarda dolu
	// olabilir (v0.9.1026). Boş kalması NORMAL: kolonun inmediği
	// kurulum, eski kova, ya da kuyruk olmayan kenar. Model bunu
	// "bilinmiyor" diye okumalı, '(default)' VARSAYMAMALI (v0.9.973).
	QueueCluster string `json:"queue_cluster,omitempty"`
}

// topologyEdgeRows — MV satırları → gövde satırları. calls DESC, tam
// tiebreak'li (v0.8.324 sözleşmesi: eşit calls'ta sıra oturumlar arası
// oynamasın), sonra tavan. İkinci dönüş değeri "tavan kesti mi".
// Saf — tablo testli. Girdi dilimi MUTASYONA UĞRAMAZ.
func topologyEdgeRows(in []chstore.ServiceTopologyEdge, limit int) ([]topologyEdgeRow, bool) {
	src := append([]chstore.ServiceTopologyEdge(nil), in...)
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Calls != src[j].Calls {
			return src[i].Calls > src[j].Calls
		}
		if src[i].ParentService != src[j].ParentService {
			return src[i].ParentService < src[j].ParentService
		}
		if src[i].ChildNode != src[j].ChildNode {
			return src[i].ChildNode < src[j].ChildNode
		}
		return src[i].Protocol < src[j].Protocol
	})
	trimmed := false
	if limit > 0 && len(src) > limit {
		src = src[:limit]
		trimmed = true
	}
	out := make([]topologyEdgeRow, 0, len(src))
	for _, e := range src {
		labels := e.TopLabels
		if len(labels) > topoLabelCap {
			labels = labels[:topoLabelCap]
		}
		out = append(out, topologyEdgeRow{
			Source:         e.ParentService,
			Target:         e.ChildNode,
			TargetKind:     topologyTargetKind(e.ChildNode, e.NodeKind),
			Protocol:       e.Protocol,
			Calls:          e.Calls,
			Errors:         e.Errors,
			ErrorRatePct:   mcpFloat(e.ErrorRate),
			AvgMs:          mcpFloat(e.AvgMs),
			P99Ms:          mcpFloat(e.P99Ms),
			TopLabels:      append([]string(nil), labels...),
			DistinctLabels: e.DistinctLabels,
			SourceEnv:      e.ParentEnv,
			TargetEnv:      e.ChildEnv,
			QueueCluster:   e.Cluster,
		})
	}
	return out, trimmed
}

// topologyTargetKind — hedefin türü HAM ID ÖNEKİNDEN, node_kind yalnız
// öneksiz id'ler için ipucu.
//
// Gerekçe v0.9.1028/1029'un ta kendisi: node_kind consumer kenarında
// YALAN söylüyor (kuyruk düğümü kenarın PARENT tarafında durabiliyor ve
// yazıcı onu 'service' diye etiketliyor), ham id öneki söylemiyor. api
// katmanı bu kararı nodeIdentityFromID'de verdi; burada aynı kararı
// veriyoruz — ama önek tablosunu KOPYALAMIYORUZ: yalnız türü
// türetiyoruz ve dizeyi soymuyoruz (model hedefi ham id'yle görsün,
// çünkü get_topology'ye geri geçirilecek olan o).
func topologyTargetKind(id, nodeKind string) string {
	switch {
	case strings.HasPrefix(id, "db:"):
		return "database"
	case strings.HasPrefix(id, "queue:"):
		return "queue"
	case strings.HasPrefix(id, "ext:"):
		return "external"
	}
	switch nodeKind {
	case "service":
		return "service"
	case "db":
		return "database"
	case "queue", "kafka", "messaging":
		return "queue"
	case "external":
		return "external"
	case "":
		return "service"
	}
	return "internal"
}

// splitTopologyEdges — odaklı okumanın YÖN ayrımı. upstream = odağı
// ÇAĞIRANLAR (hedef odak), downstream = odağın BAĞIMLILIKLARI (kaynak
// odak). Üçüncü kova defansif: hops=1 okuması her satırın bir ucunu
// odağa sabitler (store'daki `parent_service IN (…) OR child_node IN
// (…)` filtresi), yani boş kalması BEKLENİR — dolmuşsa okuma yolu
// değişmiş demektir ve gövdede sessizce kaybolmasın. Saf, tablo testli.
func splitTopologyEdges(rows []topologyEdgeRow, focus string) (up, down, other []topologyEdgeRow) {
	for _, r := range rows {
		switch {
		case r.Target == focus:
			up = append(up, r)
		case r.Source == focus:
			down = append(down, r)
		default:
			other = append(other, r)
		}
	}
	return up, down, other
}

// topologyEmptyReasons — kenar YOK. Boş sonuç HATA DEĞİL (find_trace_*
// emsali): hata döndürsek model ya döngüye girer ya "bu servis izole"
// diye kesin konuşur. Dürüst kanıt olası nedenlerdir; biri her zaman
// "pencere/ad" tarafında olmalı ki model kendini düzeltebilsin.
func topologyEmptyReasons(focus string) []string {
	if focus == "" {
		return []string{
			"the topology aggregate is empty for this window — the 5-minute aggregator (leader-locked worker) may not have written a bucket yet",
			"the window is narrower than the write cadence — widen range_s to at least 15 minutes",
			"no instrumented service-to-service calls happened in the window (a single-service install produces no edges)",
		}
	}
	return []string{
		fmt.Sprintf("%q is not the exact service name — confirm it with list_services (the aggregate keys on service.name verbatim)", focus),
		"the service genuinely has no instrumented neighbour in this window: an entry service has no caller, and a dependency that emits no spans produces no edge",
		"the window is too narrow or too recent — the aggregate is written one 5-minute bucket at a time, so a service that started minutes ago can be absent",
		"the traffic is there but the parent spans are not: an edge needs BOTH sides in the trace, so a caller without trace-context propagation never shows up as upstream",
	}
}

// topologyPayload — gövde üreticisi (saf, tablo testli). Zarf: pencere
// (istenen + kova granülü), sayım, truncated, ve YALNIZ gerçekten bir
// eksiklik varken not/reasons. Semantiğin geri kalanı tool
// AÇIKLAMASINDA durur — açıklama bir kez (tools/list), gövde her
// çağrıda gider.
func topologyPayload(focus string, windowS int, rows []topologyEdgeRow, trimmed bool) map[string]any {
	body := map[string]any{
		"window_s":        windowS,
		"window_bucket_s": topoBucketS,
		"count":           len(rows),
		"truncated":       trimmed,
	}
	if focus == "" {
		body["scope"] = "estate"
		body["edges"] = rows
	} else {
		body["scope"] = "service"
		body["service"] = focus
		body["hops"] = topoFocusHops
		up, down, other := splitTopologyEdges(rows, focus)
		body["upstream"] = up
		body["downstream"] = down
		body["upstream_count"] = len(up)
		body["downstream_count"] = len(down)
		if len(other) > 0 {
			body["unrelated_edges"] = other
			body["unrelated_note"] = "These edges touch NEITHER side of the focus service; the read path changed. Treat them as unattributed, do not narrate them as this service's dependencies."
		}
	}
	if len(rows) == 0 {
		body["reasons"] = topologyEmptyReasons(focus)
		body["note"] = "State plainly that no edges were found for this window and say which window you looked at; do NOT invent callers, dependencies or databases."
	}
	if trimmed {
		body["note"] = "The edge list was cut to the busiest ones; a quiet dependency may be missing. Narrow with `service` instead of raising `limit` when you need completeness."
	}
	return body
}

type getTopologyArgs struct {
	Service string `json:"service,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func getTopologyTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_topology",
		ShortDescription: "Servis grafı — kim kimi çağırıyor, her kenarda RED. `service` ile o servisin upstream (çağıranlar) + downstream (bağımlılıklar) komşuları, boşken en yoğun kenarlar. TEK hop.",
		Description: "Read the SERVICE GRAPH — who calls whom — with RED metrics on every edge: calls, errors, error rate, avg + p99 latency, the protocol " +
			"(http / rpc / db / kafka / internal) and the busiest operation labels riding that edge. " +
			"Pass `service` to get that service's DIRECT neighbours, split by direction: `upstream` = the services that CALL it, `downstream` = what it " +
			"depends on (other services plus infrastructure targets, which arrive prefixed `db:` / `queue:` / `ext:`). Leave `service` empty for the " +
			"busiest edges across the whole estate. " +
			"This is the tool for 'what is upstream/downstream of X', 'is the latency coming from a dependency', 'who writes to this Kafka topic', " +
			"'which services talk to that database'. ONE hop only — to walk further, call it again focused on a neighbour. " +
			"COST: reads the 5-minute topology pre-aggregate, never raw spans, so it is cheap to call repeatedly. " +
			"WINDOW: the aggregate is written in 5-minute buckets and the read snaps OUTWARD to bucket boundaries, so counts can include up to 5 minutes " +
			"beyond each end of the window you asked for (window_s = what you asked, window_bucket_s = the granularity). " +
			"A `ext:<name>` target means the callee produced NO server span in those traces — an uninstrumented component or a genuine third party; do not " +
			"describe it as a Coremetry service. " +
			"Environment (source_env / target_env) is an ANNOTATION, not a filter: the aggregate merges the same service across environments, so there is " +
			"no env argument here and you must never claim an edge was env-scoped. " +
			"The Topology page's operator-hidden noise patterns (logging / bsa-style kafka topics) are NOT applied here, so plumbing topics can appear as " +
			"`queue:` targets — do not present them as findings unless the operator asked about them. " +
			"An empty result is NOT an error: you get reasons instead.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type": "string",
					"description": "Exact service name (from list_services) to focus on — the answer comes back split into upstream / downstream. " +
						"Empty = the busiest edges in the estate. Infrastructure ids (db:… / queue:… / ext:…) are NOT valid here; focus on the service that talks to them.",
				},
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": 604800,
					"description": "Lookback seconds. Default 3600 (1h — the Topology page's default; 30 minutes hides an infrequent dependency). " +
						"Minimum effective value 300: the aggregate's bucket is 5 minutes, so anything narrower still reads a whole bucket.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     topoEdgeCap,
					"description": "Max edges to return, busiest first. Default 40, max 200 — a 1000-service estate has more edges than any answer can carry, so focus with `service` instead of raising this.",
				},
			},
		},
		// Salt-okunur; REST eşleri GET /api/servicegraph ve GET
		// /api/topology viewer'a açık (kapısız).
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getTopologyArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			service := strings.TrimSpace(a.Service)
			// ŞEKİL hatası ile BULUNAMAMA farklı şeyler (find_trace_by_span
			// emsali): altyapı id'si geçirmek taramaya bile girmeden
			// düzeltilebilir bir çağıran hatasıdır — MV'de bu id yalnız
			// child_node olarak var, parent olarak asla.
			if k := topologyTargetKind(service, ""); service != "" && k != "service" {
				return nil, fmt.Errorf("service %q bir altyapı düğümü (%s) — get_topology yalnız SERVİSE odaklanır; "+
					"o düğüme dokunan servisi odakla ya da service'i boş bırakıp filo kenarlarına bak", service, k)
			}
			windowS := topologyWindowS(a.RangeS)
			from, to := rangeWindow(windowS)
			limit := clampLimit(a.Limit, 40, topoEdgeCap)
			var (
				edges []chstore.ServiceTopologyEdge
				err   error
			)
			if service != "" {
				// Odaklı okuma store-side IN-filtreli (v0.9.366): filonun
				// calls-DESC ilk N kenarını çekip Go'da süzmek, 20k'yı aşan
				// kurulumda odağın SESSİZ bağımlılıklarını düşürüyordu.
				edges, err = d.Store.ReadServiceTopologyAggForFocus(ctx, from, to, service, topoFocusHops, limit)
			} else {
				edges, err = d.Store.ReadServiceTopologyAgg(ctx, from, to, limit)
			}
			if err != nil {
				return nil, err
			}
			rows, trimmed := topologyEdgeRows(edges, limit)
			// Okuma KENDİ tavanına dayandıysa da kesme var: store limit'i
			// benim limitim, yani len==limit sessiz tavan demek.
			if len(edges) >= limit {
				trimmed = true
			}
			return topologyPayload(service, windowS, rows, trimmed), nil
		},
	}
}

// ─── get_blast_radius ──────────────────────────────────────────

// blastDefaultRangeS / blastMaxRangeS — canlı REST ucunun (api.go
// getServiceBlastRadius) varsayılanı ve TAVANI birebir: 1 saat / 24
// saat. Tavanı burada da uyguluyoruz ki tool ucun ötesine geçen bir
// pencere vaat etmesin.
const (
	blastDefaultRangeS = 3600
	blastMaxRangeS     = 24 * 3600
)

// blastCallerCap — GetServiceBlastRadius'un SQL LIMIT'i (25). Store
// total döndürmüyor; "tam bu sayıda geldi" tek kesme sinyalimiz.
const blastCallerCap = 25

// blastWindowS — range_s → efektif saniye (varsayılan 1h, taban 300 =
// service_callers_5m kovası, tavan 24h). Saf, tablo testli.
func blastWindowS(rangeS int) int {
	if rangeS <= 0 {
		return blastDefaultRangeS
	}
	if rangeS < topoBucketS {
		return topoBucketS
	}
	if rangeS > blastMaxRangeS {
		return blastMaxRangeS
	}
	return rangeS
}

// blastCallerRow — bir yukarı-akış çağıran.
//
// Alan adı `had_open_problem_in_window` BİLİNÇLİ olarak uzun:
// chstore'daki hasOpenProblem "ŞU AN açık" gibi okunuyordu ve
// v0.9.1047 tam olarak bunu düzeltti (çözülmüş bir problemin
// penceresinde cascade bayrağı yanlış çıkıyordu). Modelin yanlış
// zaman kipinde konuşmaması için semantik ADIN İÇİNDE.
type blastCallerRow struct {
	Service                string  `json:"service"`
	Calls                  uint64  `json:"calls"`
	Errors                 uint64  `json:"errors"`
	RPS                    float64 `json:"rps"`
	ErrorRatePct           float64 `json:"error_rate_pct"`
	HadOpenProblemInWindow bool    `json:"had_open_problem_in_window"`
}

// blastEmptyReasons — çağıran YOK. Yine: boş sonuç hata değil, ve
// nedenlerin biri "bu servis GİRİŞ noktası" olmalı — filonun kenarındaki
// gateway'ler için doğru cevap tam olarak bu.
func blastEmptyReasons(service string) []string {
	return []string{
		fmt.Sprintf("%q is an ENTRY service — the traffic arrives from outside the instrumented estate (browser, gateway, cron), so there is no caller service to list", service),
		"its callers are not instrumented, or they do not propagate trace context, so no caller row was aggregated",
		"the service name is not exact — confirm it with list_services",
		"the window is too narrow or predates the caller aggregate's 14-day retention",
	}
}

// blastRadiusPayload — gövde üreticisi (saf, tablo testli). YÖN
// bilgisini alan adlarına ve `direction` alanına gömüyoruz: bu tool'un
// tek büyük yanlış-okuma riski, çağıranları bağımlılık sanmaktır.
func blastRadiusPayload(br chstore.BlastRadius, windowS int) map[string]any {
	callers := make([]blastCallerRow, 0, len(br.Callers))
	for _, c := range br.Callers {
		callers = append(callers, blastCallerRow{
			Service:                c.Service,
			Calls:                  c.Calls,
			Errors:                 c.Errors,
			RPS:                    mcpFloat(c.RPS),
			ErrorRatePct:           mcpFloat(c.ErrorRate),
			HadOpenProblemInWindow: c.HasOpenProblem,
		})
	}
	body := map[string]any{
		"service":            br.Service,
		"direction":          "upstream-callers",
		"window_s":           windowS,
		"window_bucket_s":    topoBucketS,
		"callers":            callers,
		"total_callers":      len(callers),
		"cascading_callers":  br.CascadingCallers,
		"total_rps":          mcpFloat(br.TotalRPS),
		"total_errors_per_s": mcpFloat(br.TotalErrorsPerSec),
		"truncated":          len(callers) >= blastCallerCap,
	}
	if len(callers) == 0 {
		body["reasons"] = blastEmptyReasons(br.Service)
		body["note"] = "Say plainly that no upstream caller was observed in this window; do NOT invent affected services or an impact count."
	}
	if len(callers) >= blastCallerCap {
		body["note"] = fmt.Sprintf("Only the %d busiest callers are returned; there is a long tail beyond them, so total_rps is a LOWER BOUND on the real upstream traffic.", blastCallerCap)
	}
	body["next"] = "Aşağı-akış bağımlılıkları için get_topology (downstream); bir çağıranın kendi RED'i için get_service_health; cascade bayrağı olan çağıranın problemi için list_problems."
	return body
}

type getBlastRadiusArgs struct {
	Service string `json:"service"`
	RangeS  int    `json:"range_s,omitempty"`
}

func getBlastRadiusTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_blast_radius",
		ShortDescription: "'Bu servis bozulursa kim etkilenir' — UPSTREAM çağıranlar, kaskad işaretiyle (çağıranın kendi problemi de açık mıydı). Bağımlılıklar için get_topology downstream.",
		Description: "Answer 'if this service is degraded, WHO ELSE is hurting' — the UPSTREAM callers of `service` in the window, each with calls, errors, " +
			"error rate, rps and had_open_problem_in_window (the cascade signal: that caller had its OWN Problem open during the same window, i.e. the " +
			"failure is propagating up the call graph). Cascading callers are listed FIRST, regardless of volume. " +
			"DIRECTION MATTERS: these are the services that CALL the inspected one — their requests fail when it fails. This is NOT the list of things the " +
			"service depends on; for that use get_topology and read `downstream`. " +
			"Use it to size an incident before writing an impact statement, and to decide whether a problem is a root cause or a symptom (many cascading " +
			"callers = you are probably looking at the root). " +
			"COST: reads the 5-minute caller pre-aggregate plus one small problems lookup — cheap. " +
			"BOUNDS: the 25 busiest callers, store-side; truncated=true means total_rps is a LOWER bound. The window snaps back to the 5-minute bucket " +
			"boundary, so counts can include up to 5 minutes before the start, and it is capped at 24 hours like the UI endpoint. " +
			"An empty caller list is NOT an error — an entry service legitimately has none, and you get reasons. Never invent affected services.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (from list_services) whose upstream impact you want. Required — a fleet-wide blast radius is not a meaningful question.",
				},
				"range_s": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": blastMaxRangeS,
					"description": "Lookback seconds. Default 3600 (1h), max 86400 (24h — the same ceiling the UI endpoint enforces). " +
						"Minimum effective value 300 (the aggregate's 5-minute bucket). For a problem, use roughly the problem's own duration.",
				},
			},
			"required": []string{"service"},
		},
		// Salt-okunur; REST eşi GET /api/services/{name}/blast-radius
		// viewer'a açık ve kök-neden yolu (rootcause.go) aynı okumayı
		// yapıyor.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getBlastRadiusArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			service := strings.TrimSpace(a.Service)
			if service == "" {
				return nil, fmt.Errorf("service zorunlu — önce list_services")
			}
			windowS := blastWindowS(a.RangeS)
			from, to := rangeWindow(windowS)
			br, err := d.Store.GetServiceBlastRadius(ctx, service, from, to)
			if err != nil {
				return nil, err
			}
			return blastRadiusPayload(br, windowS), nil
		},
	}
}

// ─── get_log_histogram ─────────────────────────────────────────

// logHistDefaultBuckets / logHistMaxBuckets — KOVA SAYISI sınırı.
// v0.9.287'nin dersi: bucketSec'in DEĞERİNİ kelepçelemek kova SAYISINI
// sınırlamaz (1s × 30 gün = 2.6M kova × 6 bant) — sınırlanması gereken
// sayıdır. Model genişlik değil SAYI seçer; genişlik pencereden türer.
const (
	logHistDefaultBuckets = 24
	logHistMaxBuckets     = 60
)

// logHistDefaultRangeS — 1 saat. Sohbetin 30dk varsayılanı yerine:
// "hata ne zaman başladı" sorusunun cevabı çoğunlukla son yarım saatin
// dışındadır ve 24 kovada 1 saat okunur bir şekil verir.
const logHistDefaultRangeS = 3600

// logHistWindowS — range_s → efektif saniye (varsayılan 1h, tavan 7
// gün = rangeWindow'un genel tavanı). Saf, tablo testli.
func logHistWindowS(rangeS int) int {
	if rangeS <= 0 {
		return logHistDefaultRangeS
	}
	if rangeS > 7*86400 {
		return 7 * 86400
	}
	return rangeS
}

// logHistBucketS — (pencere, istenen kova sayısı) → kova genişliği.
//
// TAVAN BÖLME (ceiling) bilinçli ve v0.9.287'nin aynı gerekçesi: aşağı
// yuvarlayan bölme kova sayısını istenen SAYININ ÜSTÜNDE bırakır
// (1800/24 = 75 tam, ama 1801/24 = 75.04 → 76 kova). Tavan bölme
// sayıyı gerçekten sınırlar. Saf, tablo testli — birim karışımı
// sınıfının kuralı: saniye girer, saniye çıkar; `buckets` bir SAYIDIR.
func logHistBucketS(windowS, buckets int) int {
	if buckets <= 0 {
		buckets = logHistDefaultBuckets
	}
	if buckets > logHistMaxBuckets {
		buckets = logHistMaxBuckets
	}
	if windowS <= 0 {
		windowS = logHistDefaultRangeS
	}
	b := (windowS + buckets - 1) / buckets
	if b < 1 {
		b = 1
	}
	return b
}

// logSeverityBandOrder — kanonik bant sırası. Hem ES (filters agg
// sırası) hem CH (multiIf) AYNI altı bandı üretiyor ama SIRALARI
// ayrışıyor: CH tarafı ilk-görülme sırasında döner. Aynı soruya iki
// backend'de farklı sıralı cevap vermek, bu depoda ayrı bir sınıf
// olarak yanmış (CH vs ES ayrışması) — sırayı burada sabitliyoruz.
// Sözlükte olmayan bir ad (operatörün özel seviyesi) alfabetik olarak
// sona düşer, DÜŞMEZ.
var logSeverityBandOrder = map[string]int{
	"ERROR": 0, "WARN": 1, "INFO": 2, "DEBUG": 3, "TRACE": 4, "OTHER": 5,
}

// logHistSeriesRow — bir bant. points sadece DOLU kovaları taşır
// (min_doc_count:1 / CH sparse), peak modelin "zirve neredeydi"
// sorusunu tek bakışta cevaplaması için.
type logHistSeriesRow struct {
	Band   string         `json:"band"`
	Total  int64          `json:"total"`
	Points []logHistPoint `json:"points"`
	Peak   *logHistPoint  `json:"peak,omitempty"`
}

type logHistPoint struct {
	T string `json:"t"` // ISO-8601 UTC, kova BAŞLANGICI
	V int64  `json:"v"`
}

// logHistSeriesRows — logstore serileri → gövde satırları: kanonik bant
// sırası, boş seriler düşer, toplam + zirve türetilir. Saf, tablo
// testli. `_total` (groupBy'sız tek seri) adı da geçebilir; bant
// sözlüğünde olmadığı için sona düşer ve OLDUĞU GİBİ görünür — bir
// backend severity kırılımı üretemediğinde model bunu görmeli.
func logHistSeriesRows(in []logstore.LogSeries) ([]logHistSeriesRow, int64) {
	src := append([]logstore.LogSeries(nil), in...)
	sort.SliceStable(src, func(i, j int) bool {
		ri, oki := logSeverityBandOrder[src[i].Name]
		rj, okj := logSeverityBandOrder[src[j].Name]
		if oki != okj {
			return oki // bilinen bant her zaman önce
		}
		if oki && ri != rj {
			return ri < rj
		}
		return src[i].Name < src[j].Name
	})
	out := make([]logHistSeriesRow, 0, len(src))
	var grand int64
	for _, s := range src {
		row := logHistSeriesRow{Band: s.Name, Points: make([]logHistPoint, 0, len(s.Points))}
		var peak *logHistPoint
		for _, p := range s.Points {
			if p.V <= 0 {
				continue // sparse sözleşmesi: dolu kova = veri
			}
			pt := logHistPoint{T: time.Unix(0, p.T).UTC().Format(time.RFC3339), V: p.V}
			row.Points = append(row.Points, pt)
			row.Total += p.V
			if peak == nil || p.V > peak.V {
				cp := pt
				peak = &cp
			}
		}
		if row.Total == 0 {
			continue // tamamen boş bant gövdeye girmez
		}
		row.Peak = peak
		grand += row.Total
		out = append(out, row)
	}
	return out, grand
}

// logHistEmptyReasons — hiç log yok. ES tarafında "boş" ile "yavaş"
// AYNI şekle düşüyor (bu dönüş tipi partial bayrağı taşımıyor), o
// yüzden nedenlerden biri MUTLAKA doğrulama yolunu söyler.
func logHistEmptyReasons(service string) []string {
	out := []string{
		"no log line matched in this window — widen range_s or drop the query",
	}
	if service != "" {
		out = append(out,
			fmt.Sprintf("the log documents do not carry %q as the service name: on an external log backend the workload name often lives on a different field (container name, app label) and the environment suffix may sit on the namespace instead", service))
	}
	out = append(out,
		"the query is narrower than you think — with the Elasticsearch backend it is a query_string with AND as the default operator, so every term must match",
		"the backend returned nothing because it was slow: this response shape carries NO partial flag, so confirm with search_logs (its response does) before concluding the logs are quiet")
	return out
}

// logHistogramPayload — gövde üreticisi (saf, tablo testli).
func logHistogramPayload(service, query, backend string, windowS, bucketS int, series []logstore.LogSeries) map[string]any {
	rows, grand := logHistSeriesRows(series)
	totals := make(map[string]int64, len(rows))
	for _, r := range rows {
		totals[r.Band] = r.Total
	}
	body := map[string]any{
		"window_s":    windowS,
		"bucket_s":    bucketS,
		"backend":     backend,
		"series":      rows,
		"band_totals": totals,
		"total":       grand,
		// Sözleşme alanı: BOŞ kova gövdeye girmez (ES min_doc_count:1 /
		// CH sparse çıktı). Adı "sparse" değil bu: model "sparse" görüp
		// "örneklenmiş" sanmasın — eksik kova, veri yokluğu DEĞİL sıfır
		// eşleşme demek.
		"empty_buckets_omitted": true,
	}
	if service != "" {
		body["service"] = service
	}
	if query != "" {
		body["query"] = query
	}
	if grand == 0 {
		body["reasons"] = logHistEmptyReasons(service)
		body["note"] = "Say plainly that no matching log line was found in this window; do NOT report zero errors as proof the service is healthy."
	}
	body["next"] = "Zirve kovadaki satırları görmek için search_logs (aynı service/query + severity_min=17); desen kırılımı için list_exception_groups; trace'e geçmek için search_traces."
	return body
}

type getLogHistogramArgs struct {
	Service string `json:"service,omitempty"`
	Query   string `json:"query,omitempty"`
	RangeS  int    `json:"range_s,omitempty"`
	Buckets int    `json:"buckets,omitempty"`
}

func getLogHistogramTool(d Deps) mcp.Tool {
	return mcp.Tool{
		Name:             "get_log_histogram",
		ShortDescription: "Log HACMİNİN zaman serisi, severity bandlarına ayrık — 'hata ne zaman başladı', 'artış mı sabit mi'. search_logs SATIR verir, bu ŞEKİL. Boş kova = eşleşme yok.",
		Description: "Log VOLUME over time, split into canonical severity bands — the SHAPE question search_logs cannot answer, because that tool returns " +
			"rows and this one returns a time series: 'when did the errors start', 'is this a spike or steady state', 'did the flood stop after the deploy'. " +
			"Bands are ERROR (FATAL folds into it), WARN, INFO, DEBUG, TRACE and OTHER (a level outside the vocabulary, or a document with no severity " +
			"field at all). Each band carries its buckets, its total and its PEAK bucket, so you can name the minute the surge began. " +
			"You choose the bucket COUNT, not the width — the width is derived from the window and echoed back as bucket_s. " +
			"COST: exactly ONE aggregation request against the log backend (Elasticsearch or ClickHouse — `backend` tells you which), with the house cost " +
			"guards. Do not loop it to build a wider picture: raise range_s once. " +
			"SPARSE: buckets with zero matches are OMITTED, so a missing bucket means 'nothing matched', not 'no data'. " +
			"PARTIALITY: this shape carries NO partial flag — on a slow or shard-degraded backend a DIP can be the timeout rather than a traffic drop. If a " +
			"dip carries your conclusion, confirm it with search_logs, whose response does report partial / shardsFailed. " +
			"There is no environment argument: on logs the env filter can silently fail to resolve and this shape has no room for the 'env not applied' " +
			"flag search_logs carries — narrow by service instead, and never claim the counts are env-scoped.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{
					"type":        "string",
					"description": "Exact service name (from list_services). Empty = the whole estate's log volume, which is the right call for 'is anything flooding right now'.",
				},
				"query": map[string]any{
					"type": "string",
					"description": "Optional free-text / structured narrowing applied BEFORE bucketing. With the Elasticsearch backend this is a Lucene " +
						"query_string (`level:error`, `timeout AND NOT healthcheck`) with AND as the default operator and leading wildcards rejected; with " +
						"the ClickHouse backend it is a case-insensitive substring match on the log body. Empty = all lines.",
				},
				"range_s": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"maximum":     604800,
					"description": "Lookback seconds. Default 3600 (1h), max 604800 (7d). The window IS the cost on a billion-document backend — start narrow, widen once if the shape is inconclusive.",
				},
				"buckets": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     logHistMaxBuckets,
					"description": "How many time buckets to split the window into. Default 24, max 60. The width is derived (bucket_s in the response) — you never pass a width.",
				},
			},
		},
		// Salt-okunur; REST eşi GET /api/logs/timeseries viewer'a açık ve
		// uygulama içi guided log kanıtı (guidedLogErrorsBundle) tam olarak
		// bu okumayı yapıyor.
		MinRole: "",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a getLogHistogramArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return nil, fmt.Errorf("decode args: %w", err)
				}
			}
			// Nil kontrolü AÇIK: nil bir somut tip arayüze sarıldığında
			// arayüz nil OLMAZ, ama burada LogStore hiç kurulmadıysa alan
			// gerçekten nil'dir ve çağrı panikler.
			if d.LogStore == nil {
				return nil, fmt.Errorf("log backend not configured")
			}
			windowS := logHistWindowS(a.RangeS)
			bucketS := logHistBucketS(windowS, a.Buckets)
			from, to := rangeWindow(windowS)
			service := strings.TrimSpace(a.Service)
			query := strings.TrimSpace(a.Query)
			// groupBy "severity" SABİT: bant sözlüğü iki backend'de de
			// kanonik (v0.8.377) ve tool'un vaadi bu kırılım. Serbest
			// bir groupBy, ES'te yalnız "service" ile eşleşiyor ve geri
			// kalan her değer sessizce tek `_total` serisine düşerdi.
			series, err := d.LogStore.Histogram(ctx, logstore.Filter{
				Service: service,
				Search:  query,
				From:    from,
				To:      to,
			}, bucketS, "severity")
			if err != nil {
				return nil, err
			}
			return logHistogramPayload(service, query, d.LogStore.Backend(), windowS, bucketS, series), nil
		},
	}
}
