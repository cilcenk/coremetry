package chstore

import (
	"context"
	"time"
)

// ServiceEdgePair is one directed service-to-service edge. The two
// endpoints (Caller → Callee) are always populated; the weight fields
// are filled only by GetServiceAdjacencyWeighted.
//
// v0.8.67 (correlator Faz 5) — added the weight fields so the
// correlator can build a DIRECTED, WEIGHTED adjacency graph (rank a
// service's downstream deps by error-carrying volume) instead of the
// symmetric unweighted set it used through Faz 4. The plain
// GetServiceAdjacency still returns endpoint-only pairs (weights
// zero) — its only caller, the fusion evidence bundle, needs just the
// "who calls who" topology, so its lean query is left untouched.
type ServiceEdgePair struct {
	Caller string
	Callee string

	// CallerKind / CalleeKind — v0.9.1327 (entity-model A4). Her ucun
	// NE OLDUĞU: "service" | "db" | "queue" | "external", ham düğüm
	// ID önekinden türetilir (TopologyEndpointKind, identity.go).
	//
	// Neden alan: bu tip artık yalnız servis çiftleri taşımıyor. Ad
	// tek başına ayırt edici DEĞİL — bir tüketici, `db:oracle@host`
	// dizesini bir servis adından ayırmak için önek kuralını YENİDEN
	// yazmak zorunda kalırdı ve o kural repoda zaten üç yerde
	// aynalanmıştı (v0.9.1029). Kind'ı burada bir kez türetip taşımak
	// dördüncü aynayı engelliyor.
	CallerKind string
	CalleeKind string

	// Weighted-edge fields — populated by GetServiceAdjacencyWeighted,
	// left zero by GetServiceAdjacency.
	Calls         uint64 // total calls on this edge in the window
	Errors        uint64 // error-status calls on this edge in the window
	SumDurationNs uint64 // summed span duration — window avg = SumDurationNs/Calls
}

// typeEdge stamps both endpoint kinds from the raw node IDs. Pure and
// separate from the query so the typing contract is table-testable
// without a live ClickHouse — the queue-parent case in particular
// (v0.9.1327: `node_kind` describes the CHILD, so a queue parent rode
// in under `node_kind='service'` and the correlator treated a Kafka
// topic as a service).
func typeEdge(e *ServiceEdgePair) {
	e.CallerKind = TopologyEndpointKind(e.Caller)
	e.CalleeKind = TopologyEndpointKind(e.Callee)
}

