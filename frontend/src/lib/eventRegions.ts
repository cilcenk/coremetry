import type { ChartTimeRegion } from '@/lib/chart/overlays';

// eventRegions — operatör olaylarını chart-İÇİ bölgelere çeviren saf
// yardımcı (v0.9.1044, Ş3 kapanışı: "chart-içi ▼ tek desen, Endpoints'e
// paritesi"). v0.5.478'in DOM-overlay EventMarkers'ı grafiğin ÜSTÜNE
// mutlak-konumlu çizgi basıyordu: kendi yüzde hesabı sayfa penceresine
// bağlı, grafiğin gerçek x-scale'inden habersiz — zoom/pan'de kayar,
// ayrıca her karo kendi /api/events fetch'ini atar (3 karo = 3 istek;
// v0.9.396'nın ServiceCharts'ta söktüğü çift-iş). Bölge yolu grafiğin
// KENDİ ekseninde yaşar ve deploy işaretleriyle aynı desendir.
//
// Renkler EventMarkers.tsx'in v0.5.478 paletiyle AYNI KALIR — /events
// sayfasındaki kind çipleri de bu paleti kopyalar; kaynak artık burası.
export const EVENT_KIND_COLOUR: Record<string, string> = {
  deploy:      'rgba(46,160,67,0.65)',
  config:      'color-mix(in srgb, var(--accent) 65%, transparent)',
  incident:    'rgba(220,38,38,0.70)',
  maintenance: 'rgba(217,119,6,0.65)',
};
export const EVENT_DEFAULT_COLOUR = 'rgba(160,160,160,0.55)';

// Yapısal minimum — api.listEvents dönüşünün alt kümesi; tam OperatorEvent
// tipine bağlanmaz ki saf test api katmanını import etmesin.
export interface EventLike {
  time: number;  // unix ns
  kind: string;
  label: string;
}

// Sıfır-genişlik bölge = dikey işaret çizgisi (MultiLineChart'ın deploy
// eşlemesiyle birebir aynı sözleşme: fromSec === toSec).
export function operatorEventsToRegions(events: readonly EventLike[] | null | undefined): ChartTimeRegion[] {
  if (!events || events.length === 0) return [];
  return events.map(e => ({
    fromSec: e.time / 1e9,
    toSec: e.time / 1e9,
    color: EVENT_KIND_COLOUR[e.kind] ?? EVENT_DEFAULT_COLOUR,
    label: e.label ? `${e.kind} · ${e.label}` : e.kind,
  }));
}
