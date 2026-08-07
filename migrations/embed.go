// Package migrations — rollup DDL dosyalarını BİNARY'ye gömer.
//
// v0.9.770. Neden gömüyoruz: rollup kurulum sihirbazı (/admin/clickhouse
// → "Rollup katmanı") DDL'i pod içinden koşuyor; dosya sisteminden okumak
// tek-binary kısıtını bozardı (imajda migrations/ dizini yok, yalnız
// /app/coremetry var).
//
// Yalnız 0001 + 0003 gömülü — sihirbazın kapsamı BU İKİ AİLE:
//   0001 → dar span rollup zinciri (10s→1m→5m→1h)
//   0003 → metrik rollup zinciri (1m→5m→1h)
// 0002 (geniş aile) ve 0004-0007 (backfill / attr promotion / onarım)
// operatör kararı gerektiren, geri alınması pahalı işler — bilinçli
// olarak sihirbazın DIŞINDA, elle koşulmaya devam ediyor.
package migrations

import "embed"

// FS — gömülü DDL dosyaları. chstore.RollupApply okur.
//
//go:embed 0001_rollup_narrow.sql 0003_rollup_metrics.sql
var FS embed.FS
