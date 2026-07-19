// legendStats.ts (v0.9.103, Grafana-parity #1) — grafik lejantı için seri-başı
// istatistik çekirdeği. Saf + test'li (lib/chart/tooltipModel.ts emsali);
// OVC + TC paylaşılan StatsLegend bileşeni tüketir.

export interface SeriesStat {
  last: number | null;   // son DOLU örnek (null → boş)
  min: number | null;
  max: number | null;
  mean: number | null;
  sum: number;           // dolu örneklerin toplamı (count=0 → 0)
  count: number;         // dolu örnek sayısı
}

// seriesStats — null/NaN atlayarak last/min/max/mean/sum/count. Hepsi boşsa
// last/min/max/mean null, sum 0.
export function seriesStats(values: ReadonlyArray<number | null | undefined>): SeriesStat {
  let last: number | null = null;
  let min = Infinity;
  let max = -Infinity;
  let sum = 0;
  let count = 0;
  for (const v of values) {
    if (v == null || !isFinite(v)) continue;
    last = v;            // son dolu örnek
    if (v < min) min = v;
    if (v > max) max = v;
    sum += v;
    count++;
  }
  if (count === 0) return { last: null, min: null, max: null, mean: null, sum: 0, count: 0 };
  return { last, min, max, mean: sum / count, sum, count };
}

// isAdditiveUnit — Sum/Σ (ve "Toplam" satırı) bu birimde ANLAMLI mı?
// Toplanabilir: boş (sayaç/adet), oran/hız (rps, req/s, /s, ops), bytes
// (B/KB/MB/GB). Toplanamaz: yüzde (%), gecikme/süre (ms/s/µs/ns/min/h) —
// pod'lar arası p95 latency'yi TOPLAMAK anlamsız. Belirsizde KAPALI (Sum
// gizlensin — yanlış toplam göstermekten iyidir).
export function isAdditiveUnit(unit: string | undefined): boolean {
  const u = (unit || '').trim().toLowerCase();
  if (u === '') return true;                                   // birimsiz sayaç/adet
  if (u.includes('%')) return false;                          // yüzde
  // hız / oran / adet / bytes → toplanabilir (süreden ÖNCE bakılır ki
  // "req/s" gecikme sanılıp elenmesin).
  if (/\/s\b|\brps\b|\breq|\bops\b|count|error|request|byte|\b[kmgt]?b\b/.test(u)) return true;
  // süre / gecikme → toplanamaz
  if (/\b(ms|s|sec|secs|ns|µs|us|min|h|hr)\b/.test(u)) return false;
  return false;
}
