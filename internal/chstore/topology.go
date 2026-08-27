package chstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TopologyEdge is one parent→child operation invocation aggregated
// over a time window. Used by the op-level depth view; the service-
// level view consumes ServiceTopologyEdge below.
type TopologyEdge struct {
	ParentService string `json:"parentService"`
	ParentOp      string `json:"parentOp"`
	ChildService  string `json:"childService"`
	ChildOp       string `json:"childOp"`
	Calls         uint64 `json:"calls"`
}

// GetTopologyEdges aggregates parent→child operation pairs from
// the spans table over [from,to]. Self-join on (trace_id, span_id)
// = (trace_id, parent_id). Capped at `limit` heaviest edges so an
// install with very high operation cardinality (each HTTP route a
// distinct op) still serves an answer.
func (s *Store) GetTopologyEdges(ctx context.Context, from, to time.Time, limit int) ([]TopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 50000
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
			p.service_name AS parent_service,
			p.name         AS parent_op,
			c.service_name AS child_service,
			c.name         AS child_op,
			count() AS calls
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name, name
			FROM spans
			WHERE time >= ? AND time <= ?
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= ? AND c.time <= ?
		  AND c.parent_id != ''
		  AND `+topoNoiseExcludeSQL("c.name")+`
		GROUP BY parent_service, parent_op, child_service, child_op
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopologyEdge
	for rows.Next() {
		var e TopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ParentOp,
			&e.ChildService, &e.ChildOp, &e.Calls); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ServiceTopologyEdge collapses the per-operation join into a
// service-level interaction with a protocol family. One edge per
// (parent_service, child_node, protocol) so the UI can draw
// "service A → service B via HTTP" and "service A → postgres via
// db" as two separate strands even when they share endpoints.
//
// TopLabels carries up to 5 distinct method+endpoint strings by
// frequency — the renderer shows TopLabels[0] inline on the edge
// and surfaces the rest on click-to-expand without a second
// round-trip. DistinctLabels is the global count, which lets the
// UI render "(N endpoints)" hints even when TopLabels truncates.
type ServiceTopologyEdge struct {
	ParentService  string   `json:"parentService"`
	ChildNode      string   `json:"childNode"`
	NodeKind       string   `json:"nodeKind"` // "service" | "db" | "queue" | "cache" | "external"
	Protocol       string   `json:"protocol"` // "http" | "rpc" | "kafka" | "db" | "internal"
	TopLabels      []string `json:"topLabels"`
	DistinctLabels uint64   `json:"distinctLabels"`
	Calls          uint64   `json:"calls"`
	// v0.5.393 — errors + error-rate per edge so the topology
	// page can tint hot edges red and surface (errors / calls)
	// in the tooltip. The errors column landed on
	// topology_edges_5m in v0.5.367; we now pipe it through to
	// the read path so the operator reads "is this edge
	// breaking?" directly off the graph rather than having to
	// click into the dependent service.
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"` // (errors / calls) * 100
	AvgMs     float64 `json:"avgMs"`     // window-wide avg ms (sum/calls)
	P99Ms     float64 `json:"p99Ms"`     // conservative window p99
	// v0.5.409 — known external SaaS / cloud annotation. When
	// NodeKind == "external" and the peer host matches the
	// external_catalogue, these carry the human-friendly display
	// name + category (payments / messaging / cdn / etc.) so the
	// frontend can render a colored badge. Empty when the peer
	// isn't in the catalogue — UI falls back to the raw
	// `ext:<peer>` label.
	ExtDisplay string `json:"extDisplay,omitempty"`
	ExtKind    string `json:"extKind,omitempty"`
	// v0.5.410 — environment annotation per side. Resolved at
	// aggregation time from deployment.environment /
	// service.namespace / k8s.namespace.name resource attrs.
	// Display-only — same-name service in different envs still
	// merges in the MV's ReplacingMergeTree dedup (env not in
	// ORDER BY); a strict per-env split needs a table rebuild
	// and is deferred. Empty when no env attr was present on
	// the underlying spans.
	ParentEnv string `json:"parentEnv,omitempty"`
	ChildEnv  string `json:"childEnv,omitempty"`
	// v0.9.1026 — kuyruk düğümünün messaging cluster'ı (v0.9.1025'te
	// yazılmaya başlandı). YALNIZ queue düğümü taşıyan kenarlarda dolu;
	// hangi TARAFIN kuyruk olduğu kenarı üreten pass'e göre değişir
	// (producer kenarında CHILD, consumer kenarında PARENT), o yüzden
	// API katmanı 'queue:' önekine bakarak yerleştiriyor.
	//
	// Boş kalması NORMAL ve beklenen bir hâl: kolonun inmediği kurulum,
	// v0.9.1025 öncesi yazılmış kovalar, ve kuyruk olmayan her kenar.
	// Tüketicisi bu hâli "cluster bilinmiyor" diye okumalı ve v0.9.972
	// katalog köprüsüne düşmeli — asla '(default)' VARSAYMAMALI (çok-
	// cluster kurulumda sessizce boş bir çekmece açardı, v0.9.973).
	Cluster string `json:"cluster,omitempty"`
	// v0.5.414 — prior-window comparison values for the
	// what-changed banner. Populated only when the API caller
	// asks for the compare=prior variant. Frontend derives the
	// delta + surfaces edges whose errorRate or p99 jumped ≥2×.
	PriorCalls  uint64  `json:"priorCalls,omitempty"`
	PriorErrors uint64  `json:"priorErrors,omitempty"`
	PriorAvgMs  float64 `json:"priorAvgMs,omitempty"`
	PriorP99Ms  float64 `json:"priorP99Ms,omitempty"`
}

// RootFlow describes one business-level entry point: the root
// span (kind=server, parent_id=”) groups under (service, op) and
// counts how many traces start there. Services carries the set
// of unique services those traces touch, in arbitrary order (use
// GetFlowTopology to recover the call-graph shape for one flow).
// P99Ns is the 99th-percentile root-span duration in the window —
// computed lazily by ComputeFlowsLatencyP99 and merged in at the
// API layer (v0.5.156). 0 = not yet computed / no samples.
type RootFlow struct {
	RootService string   `json:"rootService"`
	RootOp      string   `json:"rootOp"`
	TraceCount  uint64   `json:"traceCount"`
	Services    []string `json:"services"`
	P99Ns       uint64   `json:"p99Ns,omitempty"`
}

// GetRootFlows returns the top business flows by trace count over
// [from, to]. A flow is identified by (root_service, root_op);
// the typical examples are HTTP entry points (POST /login, POST
// /payment), Kafka consumer roots, and scheduled jobs. limit
// caps the number of flows returned so the UI list stays
// scannable. The companion Services slice is materialised via
// groupUniqArray so the operator can see "login flow involves:
// api-gateway, user-service, postgresql, redis" without opening
// each one.
func (s *Store) GetRootFlows(ctx context.Context, from, to time.Time, limit int) ([]RootFlow, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		WITH root_traces AS (
			SELECT trace_id, service_name AS root_service, name AS root_op
			FROM spans
			WHERE parent_id = '' AND time >= ? AND time <= ?
		)
		SELECT
			rt.root_service,
			rt.root_op,
			uniqExact(rt.trace_id) AS trace_count,
			groupUniqArrayArray(50)(arrayDistinct([sp.service_name])) AS services
		FROM root_traces AS rt
		GLOBAL INNER JOIN (
			SELECT trace_id, service_name
			FROM spans
			WHERE time >= ? AND time <= ?
		) AS sp
			ON sp.trace_id = rt.trace_id
		GROUP BY rt.root_service, rt.root_op
		ORDER BY trace_count DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RootFlow
	for rows.Next() {
		var f RootFlow
		if err := rows.Scan(&f.RootService, &f.RootOp, &f.TraceCount, &f.Services); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFlowTopology returns the service-level subgraph restricted
// to traces whose root span matches (rootService, rootOp). Same
// shape as GetServiceTopologyEdges so the renderer reuses one
// code path. Used by the flow-detail view.
func (s *Store) GetFlowTopology(ctx context.Context, from, to time.Time, rootService, rootOp string, limit int) ([]ServiceTopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	// Two passes mirroring GetServiceTopologyEdges, both filtered
	// to the trace-id set whose root matches the flow signature.
	// The CTE-style filter is materialised once per query so each
	// pass benefits from CH's GLOBAL IN dedup.
	rows, err := s.telemetryReadConn().Query(ctx, `
		WITH root_traces AS (
			SELECT trace_id FROM spans
			WHERE parent_id = ''
			  AND service_name = ? AND name = ?
			  AND time >= ? AND time <= ?
		),
		multiIf(
			c.db_system  != '', 'db',
			c.msg_system != '', 'kafka',
			c.rpc_system != '', 'rpc',
			c.http_method != '', 'http',
			'internal'
		) AS proto,
		multiIf(
			c.http_method != '', concat(c.http_method, ' ',
				if(c.http_route != '', c.http_route, c.name)),
			c.rpc_method  != '', c.rpc_method,
			c.db_system   != '', concat(c.db_system, ' ', c.name),
			c.msg_system  != '', concat(c.msg_system, ' ', c.name),
			c.name
		) AS label
		SELECT
			p.service_name AS parent_service,
			c.service_name AS child_service,
			proto AS protocol,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count() AS calls
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name
			FROM spans
			WHERE time >= ? AND time <= ?
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.trace_id GLOBAL IN (SELECT trace_id FROM root_traces)
		  AND c.time >= ? AND c.time <= ?
		  AND c.parent_id != ''
		  AND p.service_name != c.service_name
		GROUP BY parent_service, child_service, protocol
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 60,
		         join_algorithm = 'grace_hash',
		         max_bytes_in_join = 2000000000,
		         `+s.queryMemSetting(flowTopologyMemory)+`,
		         distributed_product_mode = 'global'`,
		rootService, rootOp, from, to,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	var out []ServiceTopologyEdge
	for rows.Next() {
		var e ServiceTopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			rows.Close()
			return nil, err
		}
		e.NodeKind = "service"
		// v0.5.407 — templating runs post-Scan so the stored
		// labels stay raw (no MV migration), only the rendered
		// edges show templated forms. Dedupe collapses concrete-
		// id variants that map to the same template.
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Infra pass — same filter, db/msg/peer destinations.
	infra, err := s.telemetryReadConn().Query(ctx, `
		WITH root_traces AS (
			SELECT trace_id FROM spans
			WHERE parent_id = ''
			  AND service_name = ? AND name = ?
			  AND time >= ? AND time <= ?
		),
		multiIf(
			db_system  != '', concat('db:',    db_system),
			msg_system != '', concat('queue:', msg_system),
			peer_service != '' AND kind = 'client', concat('ext:', peer_service),
			''
		) AS child,
		multiIf(
			db_system  != '', 'db',
			msg_system != '', 'kafka',
			peer_service != '', 'http',
			''
		) AS proto,
		multiIf(
			db_system  != '', 'db',
			msg_system != '', 'queue',
			peer_service != '', 'external',
			''
		) AS kind_out,
		multiIf(
			http_method != '', concat(http_method, ' ',
				if(http_route != '', http_route, name)),
			db_system   != '', name,
			msg_system  != '', name,
			name
		) AS label
		SELECT
			service_name AS parent_service, child, proto, kind_out,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count() AS calls
		FROM spans
		WHERE trace_id GLOBAL IN (SELECT trace_id FROM root_traces)
		  AND time >= ? AND time <= ?
		  AND child != ''
		GROUP BY parent_service, child, proto, kind_out
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 25,
		         distributed_product_mode = 'global'`,
		rootService, rootOp, from, to,
		from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer infra.Close()
	for infra.Next() {
		var e ServiceTopologyEdge
		if err := infra.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.NodeKind, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			return nil, err
		}
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		out = append(out, e)
	}
	return out, infra.Err()
}

// EdgeInstance is one (peer_service) bucket for an infra edge —
// the actual host / cluster behind a `db:postgresql` or
// `queue:kafka` node. Drives the EdgeDetailPanel "per-instance"
// expand in topology so the operator can see which postgres
// instance is hot without leaving the diagram.
type EdgeInstance struct {
	Instance string  `json:"instance"`
	Calls    uint64  `json:"calls"`
	AvgMs    float64 `json:"avgMs"`
	P99Ms    float64 `json:"p99Ms"`
}

// GetEdgeInstances returns the peer_service breakdown for one
// (parentService, system, kind) edge over [from, to]. Bounded by
// the spans (service_name, time) primary key + filtered by
// db_system / msg_system so the scan stays tight even at
// billions of spans/day. Limit caps the buckets — 50 hosts is
// more than enough for any realistic per-service db/queue fan-out.
//
// kind: "db" → filter by db_system; "queue" → filter by msg_system.
// Returns empty slice when nothing matches (empty window).
func (s *Store) GetEdgeInstances(ctx context.Context, parentService, system, kind string, from, to time.Time, limit int) ([]EdgeInstance, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// v0.9.1318 (entity-model A3 / Ç10) — instance ifadesi ARTIK kind'a
	// göre seçiliyor.
	//
	// Önceden her iki kind da tek basamaklı `coalesce(nullIf(peer_service,
	// ''), 'unknown')` kullanıyordu. db kenarları için bu, db_summary_5m'in
	// ALTI basamaklı kimliğinin BİRİNCİ basamağıydı: peer_service'i boş
	// bırakan her kurulumda (ölçüldü — lokal `clickhouse`, 53.692 span)
	// panel tek bir 'unknown' kovası gösteriyordu, oysa MV o instance'ı
	// `coremetry-monolithic` diye adlandırmıştı. Yani "şu db düğümünün
	// instance'ları" paneli, açıldığı düğümle ÇELİŞİYORDU.
	//
	// queue BİLİNÇLİ olarak peer_service'te kalıyor: bir kuyruk kenarının
	// instance'ı broker'dır ve dbInstanceExpr'in db.host/db.name
	// basamakları orada anlamsızdır. msgClusterExpr'e geçirmek de yanlış
	// olurdu — o messaging CLUSTER'ını çözer, broker'ı değil; düğüm adı
	// zaten cluster'ı taşıyor (topoQueueClusterSQL).
	var sysCol, instanceExpr string
	switch kind {
	case "db":
		sysCol, instanceExpr = "db_system", dbInstanceExpr
	case "queue":
		sysCol, instanceExpr = "msg_system", `coalesce(nullIf(peer_service, ''), 'unknown')`
	default:
		return []EdgeInstance{}, nil
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
			`+instanceExpr+` AS instance,
			toUInt64(count())                              AS calls,
			toFloat64(avg(duration)) / 1e6                 AS avg_ms,
			toFloat64(quantile(0.99)(duration)) / 1e6      AS p99_ms
		FROM spans
		WHERE service_name = ?
		  AND `+sysCol+` = ?
		  AND time >= ? AND time <= ?
		GROUP BY instance
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         distributed_product_mode = 'global'`,
		parentService, system, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EdgeInstance{}
	for rows.Next() {
		var e EdgeInstance
		if err := rows.Scan(&e.Instance, &e.Calls, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WriteTopologyBucket aggregates the service-level topology for
// one 5-min window and inserts the result rows into
// topology_edges_5m. Two passes (cross-service join + infra
// detection), each INSERT ... SELECT in a single CH round-trip.
//
// Idempotent: re-running over the same bucket inserts new rows
// with the same primary key (time_bucket, parent_service,
// child_node, node_kind, protocol). ReplacingMergeTree(version)
// dedupes them at read time by keeping the highest version. A
// background goroutine in internal/topology calls this every
// 5 minutes; the API never invokes the heavy join directly.

// topoEnvChainSQL is the ONE env-derivation chain every topology pass
// uses (v0.8.380, audit-found bug: three passes read only the legacy
// deployment.environment attr and ignored the typed deploy_env column,
// so deployment.environment.name emitters — the operator's int/uat/
// prep test envs — got no env chip at all; a fourth pass, the
// queue→consumer edge, had the same miss). deploy_env leads: ingest
// (v0.8.379) populates it for BOTH semconv spellings; the raw-attr
// lookups remain for rows ingested before that fix, then the
// namespace fallbacks. prefix qualifies columns in joined scopes
// ("c." / ""). Pinned by TestTopoEnvChainSQL.
func topoEnvChainSQL(prefix string) string {
	return `coalesce(
				nullIf(` + prefix + `deploy_env, ''),
				nullIf(` + prefix + `res_values[indexOf(` + prefix + `res_keys, 'deployment.environment.name')], ''),
				nullIf(` + prefix + `res_values[indexOf(` + prefix + `res_keys, 'deployment.environment')], ''),
				nullIf(` + prefix + `res_values[indexOf(` + prefix + `res_keys, 'service.namespace')], ''),
				nullIf(` + prefix + `res_values[indexOf(` + prefix + `res_keys, 'k8s.namespace.name')], ''),
				''
			)`
}

// topoNoiseExcludeSQL — yayın (broadcast) desenli trafiğin topoloji
// kenarlarından dışlanması (v0.9.67, operatör direktifi): config-
// server'ın TÜM deployment'lara yayınladığı cache-refresh mesajları
// (örn. *.kafka.core.cache.refresh.<env> topic'i) her servisi
// birbirine bağlı gösteriyordu — yanlış all-to-all graf. Desen
// jenerik: verilen kolonda 'cache.refresh' / 'cache_refresh' geçen
// satır kenar üretmez. Hem span adına hem messaging destination'a
// uygulanır; üç WriteTopologyBucket pass'i + op-bucket ikizi +
// ad-hoc GetTopologyEdges aynı yardımcıyı kullanır.
func topoNoiseExcludeSQL(col string) string {
	return `(positionCaseInsensitive(` + col + `, 'cache.refresh') = 0
	  AND positionCaseInsensitive(` + col + `, 'cache_refresh') = 0)`
}

// topoQueueClusterSQL — kuyruk düğümünün messaging CLUSTER'ı (v0.9.1025).
//
// Zincir TÜRETİLMİYOR: identity.go'daki `msgClusterExpr` sabiti AYNEN
// kullanılıyor, çünkü messaging_summary_5m / messaging_caller_summary_5m
// MV'leri de tam olarak o zinciri materialize ediyor. Ayrışma SESSİZ bir
// kırılmadır ve bu özelliğin tam kalbinden vurur: topoloji düğümünden
// kurulan derin link, /messaging çekmecesinin (system, cluster,
// destination) üçlüsüyle TAM EŞİTLİKLE eşleşmezse çekmece BOŞ açılır —
// hata değil, cevapsızlık (v0.9.973'ün teşhis ettiği sınıf).
// TestTopoQueueClusterMirrorsMessagingMV bu aynılığı çiviler.
//
// msg_system guard'ı bir mikro-optimizasyon değil: coalesce dört
// indexOf() dizi taraması demek ve infra pass'i HER span'i (yalnız
// messaging olanları değil) satır satır geçiyor. Lokal ölçümde messaging
// oranı %6,7 — guard olmadan tarama ~15× daha çok satırda koşardı.
// Guard tüketici pass'inde WHERE sayesinde zaten daima doğru; yine de
// BİREBİR aynı metin yazılıyor, çünkü iki pass'in "MUST mirror"
// sözleşmesi ancak metin aynıysa test edilebilir.
func topoQueueClusterSQL() string {
	return `if(msg_system != '', ` + msgClusterExpr + `, '')`
}

// topoJoinMemBudget — grace-hash spill eşiği (max_bytes_in_join), üç
// topoloji JOIN pass'inin ortak değeri (v0.9.1190, operatör-bildirimli
// prod 241).
//
// Canlı arıza: cross-service pass remote shard'da "would use 8.00 GiB
// (attempt to allocate chunk of 2.50 GiB), maximum: 7.45 GiB" ile öldü.
// 7.45 GiB bizim kendi tavanımız (heavyScanMemory = 8e9); 2.5 GiB'lık tek
// parça ise join hash tablosunun RESIZE'ı. Eski eşik 4e9 idi ve kusur tam
// buradaydı: join 3.7 GiB'a kadar BÜYÜMEYE HAKLI sayılıyordu, ama tablo
// ~2 GiB'a vardığında bir sonraki resize TEK seferde ~2.5 GiB isteyip sol
// blokların + aggregation state'lerinin üstüne binince toplam tavanı
// deliyordu. Spill eşiği tavana bu kadar yakın olunca grace'in sigortası
// HİÇ ateşlenemeden query ölüyor — query_memory.go'nun kendi cümlesi:
// "a spill threshold at or above the cap can never fire".
//
// 1.5e9: tablo ~1.4 GiB'da spill'e döner, yani en kötü tek resize da o
// mertebede kalır; 8 GB zarfın kalanı sol blokların ve aggregation'ın.
// Değer heavyScanMemory'nin ÇEYREĞİNİN altında tutulmalı —
// topology_mem_test.go bu oranı çiviler ki tavanı yükselten biri eşiği
// sessizce yeniden tavana yaklaştırmasın.
const topoJoinMemBudget int64 = 1_500_000_000

// topoJoinMemSettings — üç JOIN pass'inin ortak bellek disiplini. TEK
// kaynak: pass'lerden biri düzelirken diğerinde eski üçlünün kalması,
// aggregator sıradaki pass'e geçtiği an aynı 241'i oradan üretir (bu
// vakada olan tam buydu — cross-service düşünce op-bucket hiç koşmamıştı).
//
// grace_hash_join_initial_buckets = 16: tablo tek parça büyüyüp baskı
// altında bölünmek yerine BAŞTAN 16 parçaya bölünür — gözlenen arızanın
// yolu tam olarak "büyü, sonra dev resize" idi; ön-bölme o yolu kapatır.
func (s *Store) topoJoinMemSettings() string {
	return `join_algorithm = 'grace_hash',
		         grace_hash_join_initial_buckets = 16,
		         max_bytes_in_join = ` + strconv.FormatInt(topoJoinMemBudget, 10) + `,
		         ` + s.queryMemSetting(heavyScanMemory)
}

func (s *Store) WriteTopologyBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)

	// v0.9.1025 — kuyruk düğümünün cluster'ı, probe'a BAĞLI olarak yazılır.
	// Koşulsuz yazsaydık kolonun henüz inmediği boot'ta (küme kipinde DDL
	// ertelemesi, v0.9.614) her bucket code 47 ile ölür ve topoloji grafiği
	// tamamen boş kalırdı — yani "yeni özellik çalışmıyor" değil, "graf
	// gitti". Üç parça birlikte açılıp kapanır; ikisi açık biri kapalı hâl
	// kolon-sayısı uyuşmazlığı demektir.
	//
	// Kolon listenin SONUNA ekleniyor: bağlı argümanlar (`?`) SIRAYLA
	// eşleşiyor ve eklenen ifade hiç placeholder içermiyor, dolayısıyla
	// mevcut arg sırası olduğu gibi kalıyor.
	clusterCol, clusterInner, clusterOuter := "", "", ""
	if s.hasTopoClusterCol {
		clusterCol = ", cluster"
		clusterInner = ",\n\t\t\t\t" + topoQueueClusterSQL() + " AS msg_cluster"
		// any(): parent_env/child_env ile aynı sözleşme. Cluster GROUP BY'a
		// GİRMEZ — girseydi tek bir aggregation koşusu dedup anahtarı dışında
		// ayrışan iki satır üretir ve ReplacingMergeTree FINAL okumasında
		// birini SİLERDİ (calls kaybı). Annotation olarak sayılar tam kalır,
		// yalnız etiket çok-cluster hâlinde keyfîdir.
		clusterOuter = ",\n\t\t\tany(msg_cluster) AS cluster"
	}

	// Cross-service pass — service A → service B via http/rpc.
	//
	// Memory note: the right side of the join (`p`) is column-
	// projected to (trace_id, span_id, service_name) and pre-
	// filtered to the bucket window in a subquery so CH doesn't
	// load the whole spans row block into the hash side. With
	// join_algorithm='grace_hash' the hash table spills to disk
	// past the per-query limit, so a 1B-span 5-min slice still
	// fits even on a modest 4-8 GB CH. allow_experimental_analyzer
	// off keeps grace_hash on the legacy planner where it's
	// stable across 23.x→24.x.
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_edges_5m
			(time_bucket, parent_service, child_node, node_kind,
			 protocol, top_labels, distinct_labels, calls,
			 sum_duration_ns, p99_ms, errors,
			 parent_env, child_env, version)
		WITH
			multiIf(
				c.db_system  != '', 'db',
				c.msg_system != '', 'kafka',
				c.rpc_system != '', 'rpc',
				c.http_method != '', 'http',
				'internal'
			) AS proto,
			multiIf(
				c.http_method != '', concat(c.http_method, ' ',
					if(c.http_route != '', c.http_route, c.name)),
				c.rpc_method  != '', c.rpc_method,
				c.db_system   != '', concat(c.db_system, ' ', c.name),
				c.msg_system  != '', concat(c.msg_system, ' ', c.name),
				c.name
			) AS label,
			-- v0.5.410 — per-span env derivation. Same coalesce
			-- chain across child + parent so the operator's
			-- prod/stage chip reads the same way regardless of
			-- which side the env came from.
			-- v0.8.380 (audit-found): the typed deploy_env column leads
			-- the chain — it is populated for BOTH semconv spellings at
			-- ingest (v0.8.379), while the raw attr lookup below only
			-- knew the legacy key and missed
			-- deployment.environment.name emitters entirely. The new
			-- spelling is also added for pre-v0.8.379 rows.
			`+topoEnvChainSQL("c.")+` AS c_env
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			p.service_name        AS parent_service,
			c.service_name        AS child_node,
			'service'             AS node_kind,
			proto                 AS protocol,
			topK(5)(label)        AS top_labels,
			toUInt32(uniqExact(label)) AS distinct_labels,
			toUInt64(count())     AS calls,
			toUInt64(sum(c.duration)) AS sum_duration_ns,
			-- v0.9.1190 — quantileExact'ten TDigest'e (üç yazıcı pass'te
			-- birden). Exact, grup başına TÜM süreleri bellekte tutar; prod
			-- ölçeğinde sıcak bir kenarın 5 dakikası milyonlarca değer demek
			-- ve hepsi JOIN'le AYNI 8 GB zarfın içinde. /clickhouse-schema
			-- kuralı zaten net: ~1M satırın üstünde quantile() değil TDigest
			-- (≤%2 hata). p99_ms düz Float64 kolonu — değer ~%2 oynayabilir;
			-- bu, her 5 dakikada bir 241 ile kovayı tamamen KAYBETMEKTEN
			-- (grafikte delik) ölçülemeyecek kadar ucuz bir bedel.
			toFloat64(quantileTDigest(0.99)(c.duration)) / 1e6 AS p99_ms,
			-- v0.5.367 — per-edge error count powers /api/service-graph
			-- ErrorRate reads from the MV (no more raw-spans self-join).
			toUInt64(countIf(c.status_code = 'error')) AS errors,
			-- v0.5.410 — env per side. any() picks an arbitrary
			-- representative within the bucket; cardinality of env
			-- per (service, 5min) is typically 1 so the pick is
			-- stable for the operator's eye.
			any(p.env)            AS parent_env,
			any(c_env)            AS child_env,
			toUInt64(?)           AS version
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name,
			       `+topoEnvChainSQL("")+` AS env
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= toDateTime(?, 'UTC') AND c.time < toDateTime(?, 'UTC')
		  AND c.parent_id != ''
		  AND p.service_name != c.service_name
		  AND `+topoNoiseExcludeSQL("c.name")+`
		GROUP BY parent_service, child_node, protocol
		SETTINGS max_execution_time = 180,
		         `+s.topoJoinMemSettings()+`,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology bucket cross-service pass: %w", err)
	}

	// RUNS_ON pass (v0.10.93, dikey eksen dilim ①) — service → k8s node.
	//
	// Üç pass'in aksine JOIN'SİZ: kimlik span satırının KENDİ
	// res_keys'inde (k8s.node.name), ebeveyn-çocuk span çifti yok.
	// Kaynak k8sattributes (v0.10.92 opt-in) ya da üreticinin kendi
	// resource'u; alan akmıyorsa sonuç BOŞ KÜMEDİR — pass 0 satır yazar
	// ve hiçbir okuma bozulmaz (boş küme kaybolur, sıfır olmaz — burada
	// bilinçli ve zararsız).
	//
	// top_labels bu kenarda POD adlarını taşır (topK 5): operatörün
	// "bu node'da servisin hangi pod'ları" sorusu etikete sığıyor;
	// distinct_labels = pod sayısı. protocol='runs_on' — DDL yorumundaki
	// http|rpc|db|kafka|internal listesi çağrı kenarlarını sayar, bu
	// kenar çağrı değil yerleşim; okuyucular türü child_node önekinden
	// (nodeIDPrefix) tanır, TopoCallEdgeFilterSQL dışlar.
	// Per-row hesaplar İÇ SELECT'te (v0.9.186 analyzer-portable kuralı).
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_edges_5m
			(time_bucket, parent_service, child_node, node_kind,
			 protocol, top_labels, distinct_labels, calls,
			 sum_duration_ns, p99_ms, errors,
			 parent_env, child_env, version)
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			parent_service,
			concat('`+nodeIDPrefix+`', node_name) AS child_node,
			'`+NodeKindNode+`'   AS node_kind,
			'runs_on'            AS protocol,
			topK(5)(pod)         AS top_labels,
			toUInt32(uniqExact(pod)) AS distinct_labels,
			toUInt64(count())    AS calls,
			toUInt64(sum(duration)) AS sum_duration_ns,
			toFloat64(quantileTDigest(0.99)(duration)) / 1e6 AS p99_ms,
			toUInt64(countIf(status_code = 'error')) AS errors,
			any(p_env)           AS parent_env,
			''                   AS child_env,
			toUInt64(?)          AS version
		FROM (
			SELECT
				service_name AS parent_service,
				res_values[indexOf(res_keys, 'k8s.node.name')] AS node_name,
				res_values[indexOf(res_keys, 'k8s.pod.name')]  AS pod,
				duration, status_code,
				`+topoEnvChainSQL("")+` AS p_env
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
			  AND service_name != ''
			  AND has(res_keys, 'k8s.node.name')
		)
		WHERE node_name != ''
		GROUP BY parent_service, node_name
		SETTINGS max_execution_time = 60`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology bucket runs-on pass: %w", err)
	}

	// Infra pass — service → db/queue/external.
	// v0.5.408 — DB / queue child_node now includes the host
	// instance suffix (e.g. `db:postgres@10.0.1.5` or
	// `db:postgres@orders-rds.aws.amazonaws.com`). Datadog /
	// Honeycomb / Dynatrace separate instances of the same DB
	// system because operationally they're different
	// destinations — different replicas, different availability,
	// different latency.
	//
	// v0.9.1318 (A3/Ç10) — bu yorum ÖNCEDEN "db_summary_5m'in
	// kullandığı AYNI coalesce zinciri" diyordu ve parantez içinde üç
	// basamak sayıyordu; MV ise ALTI basamak tarıyordu. Sözleşme
	// iddia edilmiş ama kurulmamıştı — ve tam olarak yorumun vaat
	// ettiği yerde kırılıyordu. Artık db dalı paylaşılan
	// dbInstanceExpr sabitini (identity.go) kullanıyor, queue ve
	// external dalları infra_host'ta KALIYOR: bir Kafka broker'ı ya da
	// bir HTTP peer'ı db.host/db.name/service_name basamaklarıyla
	// adlandırmak yanlış olurdu.
	// External peer hosts keep the prior `ext:<service>` shape
	// since peer_service IS the canonical external name.
	// v0.9.186 — analyzer-portable restructure (prod CH 26.2 code 60 fix).
	// Eski biçimde per-row WITH-alias zinciri (infra_host/unanswered/child…)
	// dış WITH clause'daydı; CH 26.x yeni analyzer'ı GLOBAL NOT IN
	// alt-sorgusunun `FROM spans`'ini o WITH scope'uyla karıştırıp remote
	// shard'da "Unknown table expression identifier 'spans'" (code 60)
	// veriyordu (allow_experimental_analyzer=0 ise code 215 — o yüzden
	// analyzer kapatma değil). Çözüm: tüm per-row hesaplar İÇ SELECT'te
	// materialize edilir, dış sorgu düz kolonlarla aggregate eder — WITH
	// scope karışıklığı ortadan kalkar. Çıktı iki analyzer modunda da
	// birebir aynı (24.8 single+distributed doğrulandı; 26.2 reproduce).
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_edges_5m
			(time_bucket, parent_service, child_node, node_kind,
			 protocol, top_labels, distinct_labels, calls,
			 sum_duration_ns, p99_ms, errors,
			 parent_env, child_env, version`+clusterCol+`)
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			parent_service,
			child                AS child_node,
			kind_out             AS node_kind,
			proto                AS protocol,
			topK(5)(label)       AS top_labels,
			toUInt32(uniqExact(label)) AS distinct_labels,
			toUInt64(count())    AS calls,
			toUInt64(sum(duration)) AS sum_duration_ns,
			toFloat64(quantileTDigest(0.99)(duration)) / 1e6 AS p99_ms,
			-- v0.5.367 — infra-edge errors mirror the service-pair pass.
			toUInt64(countIf(status_code = 'error')) AS errors,
			any(p_env)           AS parent_env,
			''                   AS child_env,
			toUInt64(?)          AS version`+clusterOuter+`
		FROM (
			SELECT
				service_name AS parent_service,
				coalesce(
					nullIf(peer_service, ''),
					nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
					nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], ''),
					''
				) AS infra_host,
				-- v0.9.1318 (entity-model A3 / Ç10) — DB düğümünün instance'ı
				-- artık dbInstanceExpr'den geliyor, infra_host'tan DEĞİL.
				--
				-- infra_host ÜÇ basamak tarar (peer_service → server.address →
				-- net.peer.name); db_summary_5m'in instance kimliği ALTI
				-- tarar (+ db.host → db.name → service_name → 'unknown').
				-- Yukarıdaki v0.5.408 yorumu "aynı coalesce zinciri" diyordu
				-- ama zincirin yarısını yazıyordu — sözleşme İDDİA edilmiş,
				-- kurulmamıştı.
				--
				-- ÖLÇÜLDÜ (lokal, 24s): clickhouse db_system'i için üç
				-- basamaklı zincir '' verdi (peer_service/server.address/
				-- net.peer.name'in ÜÇÜ de boş, 53.692 span), altı basamaklı
				-- zincir coremetry-monolithic verdi. Düğüm bu yüzden düz
				-- db:clickhouse yazılıyordu; splitDbNodeName '@' göremeyip
				-- null döndüğü için düğümden /database'e link KURULAMIYORDU.
				--
				-- Değişim KESİNLİKLE EKLEMELİ: peer_service/server.address/
				-- net.peer.name'den biri doluysa iki zincir AYNI değeri verir
				-- (ilk üç basamak birebir), yani zaten instance'lı düğümler
				-- aynı adı korur. Yalnız ÖNCEDEN DÜZ olan — yani linki zaten
				-- kırık olan — düğümler ad kazanır.
				--
				-- db_system guard'ı topoQueueClusterSQL'in guard'ıyla aynı
				-- gerekçe: infra pass'i HER span'i geçiyor, dört indexOf
				-- taramasını db olmayan satırlarda koşturmanın anlamı yok.
				if(db_system != '', `+dbInstanceExpr+`, '') AS db_instance,
				-- v0.8.448 — leaf-client tespiti (external fallback için):
				-- server-kind bir child'ı OLAN client span'ın hedefi
				-- enstrümante bir servistir — o kenarı cross-service pass
				-- üretir; external adayı yalnız CEVAPSIZ (leaf) client'lar.
				-- Set penceresi bucket sonundan 5 dk taşar: sınırda başlayan
				-- client'ın child'ı bir sonraki bucket'a düşebilir. Set
				-- boyutu 5 dk'lık server-span parent_id'leri (~1-2M @ 1B/gün,
				-- hash set olarak onlarca MB) — worker pass'i, hot path değil.
				span_id GLOBAL NOT IN (
					SELECT parent_id FROM spans
					WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
					  AND kind = 'server' AND parent_id != ''
				) AS unanswered,
				-- v0.5.410 — derive parent_env from resource attrs.
				-- child_env stays empty for infra targets — db/queue/
				-- external nodes don't inherit the caller's env (they
				-- ARE cross-env infra).
				`+topoEnvChainSQL("")+` AS p_env,
				-- v0.7.31 — messaging.destination (topic) so each Kafka topic is a
				-- DISTINCT queue node (queue:<system>:<topic>) instead of every
				-- topic on a broker collapsing into one queue:<system> hairball.
				-- Operator-reported: a broadcast topic (bsa.kafka.core.cache.refresh)
				-- fanned out to thousands of consumers and tangled the whole graph;
				-- separating topics lets it be muted/collapsed surgically. The
				-- attr_values[indexOf(...)] lookup mirrors what messaging_summary_5m
				-- already pays — and this is the 5-min worker aggregation, off the
				-- hot read path.
				coalesce(
					nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], ''),
					nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], ''),
					''
				) AS msg_dest,
				multiIf(
					-- v0.9.1318 (Ç10) — TEK db dalı. dbInstanceExpr 'unknown'
					-- terminaline düştüğü için db_instance, db_system doluyken
					-- ASLA boş olmaz; eski iki-dallı biçimin "instance yok"
					-- yarısı (düz db:<system>) artık ulaşılamaz bir daldı ve
					-- çıkmaz linkin ta kendisiydi. dbInstanceExpr yorumunun
					-- dediği gibi: 'unknown' sentineli özel dal İHTİYACINI
					-- ortadan kaldırır.
					db_system  != '',
						concat('db:',    db_system, '@', db_instance),
					-- v0.5.411 — messaging branch scoped to non-consumer spans only
					-- (consumer spans get the queue → consumer pass below).
					-- v0.7.31 — topic-aware: prefer the destination so topics
					-- separate; fall back to broker host, then bare system.
					msg_system != '' AND kind != 'consumer' AND msg_dest != '',
						concat('queue:', msg_system, ':', msg_dest),
					msg_system != '' AND kind != 'consumer' AND infra_host != '',
						concat('queue:', msg_system, '@', infra_host),
					msg_system != '' AND kind != 'consumer',
						concat('queue:', msg_system),
					peer_service != '' AND kind = 'client',
						concat('ext:', peer_service),
					-- v0.8.448 — semconv fallback: hedefini yalnız
					-- server.address / net.peer.name ile adlandıran HTTP/RPC
					-- client'ları (standart semconv; peer.service opt-in bir
					-- ipucu, çoğu SDK hiç set etmez). Üç kapı: leaf-only
					-- (unanswered — cevabı olan çağrı internal'dır), http/rpc
					-- şekilli (başıboş TCP client'ı düğüm yapma), ve bilinen
					-- servis adı asla (cevap span'ı bu pass'ten SONRA gelen
					-- sınır yarışına kemer-askı; /external read tarafı da
					-- aynı seti eler).
					kind = 'client' AND infra_host != ''
						AND (http_method != '' OR rpc_system != '')
						AND unanswered
						AND infra_host GLOBAL NOT IN (
							SELECT DISTINCT service_name FROM service_summary_5m
							WHERE time_bucket >= toDateTime(?, 'UTC')
						),
						concat('ext:', infra_host),
					''
				) AS child,
				-- proto/kind_out child'dan türetilir (alias zinciri) —
				-- external dalı iki kez yazıp ıraksama riski almak yerine.
				multiIf(
					db_system  != '', 'db',
					msg_system != '', 'kafka',
					startsWith(child, 'ext:'), 'http',
					''
				) AS proto,
				multiIf(
					db_system  != '', 'db',
					msg_system != '', 'queue',
					startsWith(child, 'ext:'), 'external',
					''
				) AS kind_out,
				-- Label format: include the instance/host (peer_service)
				-- when present so the edge detail panel surfaces "which
				-- postgres instance is hot" without forcing a separate
				-- query. Falls back to system+operation when peer is
				-- empty so labels stay informative on older spans.
				multiIf(
					http_method != '', concat(http_method, ' ',
						if(http_route != '', http_route, name)),
					db_system   != '' AND peer_service != '',
						concat(db_system, '@', peer_service, ' ', name),
					db_system   != '', concat(db_system, ' ', name),
					msg_system  != '' AND peer_service != '',
						concat(msg_system, '@', peer_service, ' ', name),
					msg_system  != '', concat(msg_system, ' ', name),
					name
				) AS label,
				name,
				duration,
				status_code`+clusterInner+`
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		)
		WHERE child != ''
		  AND `+topoNoiseExcludeSQL("name")+`
		  AND `+topoNoiseExcludeSQL("msg_dest")+`
		GROUP BY parent_service, child, proto, kind_out
		SETTINGS max_execution_time = 120,
		         distributed_product_mode = 'global'`,
		// v0.9.186 — arg sırası restructure ile değişti: DIŞ SELECT önce
		// (time_bucket, version), sonra İÇ SELECT (unanswered penceresi,
		// bilinen-servis lookback'i, iç WHERE penceresi).
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Add(5*time.Minute).Unix(),
		bucketStart.Add(-time.Hour).Unix(),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology bucket infra pass: %w", err)
	}

	// v0.5.411 — Async messaging consumer pass: queue → consumer
	// service. The producer → queue half is captured by the
	// infra pass above (kind != 'consumer' branch). This pass
	// finalises the chain so the graph reads
	//     producer-service → queue:<system>@<host> → consumer
	// matching Datadog / Honeycomb / Dynatrace messaging topology
	// rendering. queue is the parent (source) here; consumer's
	// service_name is the destination. Protocol stays 'kafka' so
	// the frontend can render messaging edges dashed (async
	// semantics) regardless of which half of the chain it sees.
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_edges_5m
			(time_bucket, parent_service, child_node, node_kind,
			 protocol, top_labels, distinct_labels, calls,
			 sum_duration_ns, p99_ms, errors,
			 parent_env, child_env, version`+clusterCol+`)
		WITH
			coalesce(
				nullIf(peer_service, ''),
				nullIf(attr_values[indexOf(attr_keys, 'server.address')], ''),
				nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], ''),
				''
			) AS infra_host,
			-- v0.7.31 — MUST mirror the infra (producer) pass's queue-node
			-- naming exactly, or producer → queue and queue → consumer land on
			-- different nodes and the chain breaks. Both producer + consumer
			-- spans carry messaging.destination.name (OTel semconv), so they
			-- resolve to the same queue:<system>:<topic> node.
			coalesce(
				nullIf(attr_values[indexOf(attr_keys, 'messaging.destination.name')], ''),
				nullIf(attr_values[indexOf(attr_keys, 'messaging.destination')], ''),
				''
			) AS msg_dest,
			multiIf(
				msg_system != '' AND msg_dest != '',
					concat('queue:', msg_system, ':', msg_dest),
				msg_system != '' AND infra_host != '',
					concat('queue:', msg_system, '@', infra_host),
				msg_system != '',
					concat('queue:', msg_system),
				''
			) AS queue_source,
			-- Consumer's env (the receiver) — child_env on the
			-- queue→consumer edge so the operator sees which env
			-- consumes from a queue when multiple envs share one.
			`+topoEnvChainSQL("")+` AS c_env`+clusterInner+`
		SELECT
			toDateTime(?, 'UTC')                                AS time_bucket,
			queue_source                                        AS parent_service,
			service_name                                        AS child_node,
			'service'                                           AS node_kind,
			'kafka'                                             AS protocol,
			topK(5)(name)                                       AS top_labels,
			toUInt32(uniqExact(name))                           AS distinct_labels,
			toUInt64(count())                                   AS calls,
			toUInt64(sum(duration))                             AS sum_duration_ns,
			toFloat64(quantileTDigest(0.99)(duration)) / 1e6      AS p99_ms,
			toUInt64(countIf(status_code = 'error'))            AS errors,
			''                                                  AS parent_env,
			any(c_env)                                          AS child_env,
			toUInt64(?)                                         AS version`+clusterOuter+`
		FROM spans
		WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		  AND kind = 'consumer'
		  AND msg_system != ''
		  AND queue_source != ''
		  AND `+topoNoiseExcludeSQL("name")+`
		  AND `+topoNoiseExcludeSQL("msg_dest")+`
		GROUP BY parent_service, child_node
		SETTINGS max_execution_time = 60,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology bucket async messaging pass: %w", err)
	}
	return nil
}

// WriteTopologyOpBucket pre-aggregates per-op edges for a 5-min
// bucket. Same shape as WriteTopologyBucket but at op granularity
// — used by /api/topology (operation deep-dive view).
func (s *Store) WriteTopologyOpBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_op_edges_5m
			(time_bucket, parent_service, parent_op,
			 child_service, child_op, calls, version)
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			p.service_name AS parent_service,
			p.name         AS parent_op,
			c.service_name AS child_service,
			c.name         AS child_op,
			toUInt64(count()) AS calls,
			toUInt64(?)    AS version
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name, name
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= toDateTime(?, 'UTC') AND c.time < toDateTime(?, 'UTC')
		  AND c.parent_id != ''
		  AND `+topoNoiseExcludeSQL("c.name")+`
		GROUP BY parent_service, parent_op, child_service, child_op
		SETTINGS max_execution_time = 180,
		         `+s.topoJoinMemSettings()+`,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology op bucket: %w", err)
	}
	return nil
}

// ReadTopologyOpEdgesAgg reads per-op edges from the aggregated
// table for the requested window. Returns the full edge set; the
// API handler runs the BFS to extract the bounded subgraph.
func (s *Store) ReadTopologyOpEdgesAgg(ctx context.Context, from, to time.Time, limit int) ([]TopologyEdge, error) {
	if limit <= 0 || limit > 200000 {
		limit = 50000
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
			parent_service, parent_op, child_service, child_op,
			sum(calls) AS total_calls
		FROM topology_op_edges_5m FINAL
		WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		GROUP BY parent_service, parent_op, child_service, child_op
		ORDER BY total_calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		from.Unix(), to.Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopologyEdge
	for rows.Next() {
		var e TopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ParentOp,
			&e.ChildService, &e.ChildOp, &e.Calls); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WriteRootFlowsBucket pre-aggregates business flows for one
// 5-min window. Counts traces + collects unique services per
// (root_service, root_op). Mirrors GetRootFlows but materialises
// the result into the agg table for cheap fan-out reads later.
func (s *Store) WriteRootFlowsBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)
	if err := s.conn.Exec(ctx, `
		INSERT INTO topology_root_flows_5m
			(time_bucket, root_service, root_op,
			 trace_count, services, version)
		WITH root_traces AS (
			SELECT trace_id, service_name AS root_service, name AS root_op
			FROM spans
			WHERE parent_id = ''
			  AND time >= toDateTime(?, 'UTC')
			  AND time <  toDateTime(?, 'UTC')
		)
		SELECT
			toDateTime(?, 'UTC') AS time_bucket,
			rt.root_service,
			rt.root_op,
			toUInt64(uniqExact(rt.trace_id)) AS trace_count,
			groupUniqArrayArray(50)(arrayDistinct([sp.service_name])) AS services,
			toUInt64(?) AS version
		FROM root_traces AS rt
		GLOBAL INNER JOIN (
			SELECT trace_id, service_name
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
		) AS sp ON sp.trace_id = rt.trace_id
		GROUP BY rt.root_service, rt.root_op
		SETTINGS max_execution_time = 180,
		         `+s.topoJoinMemSettings()+`,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
	); err != nil {
		return fmt.Errorf("topology root flows bucket: %w", err)
	}
	return nil
}

