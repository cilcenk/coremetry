// havingParam — v0.8.453 (B2-c): Aggregated görünümünün genel HAVING
// koşulları için URL/API codec'i. Sunucudaki compileHaving whitelist'ini
// AYNALAR — UI hiçbir zaman bilinmeyen metrik/operatör gönderemez, bozuk
// URL parametresi sessizce elenmiş geçerli alt-kümeye iner (paylaşılan
// link asla sayfayı kırmaz). Pure — havingParam.test.ts'te tablo-testli.

export const HAVING_METRICS = [
  { value: 'count',     label: 'Calls' },
  { value: 'perMin',    label: 'Req/min' },
  { value: 'errorRate', label: 'Error %' },
  { value: 'avg',       label: 'Avg ms' },
  { value: 'p50',       label: 'P50 ms' },
  { value: 'p95',       label: 'P95 ms' },
  { value: 'p99',       label: 'P99 ms' },
  { value: 'max',       label: 'Max ms' },
] as const;

export const HAVING_OPS = ['>', '>=', '<', '<='] as const;

export type HavingMetric = typeof HAVING_METRICS[number]['value'];
export type HavingOp = typeof HAVING_OPS[number];

export interface HavingRow {
  metric: HavingMetric;
  op: HavingOp;
  value: number;
}

const METRIC_SET = new Set<string>(HAVING_METRICS.map(m => m.value));
const OP_SET = new Set<string>(HAVING_OPS);

// parseHavingParam — URL'deki ?having= JSON'unu doğrulayarak çözer.
// Geçersiz JSON → []; geçersiz satırlar (bilinmeyen metrik/op, sayısal
// olmayan değer) tek tek elenir.
export function parseHavingParam(raw: string | null): HavingRow[] {
  if (!raw) return [];
  let v: unknown;
  try { v = JSON.parse(raw); } catch { return []; }
  if (!Array.isArray(v)) return [];
  const out: HavingRow[] = [];
  for (const item of v) {
    if (typeof item !== 'object' || item === null) continue;
    const r = item as Record<string, unknown>;
    const metric = r.metric, op = r.op, value = r.value;
    if (typeof metric !== 'string' || !METRIC_SET.has(metric)) continue;
    if (typeof op !== 'string' || !OP_SET.has(op)) continue;
    if (typeof value !== 'number' || !Number.isFinite(value)) continue;
    out.push({ metric: metric as HavingMetric, op: op as HavingOp, value });
  }
  return out.slice(0, 8); // sunucudaki maxHavingExprs ile aynı tavan
}

// encodeHavingParam — hem URL'e hem API'ye giden tek biçim.
// Boş liste '' döner (param URL'den düşer).
export function encodeHavingParam(rows: HavingRow[]): string {
  if (rows.length === 0) return '';
  return JSON.stringify(rows);
}
