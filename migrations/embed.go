// Package migrations — rollup DDL dosyalarını BİNARY'ye gömer.
//
// v0.9.770. Neden gömüyoruz: rollup kurulum sihirbazı (/admin/clickhouse
// → "Rollup katmanı") DDL'i pod içinden koşuyor; dosya sisteminden okumak
// tek-binary kısıtını bozardı (imajda migrations/ dizini yok, yalnız
// /app/coremetry var).
//
// v0.10.134: 0011 (entity katmanı şeması) da gömülü — Admin → ClickHouse
// "K8s entity katmanı" adımı (operatör: "0011 sihirbazda yok").
// v0.10.299: 0014 (attribute hash indeksi attr_kvh/res_kvh + bloom) — prod elle
// (docs/audit/trace-attribute-search.md); testi attr_index_migration_test.go.
// v0.10.197: 0012 (rollouts katmanı: 6 terfi kolonu + workload_rollouts +
// workload_revision_activity_1m MV) — Admin → ClickHouse "Rollouts katmanı".
// Yalnız 0001 + 0003 + 0008 (+0011) gömülü — sihirbazın kapsamı:
//
//	0001 → dar span rollup zinciri (10s→1m→5m→1h)
//	0003 → metrik rollup zinciri (1m→5m→1h)
//	0008 → route (endpoint) kırılımlı metrik zinciri (1m→5m→1h)
//
// 0002 (geniş aile) ve 0004-0007 (backfill / attr promotion / onarım)
// operatör kararı gerektiren, geri alınması pahalı işler — bilinçli
// olarak sihirbazın DIŞINDA, elle koşulmaya devam ediyor.
package migrations

import "embed"

// FS — gömülü DDL dosyaları. chstore.RollupApply okur.
//
//go:embed 0001_rollup_narrow.sql 0003_rollup_metrics.sql 0008_rollup_metrics_route.sql 0011_entity_layer.sql 0012_rollout_layer.sql 0013_function_id.sql 0013_function_id_rollback.sql 0014_attr_kvh.sql 0014_attr_kvh_rollback.sql
var FS embed.FS
