// kafkaClients.ts — v0.10.551 (Messaging Kafka Faz 2). SAF: /api/messaging/clients
// zarfını çekmece bölümünün çizdiği şekle çevirir. Seri etiketi = groupKey
// birleşimi; "son değer" = serinin son noktası; servis-düzeyi blok (groupBy
// [service.name]) istemci-düzeyi satıra SERVİS anahtarıyla düşer (tük/s aynı
// servisin her istemcisinde tekrarlar — sütun başlığı "(servis)" der).
import type { CorePanelMultiItem } from '@/components/chart/corePanelEntry';
import type { KafkaMetricBlock, MessagingClients, SpanMetricSeries } from '@/lib/types';

/** Çekmece kartında okunabilir seri tavanı (E2E paneliyle aynı genişlik). */
export const KAFKA_SERIES_CAP = 12;

export function kafkaSeriesLabel(groupKey: string[] | null | undefined): string {
  const parts = (groupKey ?? []).map(s => (s ?? '').trim()).filter(Boolean);
  return parts.length ? parts.join(' · ') : '(tümü)';
}

export function kafkaBlockItems(block: KafkaMetricBlock | null | undefined): { items: CorePanelMultiItem[]; truncated: number } {
  const series = block?.series ?? [];
  const shown = series.slice(0, KAFKA_SERIES_CAP);
  return {
    items: shown.map(s => ({ name: kafkaSeriesLabel(s.groupKey), role: 'data' as const, series: [s] })),
    truncated: Math.max(0, series.length - shown.length),
  };
}

export function kafkaLastValue(s: SpanMetricSeries | null | undefined): number | null {
  const pts = s?.points ?? [];
  for (let i = pts.length - 1; i >= 0; i--) {
    const v = pts[i]?.value;
    if (typeof v === 'number' && Number.isFinite(v)) return v;
  }
  return null;
}

export interface KafkaLastRow { key: string; service: string; values: Record<string, number | null> }

/** Verilen blok anahtarlarının son değerlerini seri etiketine göre birleştirir. */
export function kafkaLastRows(blocks: Record<string, KafkaMetricBlock> | null | undefined, keys: string[]): KafkaLastRow[] {
  const rows = new Map<string, KafkaLastRow>();
  const byService: Record<string, Map<string, number | null>> = {};
  for (const key of keys) {
    const block = blocks?.[key];
    if (!block) continue;
    for (const s of block.series ?? []) {
      const label = kafkaSeriesLabel(s.groupKey);
      const service = (s.groupKey?.[0] ?? '').trim();
      const v = kafkaLastValue(s);
      const row = rows.get(label) ?? { key: label, service, values: {} };
      row.values[key] = v;
      rows.set(label, row);
      (byService[key] ??= new Map()).set(service, v);
    }
  }
  const out = [...rows.values()];
  for (const row of out) {
    for (const key of keys) {
      if (!(key in row.values)) row.values[key] = byService[key]?.get(row.service) ?? null;
    }
  }
  return out.sort((a, b) => a.key.localeCompare(b.key));
}

/** Panel birimi: yalnız zaman birimleri eksen biçimine gider; sayım/oran ham sayı (sessiz 'ms' varsayımı yok). */
export function kafkaPanelUnit(unit: string | null | undefined): string | undefined {
  return unit === 'ms' || unit === 's' ? unit : undefined;
}

/** null = bölüm tam çizilir; string = tek satırlık düşüş metni. */
export function kafkaDegradeTR(r: Pick<MessagingClients, 'available'> | null | undefined): string | null {
  if (r === null) return 'Kafka istemci metriği okunamadı (kaynak hatası) — span türevli görünüm.';
  if (!r) return null;
  if (!r.available) {
    return 'Kafka istemci metriği yok — servisler OTel Java agent ile enstrümante değil ya da metrik deposu yapılandırılmamış; span türevli görünüm.';
  }
  return null;
}

export const KAFKA_PRODUCER_COLS = [
  { id: 'producer_send_rate', label: 'gönd/s' },
  { id: 'producer_error_rate', label: 'hata/s' },
  { id: 'producer_retry_rate', label: 'yeniden/s' },
] as const;
export const KAFKA_CONSUMER_COLS = [
  { id: 'consumer_consumed_rate', label: 'tük/s (servis)' },
  { id: 'consumer_lag_max', label: 'lag (maks.)' },
] as const;
