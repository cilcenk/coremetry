package chstore

import "fmt"

// rollout_schema.go — ROLLOUTS veri katmanı (v0.10.193, Faz 1a;
// docs/audits/rollouts-audit.md §1, §5).
//
// ── ÜÇ NESNE ──────────────────────────────────────────────────────────────
//
//   workload_rollouts         state: (cluster_id, namespace, workload,
//                             revision, started_at) başına TEK olay satırı.
//                             RMT(version) + FINAL (ev kuralı, 38/38);
//                             PARTITION YOK (Kural P1; retention partition
//                             düşürmüyor → kazanç sıfır); shard EDİLMEZ
//                             (düşük hacimli state → stateTableDDL birleşik
//                             grup — §5(a)); TTL 180 g (anomaly_verdicts).
//   rollout_reconcile_runs    reconciler koşu kaydı (entity_sync_runs şekli;
//                             ok|partial|failed|skipped — bu kez dördü de yazılır).
//   workload_revision_activity_1m  Faz 1b MV: spans → dakikalık (cluster,
//                             namespace, workload, revision, service) etkinlik;
//                             entity_seen_1m şablonu; YALNIZ terfi kolonu okur
//                             (INSERT başına indexOf YOK — pod_inventory'nin
//                             ertelediği maliyet sınıfı). Kapı: store.go,
//                             k8s_replicaset + k8s_deployment kolonu varsa.
//
// started_at DONUK ve DETERMİNİSTİK: revizyonun aktif kümeye girdiği ilk
// 5 dk kovanın BAŞI (time.Now() değil) — lider devri / lockDegraded
// penceresinde iki yazıcı aynı satırı üretsin, RMT birleştirsin (§2.5).
// ⚠ İki yazıcı sözleşmesi (inceleme #4/#5): (1) RMT(version) TAM SATIR
// değiştirir — bir yazıcı (span ya da KSM) satırı güncellerken ÖTEKİNİN
// alanlarını (ksm_started_at, pods_ready_at, traffic_confirmed_at …)
// mevcut satırdan TAŞIMAK zorundadır (read-modify-write; reconcile.go
// `row = *existing`), aksi hâlde 1970'e sıfırlanır. (2) started_at ORDER
// BY'da: açık bir olay MV penceresinden (7 g) daha uzun sürerse kova
// yeniden türetilemez — mevcut satırın started_at'i TAŞINIR, asla ileri
// alınmaz (reconcile.go pencere-başı devam kuralı); yoksa aynı revizyon
// için ikinci, birleşmez bir satır doğar.
// image/prev_image sentinel '' (Nullable ev kuralı C4'e aykırı; §13.14).
// cluster_id = Remote Cluster EffectiveID (rename-güvenli, §13.3); ad okuma
// anında çözülür.
//
// Prod (dış Distributed): aynı nesneler migrations/0012 ile OPERATÖRDE;
// tanımlar buradakiyle birebir tutulur (0011 sözleşmesi).

const (
	workloadRolloutsTTLDays      = 180
	rolloutReconcileRunsTTLDays  = 30
	workloadRevisionActivityDays = 7
)

// workloadRolloutsDDL — tables dilimi (CREATE tables'a, ALTER alters'a).
var workloadRolloutsDDL = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS workload_rollouts (
	cluster_id           LowCardinality(String),
	namespace            LowCardinality(String),
	workload             String,
	workload_kind        LowCardinality(String),      -- Deployment | StatefulSet | DaemonSet
	revision             String,                      -- ReplicaSet adı (pod-template-hash)
	started_at           DateTime64(3),               -- DONUK: aktif kümeye giren ilk 5 dk kovanın başı
	status               LowCardinality(String),      -- in_progress | completed | rolled_back | superseded | stalled (rollout.Status*)
	prev_revision        String        DEFAULT '',
	image                String        DEFAULT '',    -- container.image.name
	image_tag            String        DEFAULT '',    -- container.image.tag
	prev_image           String        DEFAULT '',
	prev_image_tag       String        DEFAULT '',
	first_span_at        DateTime64(3) DEFAULT 0,
	traffic_confirmed_at DateTime64(3) DEFAULT 0,     -- span: eski revizyon(lar) aktif kümeden çıktı
	ksm_started_at       DateTime64(3) DEFAULT 0,     -- KSM: kube_replicaset_created
	pods_ready_at        DateTime64(3) DEFAULT 0,     -- KSM: ready == desired
	ksm_not_ready_since  DateTime64(3) DEFAULT 0,     -- KSM: stalled sayacı (dayanıklı)
	completed_at         DateTime64(3) DEFAULT 0,
	detected_by          LowCardinality(String),      -- spans | ksm | spans+ksm
	span_count           UInt64        DEFAULT 0,
	note                 String        DEFAULT '',
	updated_at           DateTime64(3) DEFAULT now64(3),   -- SSE tail kursörü
	version              UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
ORDER BY (cluster_id, namespace, workload, revision, started_at)
TTL toDate(started_at) + INTERVAL %d DAY`, workloadRolloutsTTLDays)

// rolloutReconcileRunsDDL — koşu kaydı. ORDER BY (started_at, host): lider
// devri / lockDegraded penceresinde iki pod aynı milisaniyede yazarsa
// satırlar birbirini EZMESİN (inceleme #6; entity_sync_runs'ın cluster_id
// ayırıcısının karşılığı).
var rolloutReconcileRunsDDL = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS rollout_reconcile_runs (
	started_at       DateTime64(3),
	host             LowCardinality(String) DEFAULT '',  -- yazan pod (ayırıcı)
	finished_at      DateTime64(3),
	status           LowCardinality(String),          -- ok | partial | failed | skipped
	clusters         UInt16  DEFAULT 0,
	rollouts_written UInt32  DEFAULT 0,
	span_ms          UInt32  DEFAULT 0,
	ksm_ms           UInt32  DEFAULT 0,
	error            String  DEFAULT '',
	version          UInt64  DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
ORDER BY (started_at, host)
TTL toDate(started_at) + INTERVAL %d DAY`, rolloutReconcileRunsTTLDays)

