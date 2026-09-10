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

// v0.10.595 — GRAFİK/TOOLTIP etiketleri kısa. Operatör ekranı: her tooltip
// satırı "servis · consumer-…-<uuid>-7" idi, altı panel okunmuyordu.
//   1. blokta tüm serilerde aynı olan sütun düşer (kapsam başlıkta zaten),
//      en az bir sütun kalır
//   2. UUID ilk 8 karaktere iner; sondaki -N örnek indeksi KALIR
//   3. kısaltma iki seriyi çakıştırırsa o seriler TAM etikete düşer
// Son-değer tablosu (kafkaLastRows) TAM etiketi korur — sütunu var.
const UUID_RE = /\b([0-9a-f]{8})-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi;

export function kafkaShortLabels(series: Array<{ groupKey?: string[] | null }>): string[] {
  const keys = series.map(s => (s.groupKey ?? []).map(v => (v ?? '').trim()));
  const cols = Math.max(0, ...keys.map(k => k.length));
  const keep: number[] = [];
  for (let c = 0; c < cols; c++) {
    const vals = new Set(keys.map(k => k[c] ?? ''));
    if (vals.size > 1) keep.push(c);
  }
  // Hepsi ortaksa (tek seri ya da aynı anahtar) en az bir sütun kalsın.
  const cols2 = keep.length ? keep : (cols ? [cols - 1] : []);
  const short = keys.map(k => {
    const parts = cols2.map(c => (k[c] ?? '').replace(UUID_RE, '$1…')).filter(Boolean);
    return parts.length ? parts.join(' · ') : '(tümü)';
  });
  const seen = new Map<string, number>();
  for (const l of short) seen.set(l, (seen.get(l) ?? 0) + 1);
  return short.map((l, i) => (seen.get(l)! > 1 ? kafkaSeriesLabel(series[i].groupKey) : l));
}

export function kafkaBlockItems(block: KafkaMetricBlock | null | undefined): { items: CorePanelMultiItem[]; truncated: number } {
  const series = block?.series ?? [];
  const shown = series.slice(0, KAFKA_SERIES_CAP);
  const names = kafkaShortLabels(shown);
  return {
    items: shown.map((s, i) => ({ name: names[i], role: 'data' as const, series: [s] })),
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

// ── v0.10.575 — /messaging/topic üst grafiği (`set=chart`) ───────────────────

/** Üst grafiğin İKİ bloğu. Sıra çizim sırası: gönderilen üstte, tüketilen altta. */
export const KAFKA_CHART_KEYS = ['producer_send_rate', 'consumer_consumed_rate'] as const;

/** Blok başına görünen ad öneki — iki blok tek panelde, hangi tarafın kim olduğu yazılı. */
const CHART_PREFIX: Record<string, string> = {
  producer_send_rate: 'gönderilen',
  consumer_consumed_rate: 'tüketilen',
};

/**
 * kafkaChartItems — iki bloğu TEK panelin item listesine indirir.
 *
 * Ad ÖNEKLİ ('gönderilen · loan-api'): öneksiz iki blok aynı servis adını
 * taşıyabilir ve lejantta iki özdeş satır olurdu — hangisinin üretim hangisinin
 * tüketim olduğu ancak renkten tahmin edilirdi. Tavan blok BAŞINA uygulanır
 * (kafkaBlockItems), yani bir taraf 40 servisliyse diğer taraf onun altında
 * kaybolmaz.
 */
export function kafkaChartItems(
  blocks: Record<string, KafkaMetricBlock> | null | undefined,
): { items: CorePanelMultiItem[]; truncated: number } {
  const items: CorePanelMultiItem[] = [];
  let truncated = 0;
  for (const key of KAFKA_CHART_KEYS) {
    const { items: got, truncated: cut } = kafkaBlockItems(blocks?.[key]);
    truncated += cut;
    for (const it of got) items.push({ ...it, name: `${CHART_PREFIX[key] ?? key} · ${it.name}` });
  }
  return { items, truncated };
}

/**
 * kafkaScopeNoteTR — kapsam beyanı. null = beyan gerekmez (topic'e daraltılmış).
 *
 * 'services' hâlinde bunu YAZMAK zorunlu: Kafka istemci metrikleri topic
 * etiketi taşımıyor, yani seriler bu topic'e dokunan SERVİSLERİN tamamı.
 * Beyansız bir grafik, başka bir topic'in yükünü buranın sanıp yanlış suçlu
 * gösterir — "boş küme kaybolur, sıfır olmaz"ın kardeşi: DAR SANILAN küme.
 */
export function kafkaScopeNoteTR(scope: string | null | undefined): string | null {
  return scope === 'services'
    ? "Bu topic'e dokunan servisler — metrik topic'e göre süzülemez"
    : null;
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