// ReadRootFlowsAgg reads pre-aggregated business flows for a
// window. trace_count is summed across buckets; services arrays
// are merged + deduplicated. Limit caps the number of flows
// returned to the heaviest by trace volume.
func (s *Store) ReadRootFlowsAgg(ctx context.Context, from, to time.Time, limit int) ([]RootFlow, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
			root_service,
			root_op,
			toUInt64(sum(trace_count)) AS total_traces,
			arrayDistinct(arrayFlatten(groupArray(services))) AS services
		FROM topology_root_flows_5m FINAL
		WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		GROUP BY root_service, root_op
		ORDER BY total_traces DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		from.Unix(), to.Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RootFlow
	for rows.Next() {
		var f RootFlow
		if err := rows.Scan(&f.RootService, &f.RootOp, &f.TraceCount, &f.Services); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CountRootFlows returns the number of DISTINCT business flows (root_service,
// root_op) in the window — the denominator for the "showing N of M flows"
// honesty banner (v0.7.39). Operator-reported: Business Flows is capped at
// ?top and gave no signal that more flows existed beyond the cut. Cheap: one
// uniqExact over the small pre-aggregated MV.
func (s *Store) CountRootFlows(ctx context.Context, from, to time.Time) (int, error) {
	var n uint64
	err := s.telemetryReadConn().QueryRow(ctx, `
		SELECT toUInt64(uniqExact((root_service, root_op)))
		FROM topology_root_flows_5m FINAL
		WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		SETTINGS max_execution_time = 10`,
		from.Unix(), to.Unix(),
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// FlowSig identifies a business flow by its (root_service, root_op)
// pair. Used as a bounded IN-list for the p99 enrichment so the
// query never scans more roots than the caller already listed.
type FlowSig struct {
	RootService string
	RootOp      string
}

// ComputeFlowsLatencyP99 returns the p99 root-span duration (ns)
// for each requested flow signature over the window. Keyed on
// "service\x00op" so the caller can look up without a struct
// equality dance. Empty input → empty map, no query.
//
// The IN list is bounded by the caller's flow limit (cap 200 on
// the API surface), so even at billion-span scale this is a thin
// GROUP BY over (parent_id=”) roots filtered to a handful of
// signatures — far cheaper than ranking flows from raw spans,
// which is why we let the agg path own ranking and use this only
// for latency enrichment.
func (s *Store) ComputeFlowsLatencyP99(ctx context.Context, from, to time.Time, sigs []FlowSig) (map[string]uint64, error) {
	if len(sigs) == 0 {
		return map[string]uint64{}, nil
	}
	// Build the IN-list as a flat (svc, op, svc, op, …) arg slice;
	// CH accepts `IN ((?,?), (?,?), …)` with positional binding.
	placeholders := make([]byte, 0, len(sigs)*8)
	args := make([]any, 0, 2+len(sigs)*2)
	args = append(args, from, to)
	for i, sig := range sigs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '(', '?', ',', '?', ')')
		args = append(args, sig.RootService, sig.RootOp)
	}
	q := `
		SELECT service_name, name,
		       toUInt64(quantile(0.99)(toFloat64(duration))) AS p99_ns
		FROM spans
		WHERE parent_id = ''
		  AND time >= ? AND time < ?
		  AND (service_name, name) IN (` + string(placeholders) + `)
		GROUP BY service_name, name
		SETTINGS max_execution_time = 15`
	rows, err := s.telemetryReadConn().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]uint64, len(sigs))
	for rows.Next() {
		var svc, op string
		var p99 uint64
		if err := rows.Scan(&svc, &op, &p99); err != nil {
			return nil, err
		}
		out[svc+"\x00"+op] = p99
	}
	return out, rows.Err()
}

// ListOpsForService returns the operation names that appear as
// outbound callers for a given service in the window. Drives the
// op-picker dropdown on the operation deep-dive view. Reads
// directly from the agg table so the response is fast.
func (s *Store) ListOpsForService(ctx context.Context, service string, from, to time.Time) ([]string, error) {
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT DISTINCT parent_op
		FROM topology_op_edges_5m FINAL
		WHERE parent_service = ?
		  AND time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
		ORDER BY parent_op
		LIMIT 500
		SETTINGS max_execution_time = 5`,
		service, from.Unix(), to.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op string
		if err := rows.Scan(&op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ReadServiceTopologyAgg reads pre-aggregated topology rows for
// a window from topology_edges_5m. Each row in the agg table is
// one 5-min bucket; we sum calls + merge top_labels arrays across
// buckets to give an aggregate over the requested window.
//
// distinct_labels is approximated as the count of unique labels in
// the merged top_labels array — at 5 labels per bucket that's
// accurate up to a few dozen endpoints per strand, which is plenty
// for human-readable topology.
//
// Window is rounded out to the surrounding 5-min boundaries so a
// partially-covered bucket isn't dropped silently.
func (s *Store) ReadServiceTopologyAgg(ctx context.Context, from, to time.Time, limit int) ([]ServiceTopologyEdge, error) {
	return s.readServiceTopologyAggFiltered(ctx, from, to, limit, nil)
}

// readServiceTopologyAggFiltered is ReadServiceTopologyAgg with an optional
// node-touch filter: when `touching` is non-empty only edges with at least
// one endpoint in the set are read. v0.9.366 — the neighborhood scope's
// building block; the focus walk (ReadServiceTopologyAggForFocus) feeds
// frontiers here instead of pulling the whole estate's top-20k edge set.
func (s *Store) readServiceTopologyAggFiltered(ctx context.Context, from, to time.Time, limit int, touching []string) ([]ServiceTopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	touchWhere := ""
	args := []any{from.Unix(), to.Unix()}
	if len(touching) > 0 {
		ph := chPlaceholders(len(touching))
		touchWhere = "\n\t\t\t  AND (parent_service IN (" + ph + ") OR child_node IN (" + ph + "))"
		args = append(args, toAnySlice(touching)...)
		args = append(args, toAnySlice(touching)...)
	}
	args = append(args, limit)
	// v0.9.1026 — cluster projeksiyonu probe'a bağlı (hasTopoClusterCol).
	// Kolonun inmediği boot'ta koşulsuz bir `argMax(cluster, …)` bu
	// sorguyu code 47 ile düşürürdü — ve bu sorgu /topology'nin TAMAMINI
	// besliyor, yani graf komple kararırdı. Kapalıyken alan boş kalır ve
	// köprü v0.9.972 katalog dalına düşer.
	//
	// argMax(cluster, if(cluster != '', time_bucket, toDateTime(0))):
	// pencere içindeki EN GÜNCEL BOŞ OLMAYAN cluster. Düz
	// argMax(cluster, time_bucket) yanlış olurdu — en yeni kova bir
	// v0.9.1025 öncesi satırdan ya da cluster yazmayan bir pass'ten
	// gelirse '' kazanır ve elimizdeki bilgiyi çöpe atardık. Hepsi boşsa
	// zaten '' dönüyor (doğru cevap: "bilinmiyor").
	clusterInnerSel, clusterOuterSel := "", ""
	if s.hasTopoClusterCol {
		clusterInnerSel = ",\n\t\t\t\targMax(cluster, if(cluster != '', time_bucket, toDateTime(0))) AS cluster"
		clusterOuterSel = ",\n\t\t\tcluster"
	}
	// Subquery: aggregate within groups first, then post-process
	// the merged label array. Inlining the merged array twice in
	// the outer SELECT (once for arraySlice, once for length)
	// makes CH's analyzer reject the query as "aggregate inside
	// aggregate" — even though both wrappers are scalar array
	// functions. Splitting the merge into a named subquery field
	// sidesteps the false-positive.
	// toUInt64 casts on length() + sum() because the CH Go driver
	// is strict on Scan type matching — a UInt32 column won't bind
	// to *uint64 even though the value fits. Struct fields stay
	// uint64 so JSON encoding keeps the same shape across drivers.
	rows, err := s.telemetryReadConn().Query(ctx, `
		SELECT
			parent_service,
			child_node,
			node_kind,
			protocol,
			arraySlice(merged, 1, 5) AS top_labels,
			toUInt64(length(merged)) AS distinct_labels,
			total_calls,
			total_errors,
			avg_ms,
			max_p99_ms,
			parent_env,
			child_env`+clusterOuterSel+`
		FROM (
			SELECT
				parent_service,
				child_node,
				any(node_kind) AS node_kind,
				protocol,
				arrayDistinct(arrayFlatten(groupArray(top_labels))) AS merged,
				toUInt64(sum(calls)) AS total_calls,
				toUInt64(sum(errors)) AS total_errors,
				if(sum(calls) > 0,
				   toFloat64(sum(sum_duration_ns)) / sum(calls) / 1e6,
				   0) AS avg_ms,
				toFloat64(max(p99_ms)) AS max_p99_ms,
				-- v0.5.410 — env per side. any() picks an
				-- arbitrary representative across the merged
				-- buckets; in practice the env is stable per
				-- (service, day) so the pick is consistent.
				any(parent_env) AS parent_env,
				any(child_env)  AS child_env`+clusterInnerSel+`
			FROM topology_edges_5m FINAL
			WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
			  AND time_bucket <  toStartOfFiveMinute(toDateTime(?, 'UTC')) + INTERVAL 5 MINUTE
			  AND `+TopoCallEdgeFilterSQL+touchWhere+`
			GROUP BY parent_service, child_node, protocol
		)
		ORDER BY total_calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceTopologyEdge
	for rows.Next() {
		var e ServiceTopologyEdge
		// Scan hedefleri projeksiyonla BİRLİKTE açılıp kapanır — kolon
		// listesi ile hedef sayısı ayrışırsa driver "expected N columns"
		// ile düşer, yani tek bayrak iki yeri de sürüyor.
		dest := []any{&e.ParentService, &e.ChildNode, &e.NodeKind,
			&e.Protocol, &e.TopLabels, &e.DistinctLabels, &e.Calls,
			&e.Errors, &e.AvgMs, &e.P99Ms,
			&e.ParentEnv, &e.ChildEnv}
		if s.hasTopoClusterCol {
			dest = append(dest, &e.Cluster)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if e.Calls > 0 {
			e.ErrorRate = float64(e.Errors) / float64(e.Calls) * 100
		}
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		// v0.5.409 — annotate known 3rd-party external nodes.
		// External node format from the aggregator is
		// "ext:<peer_name>"; strip the prefix before looking up
		// in the catalogue. NodeKind=="external" gate keeps the
		// classifier from running on service/db/queue rows.
		if e.NodeKind == "external" && strings.HasPrefix(e.ChildNode, "ext:") {
			peer := strings.TrimPrefix(e.ChildNode, "ext:")
			if disp, kind, ok := classifyExternal(peer); ok {
				e.ExtDisplay = disp
				e.ExtKind = kind
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// topologyNextFrontier is the pure hop step of the focus walk: collects the
// not-yet-seen endpoints of `edges` (busiest first — edges arrive ORDER BY
// total_calls DESC), marks them seen, and caps the result. ext:<name>
// endpoints contribute BOTH spellings so the api layer's ext-merge keeps
// finding the real service's own edges on the next hop. Table-tested
// (topology_focus_test.go, v0.9.366).
func topologyNextFrontier(edges []ServiceTopologyEdge, seen map[string]bool, cap int) []string {
	var next []string
	add := func(n string) {
		cands := []string{n}
		if stripped := strings.TrimPrefix(n, "ext:"); stripped != n {
			cands = append(cands, stripped)
		}
		for _, c := range cands {
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			next = append(next, c)
		}
	}
	for _, e := range edges {
		add(e.ParentService)
		add(e.ChildNode)
	}
	if len(next) > cap {
		next = next[:cap]
	}
	return next
}

// ReadServiceTopologyAggForFocus returns the aggregated edges within `hops`
// of `focus` by walking hop-by-hop with IN-filtered MV reads. v0.9.366 —
// the neighborhood scope previously read the whole estate's top-20000
// edges by calls and hop-walked in Go: past 20k estate edges the focused
// service's own QUIET dependencies fell out of the LIMIT window and
// silently vanished from its Topology tab. Truncation now happens inside
// the neighborhood (which is what the render budget means). ≤3 bounded MV
// queries (hops clamps at 3 in the api layer).
func (s *Store) ReadServiceTopologyAggForFocus(ctx context.Context, from, to time.Time, focus string, hops, limit int) ([]ServiceTopologyEdge, error) {
	if hops < 1 {
		hops = 1
	}
	if hops > 3 {
		hops = 3
	}
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	// Frontier cap: 300 busiest per hop — far above the UI's 40-node render
	// budget, low enough to keep the IN list sane on mega-fanout hubs.
	const frontierCap = 300
	seen := map[string]bool{focus: true}
	frontier := []string{focus}
	have := map[string]bool{}
	var out []ServiceTopologyEdge
	for h := 0; h < hops && len(frontier) > 0 && len(out) < limit; h++ {
		edges, err := s.readServiceTopologyAggFiltered(ctx, from, to, limit, frontier)
		if err != nil {
			return nil, err
		}
		fresh := edges[:0]
		for _, e := range edges {
			k := e.ParentService + "\x00" + e.ChildNode + "\x00" + e.Protocol
			if have[k] {
				continue
			}
			have[k] = true
			out = append(out, e)
			fresh = append(fresh, e)
			if len(out) >= limit {
				break
			}
		}
		frontier = topologyNextFrontier(fresh, seen, frontierCap)
	}
	return out, nil
}

// GetServiceTopologyEdges returns service-pair interactions with
// protocol classification + a top label set per strand.
//
//  1. Cross-service pass (parent_service != child_service) joins
//     spans on (trace_id, parent_id). Grouped by (parent, child,
//     protocol) so HTTP-only and gRPC-only edges between the
//     same pair render separately.
//
//  2. Infra pass synthesises destination nodes from db_system /
//     msg_system / peer_service for leaf-ish client spans, so
//     databases / queues / external APIs render as nodes the
//     same way real services do.
//
// Both passes use topK(5)(label) for the per-edge top labels and
// uniqExact(label) for the global distinct count. argMax with a
// constant weight (the original v0.5.100 query) was buggy — it
// returned any label, not the most common one.
func (s *Store) GetServiceTopologyEdges(ctx context.Context, from, to time.Time, limit int) ([]ServiceTopologyEdge, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	rows, err := s.telemetryReadConn().Query(ctx, `
		WITH
			multiIf(
				c.db_system  != '', 'db',
				c.msg_system != '', 'kafka',
				c.rpc_system != '', 'rpc',
				c.http_method != '', 'http',
				'internal'
			) AS proto,
			multiIf(
				c.http_method != '', concat(c.http_method, ' ',
					if(c.http_route != '', c.http_route, c.name)),
				c.rpc_method  != '', c.rpc_method,
				c.db_system   != '', concat(c.db_system, ' ', c.name),
				c.msg_system  != '', concat(c.msg_system, ' ', c.name),
				c.name
			) AS label
		SELECT
			p.service_name AS parent_service,
			c.service_name AS child_service,
			proto          AS protocol,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count()        AS calls
		FROM spans AS c
		GLOBAL INNER JOIN (
			SELECT trace_id, span_id, service_name
			FROM spans
			WHERE time >= ? AND time <= ?
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= ? AND c.time <= ?
		  AND c.parent_id != ''
		  AND p.service_name != c.service_name
		GROUP BY parent_service, child_service, protocol
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`,
		from, to, from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	var out []ServiceTopologyEdge
	for rows.Next() {
		var e ServiceTopologyEdge
		if err := rows.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			rows.Close()
			return nil, err
		}
		e.NodeKind = "service"
		e.TopLabels = dedupTemplatedLabels(e.TopLabels)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	infraRows, err := s.telemetryReadConn().Query(ctx, `
		WITH
			multiIf(
				db_system  != '', concat('db:',    db_system),
				msg_system != '', concat('queue:', msg_system),
				peer_service != '' AND kind = 'client', concat('ext:', peer_service),
				''
			) AS child,
			multiIf(
				db_system  != '', 'db',
				msg_system != '', 'kafka',
				peer_service != '', 'http',
				''
			) AS proto,
			multiIf(
				db_system  != '', 'db',
				msg_system != '', 'queue',
				peer_service != '', 'external',
				''
			) AS kind_out,
			multiIf(
				http_method != '', concat(http_method, ' ',
					if(http_route != '', http_route, name)),
				db_system   != '', name,
				msg_system  != '', name,
				name
			) AS label
		SELECT
			service_name AS parent_service,
			child,
			proto,
			kind_out,
			topK(5)(label) AS top_labels,
			uniqExact(label) AS distinct_labels,
			count() AS calls
		FROM spans
		WHERE time >= ? AND time <= ?
		  AND child != ''
		GROUP BY parent_service, child, proto, kind_out
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 25`,
		from, to, limit,
	)
	if err != nil {
		return nil, err
	}
	defer infraRows.Close()
	for infraRows.Next() {
		var e ServiceTopologyEdge
		if err := infraRows.Scan(&e.ParentService, &e.ChildNode,
			&e.Protocol, &e.NodeKind, &e.TopLabels, &e.DistinctLabels, &e.Calls); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, infraRows.Err()
}
