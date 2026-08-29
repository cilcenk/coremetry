// anomalyRegions.ts — v0.10.162: anomali olayı → grafik bandı eşleyicisi
// (tasarım etüdü «anomali işaretleri» seçenek A + C tablosu, dilim 1 — sıfır
// backend). Kanal MEVCUT: CorePanel `regions` (ChartTimeRegion, overlays.ts;
// deploy ▼ ve Problem bantları aynı kanaldan). Eksik olan yalnız eşleyiciydi
// (brief §6). Sözleşme anomalyRegions.test.ts'te pinli:
//   - bant [startedAt, max(lastSeen, startedAt + pencerenin %0.4'ü)] — anlık
//     anomali sıfır genişlikte elenmesin (clampRegion; deploy sliver kuralı),
//   - renk TÜRE göre token; log türleri ürünün mevcut anomali rengi
//     (AnnotationLane KIND_COLOR.anomaly = --warn), trace_op --orange,
//     behavior_change --teal — --purple DEĞİL (deploy ▼ ile çakışır, yargıç
//     must-fix) ve --err DEĞİL (Problem bantlarının rengi),
//   - sessize alınmış (silences.fingerprint == id) → --text3 + «▮ sessiz»,
//   - etiket «▮ tür ×tepe» (peakRatio VAR; güven puanı YOK — çizilmez).
import type { AnomalyEvent, AnomalySilence } from '@/lib/types';
import type { ChartTimeRegion } from '@/lib/chart/overlays';

export const ANOMALY_KIND_COLOR: Record<AnomalyEvent['kind'], string> = {
  trace_op: 'var(--orange)',
  trace_op_latency: 'var(--orange)',
  log_pattern: 'var(--warn)',
  log_template_new: 'var(--warn)',
  elastic_ml: 'var(--warn)',
  behavior_change: 'var(--teal)',
};

export const ANOMALY_KIND_TR: Record<AnomalyEvent['kind'], string> = {
  trace_op: 'trace_op',
  trace_op_latency: 'trace_op gecikme',
  log_pattern: 'log_pattern',
  log_template_new: 'yeni log şablonu',
  elastic_ml: 'elastic ML',
  behavior_change: 'davranış',
};

/** Pencereyle kesişen, bu servise ait olaylar; başlangıca göre artan. */
export function windowAnomalies(items: AnomalyEvent[] | undefined, service: string, fromNs: number, toNs: number): AnomalyEvent[] {
  if (!items || !service) return [];
  return items
    .filter(e => e.service === service && e.startedAt <= toNs && e.lastSeen >= fromNs)
    .sort((a, b) => a.startedAt - b.startedAt);
}

/** Aktif susturmaların parmak izi kümesi. */
export function silencedSet(silences: AnomalySilence[] | undefined): Set<string> {
  const s = new Set<string>();
  for (const x of silences ?? []) if (x.active) s.add(x.fingerprint);
  return s;
}

/** Susturma anahtarı — /anomalies sayfasının yazdığı biçim (`kind|pattern|service`, streams.tsx onMute). */
export function silenceKey(e: Pick<AnomalyEvent, 'kind' | 'pattern' | 'service'>): string {
  return `${e.kind}|${e.pattern}|${e.service}`;
}

/** Olay susturulmuş mu — hem olay id'si hem `kind|pattern|service` anahtarı kabul (iki yazım da sahada). */
export function isSilenced(e: AnomalyEvent, silenced: Set<string>): boolean {
  return silenced.has(e.id) || silenced.has(silenceKey(e));
}

export function anomalyRegions(events: AnomalyEvent[], silenced: Set<string>, fromNs: number, toNs: number): ChartTimeRegion[] {
  const minWidthNs = Math.max(1e9, (toNs - fromNs) * 0.004);
  return events.map(e => {
    const muted = isSilenced(e, silenced);
    const endNs = Math.max(e.lastSeen, e.startedAt + minWidthNs);
    return {
      fromSec: e.startedAt / 1e9,
      toSec: endNs / 1e9,
      color: muted ? 'var(--text3)' : ANOMALY_KIND_COLOR[e.kind] ?? 'var(--warn)',
      // ▮ öneki YOK: drawTimeRegions kendisi ekler (overlays.ts fitLabel('▮ ' + label)).
      label: muted ? 'sessiz' : `${ANOMALY_KIND_TR[e.kind] ?? e.kind} ×${e.peakRatio.toFixed(1)}`,
    };
  });
}