// GetServiceAdjacency returns the distinct service→service edges
// observed in the last `since` window, read from the pre-
// aggregated topology_edges_5m MV.
//
// v0.5.304 — operator-reported boot timeout: the previous
// correlator path called GetServiceMap which runs
//
//	SELECT trace_id FROM spans WHERE time >= ? GROUP BY trace_id
//	ORDER BY count() DESC LIMIT 200
//
// over a 1h window. At billion-span scale that GROUP BY hits the
// 30s max_execution_time ceiling and the boot-time adjacency
// refresh fails (initial map stays empty until the next 5-min
// tick — also fails). This helper bypasses the trace walk
// entirely: the edges are already pre-aggregated per 5-min bucket.
//
// v0.9.1327 (entity-model A4) — `node_kind = 'service'` KALKTI ve
// yerine her ucun kind'ı ID önekinden damgalanıyor. İki ayrı kusuru
// birden kapatıyor, çünkü ikisi de aynı yanlış okumadan doğuyordu:
//
//	(a) Filtre db/queue/external ÇOCUKLARI eliyordu, yani korelatörün
//	    grafiği paylaşılan bir veritabanını hiç görmüyordu — ortak
//	    neden ADLANDIRILAMIYORDU (denetim §7.2 A4).
//	(b) Filtre EBEVEYNİ hiç kısıtlamıyordu. `node_kind` üç yazıcı
//	    pass'inde de ÇOCUĞUN kind'ı; queue→consumer pass'inde çocuk
//	    gerçekten bir servis olduğu için satır 'service' damgalı ama
//	    EBEVEYNİ `queue:<sys>:<topic>`. Yani filtre, yanlış tiplenen
//	    tek düğümü içeri alıp doğru tiplenenleri eliyordu ve
//	    korelatör bir Kafka topic'ini servis sanıyordu.
//
// (b) bir YAZICI kusuru DEĞİL — writer doğru yazıyor, okuma yanlış
// soruyordu. Bu yüzden bu dilimde MV'ye dokunulmadı ve karışık-kimlik
// geçiş penceresi (v0.9.1326'nın konusu) burada YOK.
//
// Kind'ı bilen çağıran, servis adı bekleyen yollarını (Problem.Service
// eşlemesi, /service linki) kendi kapılayabilir; bilmeyen için dizenin
// kendisi zaten öneki taşıyor.
//
// time_bucket is aligned to the bucket boundary (5-min) per
// v0.5.299's predicate-overlap fix so the most-recent partial
// bucket isn't silently excluded.
//
// LIMIT 20000 + deterministic ORDER BY (v0.9.1327): the widening adds
// every infra edge to the same cap, so an arbitrary truncation would
// now drop MORE and drop it differently on every refresh. Ordering by
// the group keys makes the cut reproducible; the weighted sibling
// below orders by error volume because it HAS weights to rank by.
func (s *Store) GetServiceAdjacency(
	ctx context.Context, since time.Duration,
) ([]ServiceEdgePair, error) {
	if since <= 0 {
		since = time.Hour
	}
	bucketStart := time.Now().Add(-since).Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, `
		SELECT parent_service, child_node
		FROM topology_edges_5m FINAL
		WHERE time_bucket >= ?
		GROUP BY parent_service, child_node
		ORDER BY parent_service, child_node
		LIMIT 20000
		SETTINGS max_execution_time = 10`, bucketStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdgePair
	for rows.Next() {
		var e ServiceEdgePair
		if err := rows.Scan(&e.Caller, &e.Callee); err != nil {
			return nil, err
		}
		if e.Caller == "" || e.Callee == "" {
			continue
		}
		typeEdge(&e)
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetServiceAdjacencyWeighted is GetServiceAdjacency plus per-edge
// weights — total calls, error calls and summed duration over the
// window — summed across the 5-min buckets (and protocols) of each
// (parent_service, child_node) pair. The correlator uses these to
// build a directed weighted graph (v0.8.67, Faz 5): Caller's
// downstream deps ranked by error-carrying volume, Callee's upstream
// callers likewise.
//
// Same MV, same bounds and partition pruning as GetServiceAdjacency
// (MV-bypass invariant satisfied — this never touches raw spans). The
// only addition is the three sum() aggregates, which FINAL collapses
// per ORDER-BY key before summing across buckets, so duplicate
// ReplacingMergeTree versions of a bucket are not double-counted —
// the exact pattern GetServiceGraph (repo.go) and ReadServiceTopologyAgg
// (topology.go) already use.
//
// ORDER BY errors DESC, calls DESC before the LIMIT so that, at a
// 1000s-services mesh where the distinct directed-edge count can exceed
// the cap, truncation is DETERMINISTIC and keeps the highest-error /
// highest-volume edges — the ones the correlator's Downstream/Upstream
// ranking (errors-first) actually consumes. Without it, LIMIT returns an
// arbitrary subset and could silently drop the single edge carrying the
// incident's error traffic. Cap is 20000 (matching ReadServiceTopologyAgg)
// — ~20k EdgeStat structs is a few MB, trivially bounded memory.
//
// v0.9.1327 — `node_kind = 'service'` burada da kalktı; gerekçe ve iki
// kusurun anlatısı GetServiceAdjacency'nin şerhinde. Bu uçta ORDER BY
// zaten deterministik olduğu için genişleme yalnız kapağın altındaki
// SIRAYI değiştiriyor: artık hata hacmi yüksek bir db/queue kenarı,
// hata taşımayan bir servis kenarının önüne geçebilir — istenen davranış
// (kesme, korelatörün gerçekten tükettiği sinyali korur).
func (s *Store) GetServiceAdjacencyWeighted(
	ctx context.Context, since time.Duration,
) ([]ServiceEdgePair, error) {
	if since <= 0 {
		since = time.Hour
	}
	bucketStart := time.Now().Add(-since).Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, `
		SELECT parent_service,
		       child_node,
		       sum(calls)           AS calls,
		       sum(errors)          AS errors,
		       sum(sum_duration_ns) AS sum_dur
		FROM topology_edges_5m FINAL
		WHERE time_bucket >= ?
		GROUP BY parent_service, child_node
		ORDER BY errors DESC, calls DESC
		LIMIT 20000
		SETTINGS max_execution_time = 10`, bucketStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdgePair
	for rows.Next() {
		var e ServiceEdgePair
		if err := rows.Scan(&e.Caller, &e.Callee, &e.Calls, &e.Errors, &e.SumDurationNs); err != nil {
			return nil, err
		}
		if e.Caller == "" || e.Callee == "" {
			continue
		}
		typeEdge(&e)
		out = append(out, e)
	}
	return out, rows.Err()
}