// workloadRevisionActivityMVDDL — Faz 1b (combined MV; adaptDDL küme
// kipinde _local + Distributed sarmalayıcı üretir; migrations/0012 ADIM 6
// aynı nesneleri FROM spans_local ile yazar). Saf; tablo-testli.
//
// ORDER BY filtre öznesi: (cluster, namespace, workload) — "bu workload'ın
// bu pencerede aktif revizyonları" ORDER BY önekinden okunur; service_name
// gün-bir (MV tek atış; verdict → servis eşlemesi buradan); bucket en sonda.
// image/tag/kind anyLast: aynı kovada aynı revizyon tek imaj/tür taşır —
// ⚠ v0.10.211 SELECT değişikliği MEVCUT MV'yi kendiliğinden güncellemez:
// küme kipinde admin rollback+apply (0012 sihirbazı), tek node'da DROP VIEW
// sonrası boot yeniden kurar. Kapsama kapısı bilinçli replicaset-bazlı
// kaldı: STS/DS-ağırlıklı bir cluster kapıyı açamayabilir (bilinen sınır).
// kind GROUP BY'da DEĞİL (inceleme #7: sıralama anahtarı dışındaki düz kolon
// merge'de rastgele değere çökerdi).
func workloadRevisionActivityMVDDL() string {
	return fmt.Sprintf(`CREATE MATERIALIZED VIEW IF NOT EXISTS workload_revision_activity_1m
		 ENGINE = AggregatingMergeTree
		 PARTITION BY toDate(bucket)
		 ORDER BY (cluster, k8s_namespace, workload, revision, service_name, bucket)
		 TTL toDate(bucket) + INTERVAL %d DAY
		 SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1
		 AS SELECT
		   toStartOfInterval(time, INTERVAL 1 MINUTE)                      AS bucket,
		   cluster,
		   k8s_namespace,
		   multiIf(k8s_deployment != '', k8s_deployment,
		           k8s_statefulset != '', k8s_statefulset,
		           k8s_daemonset != '', k8s_daemonset, '')                  AS workload,
		   anyLastSimpleState(multiIf(k8s_deployment != '', 'Deployment',
		           k8s_statefulset != '', 'StatefulSet',
		           k8s_daemonset != '', 'DaemonSet', ''))                   AS workload_kind,
		   -- v0.10.211: STS/DS pod'unda ReplicaSet yok; revizyon vekili imaj
		   -- tag'i (operatör kararı: deploy = yeni imaj). Sabit tag'li (latest)
		   -- STS rollout'u görünmez; aynı tag'e rollback tek revizyon sayılır.
		   if(k8s_replicaset != '', k8s_replicaset, container_image_tag)   AS revision,
		   service_name,
		   anyLastSimpleState(container_image)                             AS image,
		   anyLastSimpleState(container_image_tag)                         AS image_tag,
		   countState()                                                    AS span_count_state,
		   minState(time)                                                  AS first_seen_state,
		   maxState(time)                                                  AS last_seen_state
		 FROM spans
		 WHERE workload != '' AND revision != ''
		 GROUP BY bucket, cluster, k8s_namespace, workload, revision, service_name`,
		workloadRevisionActivityDays)
}
