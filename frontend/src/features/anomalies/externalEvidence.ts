// externalEvidence.ts — v0.10.230 (Influx D5): dış kaynak kanıt panelinin
// SAF çekirdeği. Panel JSX'i ince; şekillendirme burada, vitest'te pinli.

import type { DeepEvidence, SpanMetricSeries, TraceSpanSummary } from '@/lib/types';

export interface ExternalTraceRow extends TraceSpanSummary {
  /** Kanıt listesinde var ama CH'de span'i bulunmadı (retention / henüz
   *  gelmedi) — satır yine çizilir, link yine çalışır. */
  missing?: boolean;
}

/** traceRows — hipotezin trace listesi (en yeni önce) ⋈ span özeti.
 *  Özeti olmayan id `missing` ile kalır: "50 id var, 12'si CH'de" dürüstlüğü. */
export function traceRows(deep: DeepEvidence | undefined): ExternalTraceRow[] {
  if (!deep) return [];
  const byId = new Map<string, TraceSpanSummary>();
  for (const s of deep.external?.spanSummary ?? []) byId.set(s.traceId, s);
  const ids = deep.traceIds ?? [];
  const out: ExternalTraceRow[] = ids.map(id => {
    const s = byId.get(id);
    return s ? { ...s } : { traceId: id, startNs: 0, durationNs: 0, spans: 0, errorSpans: 0, missing: true };
  });
  // Özeti olup listede olmayan (birleşim sırasında kırpılmış) id'ler de gelsin.
  for (const s of byId.values()) if (!ids.includes(s.traceId)) out.push({ ...s });
  return out;
}

/** evidenceCounts — başlık altı sayım satırı için. */
export function evidenceCounts(deep: DeepEvidence | undefined) {
  const traces = deep?.traceIds?.length ?? 0;
  const withSpans = deep?.external?.spanSummary?.length ?? 0;
  return {
    traces,
    withSpans,
    pods: deep?.affectedPods?.length ?? 0,
    signatures: deep?.logSignatures?.length ?? 0,
    rows: deep?.external?.rows ?? 0,
    invalid: deep?.external?.invalidIds ?? 0,
  };
}

/** pickSeries — groupBy'lı sorgu sonucundan bu problemin serisini seçer:
 *  groupKey, label değerleriyle SIRALI birebir eşleşmeli. Yoksa null
 *  (yanlış seriyi çizmek boş çizmekten kötüdür). */
export function pickSeries(series: SpanMetricSeries[] | null | undefined, values: string[]): SpanMetricSeries | null {
  if (!series) return null;
  for (const s of series) {
    // groupBy'sız sorguda Go nil dilim `null` gelir — boş anahtar sayılır.
    const gk = s.groupKey ?? [];
    if (gk.length === values.length && gk.every((k, i) => k === values[i])) return s;
  }
  return null;
}

/** labelValues — etiket haritasını groupBy anahtar sırasına dizer. Anahtar
 *  sırası bilinmiyorsa (labels'ın kendi sırası) Object.keys sırası — Go
 *  map JSON'u alfabetik yazar, panel de groupBy'ı alfabetik SORAR. */
export function labelKeys(labels: Record<string, string> | undefined): string[] {
  return Object.keys(labels ?? {}).sort();
}

export function labelValues(labels: Record<string, string> | undefined): string[] {
  return labelKeys(labels).map(k => labels![k]);
}

/** toChart — seri noktaları (ns) → TimeChart eksenleri (saniye). Boş → null. */
export function toChart(s: SpanMetricSeries | null): { times: number[]; data: number[] } | null {
  if (!s || s.points.length === 0) return null;
  const pts = [...s.points].sort((a, b) => a.time - b.time);
  return { times: pts.map(p => Math.floor(p.time / 1e9)), data: pts.map(p => p.value) };
}

/** sevTone — OTel severity metni → rozet sınıfı. */
export function sevTone(sev: string): string {
  const s = sev.toUpperCase();
  if (s.startsWith('FATAL') || s.startsWith('ERROR')) return 'b-err';
  if (s.startsWith('WARN')) return 'b-warn';
  return 'b-gray';
}
