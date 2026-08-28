package chstore

import "fmt"

// entity_schema.go — K8s ENTITY KATMANI, span-türevli "görüldü" indeksi
// (v0.10.127, AŞAMA 3 adım 1; docs/plans/entity-layer-design-2026-08-28.md §2.4).
//
// ── NEDEN ŞİMDİ MV, pod_seen NEDEN ERTELENMİŞTİ ──────────────────────────
//
// pod_inventory.go (v0.10.40) bir pod_seen MV'sini bilinçli ertelemişti:
// namespace/pod terfi kolonu değildi, MV her span INSERT'inde
// `res_values[indexOf(res_keys, …)]` koşacaktı. Bu MV o koşulu KALDIRARAK
// geliyor: k8s_namespace / k8s_pod / k8s_node artık MATERIALIZED terfi
// kolonları (promoted_attr.go, res kapsamı) — indexOf INSERT'te bir kez,
// kolon olarak, MV düz kolon okur (yerel CH 26.2'de ölçüldü: combined MV
// kaynak tablonun MATERIALIZED kolonunu görür). `WHERE k8s_pod != ''`
// kapısı k8s bağlamsız span'leri MV'ye hiç sokmaz.
//
// ── ŞEKİL ────────────────────────────────────────────────────────────────
//
// Filtre öznesi service_name ÖNDE: "bu servisi hangi pod'lar taşıyor"
// sorusu ORDER BY önekinden okunur. Ters yön (pod → servisler) MV'yi
// önek dışı okurdu; o soru entity_relations.runs ile cevaplanır, MV yalnız
// zaman-dilimli sayım için. tDigest YOK (satır ~1.5 KB; 2.5k pod × 288
// kova/gün) — yüzdelik ham yoldan, pod filtresi set(0) index ile sınırlı.
// 1m kademesi 3 gün (yakın geçmiş hassasiyeti), 5m kademesi 30 gün.
//
// Gün-bir üç kayıt (cluster.go): highVolumeTables + defaultShardPolicy
// (cityHash64(service_name)) + tablesWithoutTraceID.

const (
	entitySeen1mTTLDays = 3
	entitySeen5mTTLDays = 30
)

// entitySeenMVDDL — iki kademenin tek şablonu. Saf; tablo-testli.
func entitySeenMVDDL(name, interval string, ttlDays int) string {
	return fmt.Sprintf(`CREATE MATERIALIZED VIEW IF NOT EXISTS %s
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(time_bucket)
		 ORDER BY (service_name, cluster, k8s_namespace, k8s_pod, time_bucket)
		 TTL toDate(time_bucket) + INTERVAL %d DAY
		 SETTINGS index_granularity = 8192
		 AS SELECT
		   toStartOfInterval(time, INTERVAL %s)    AS time_bucket,
		   service_name,
		   cluster,
		   k8s_namespace,
		   k8s_pod,
		   anyLastSimpleState(k8s_node)             AS k8s_node,
		   countState()                             AS span_count_state,
		   countIfState(status_code = 'error')      AS error_count_state,
		   sumState(duration)                       AS duration_sum_state,
		   minState(time)                           AS first_seen_state,
		   maxState(time)                           AS last_seen_state
		 FROM spans
		 WHERE k8s_pod != ''
		 GROUP BY time_bucket, service_name, cluster, k8s_namespace, k8s_pod`,
		name, ttlDays, interval)
}
