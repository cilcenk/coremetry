// routeSeries — Service Overview "Response time · avg (by route)" panelinin
// SAF yardımcıları (v0.9.774).
//
// NEDEN AYRI DOSYA: panelin iki kararı da test edilebilir olmalı.
//
//   1) KIRPMA. Metrik yolunda SUNUCU TOP-N YOK — /api/metrics/query
//      (chstore.QueryMetric) grup başına bir seri döndürür ve yalnız 50k
//      SATIR tavanına çarpar. 65 route'lu bir serviste panel 65 çizgi
//      çizerdi; span türevli kardeşi (rootOpSeries) sunucu top-N'i olmasa
//      da 5'e kırpıyor. Burada kırpma İSTEMCİDE ve SÖYLENİYOR.
//
//   2) BİRİM. Yüzdelik yolu silinince (v0.9.774) ms'ye çeviren sunucu
//      kodu da gitti; artık ham değer + OTLP birimi geliyor ve ekseni
//      @grafana/data'nın display processor'ı biçimlendiriyor. Eşleme
//      DEĞER+BİRİM şablonudur: bu kod tabanının kayıtlı dersi, her dalın
//      ship anında test edilmesi (feedback-unit-mixing-needs-both-
//      branches) — prod 's', lokal 'ms' üretiyor, yani eksen-dışı dal
//      GERÇEKTEN canlıda çalışıyor.
//
// Kırpma ölçütü ALAN (Σ|değer|): Explore'un panel slotu (PanelStack.tsx
// "Biggest-by-area win the panel slots") ve rootOpSeries AYNI ölçütü
// kullanıyor — üçüncü bir sıralama fikri icat etmiyoruz.

import type { SpanMetricSeries } from '@/lib/types';
import { rankRootOps, rootOpName } from './rootOpSeries';

/** Kaç route çizilir. 10: panel 200px ve lejant varsayılan kapalı;
 *  daha fazlası okunmuyor. rootOpSeries'in 5'inden yüksek çünkü burada
 *  tek agg var (orada her operasyon zaten kendi P95 çizgisi). */
export const ROUTE_TOP_N = 10;

export interface RouteItem {
  name: string;
  role: 'data';
  series: SpanMetricSeries[];
}

export interface RouteItems {
  items: RouteItem[];
  /** Dürüstlük notu (yoksa null). */
  note: string | null;
}

// routeMoreNote — İKİ AYRI eksiklik, ikisi de söylenir:
//   • more       — evrende daha çok route var, çizilen ilk N (alan bazlı).
//   • rowsCapped — sorgu 50k satır tavanına çarptı: "+N daha" bile
//                  gerçek evreni bilmiyor.
// more=0 iken taban not YOK (rootOpMoreNote deseni): lejant zaten
// "Series (N)" yazıyor, tekrarı gürültü.
export function routeMoreNote(total: number, shown: number, cap: number, rowsCapped = false): string | null {
  const more = Math.max(0, total - shown);
  const base = more > 0
    ? `${shown} seri · +${more} daha (alan bazlı: Σ|değer| en yüksek ${cap})`
    : null;
  if (!rowsCapped) return base;
  const capped = '⚠ satır tavanı doldu — liste eksik olabilir';
  return base ? `${base} · ${capped}` : capped;
}

// topRoutesByArea — ham seri kümesi → panel item'ları + not.
//
// Hizalama YOK: CorePanel'in frame birleşimi (framesToAligned) zaman
// eksenini kendisi kurar ve eksik bucket null olur (sıkı doktrin) —
// buildRootOpItems ile aynı gerekçe.
export function topRoutesByArea(
  series: readonly SpanMetricSeries[] | null | undefined,
  cap = ROUTE_TOP_N,
  rowsCapped = false,
): RouteItems {
  const all = series ?? [];
  const top = rankRootOps(all, cap);
  return {
    items: top.map(s => ({ name: rootOpName(s.groupKey), role: 'data' as const, series: [s] })),
    note: routeMoreNote(all.length, top.length, cap, rowsCapped),
  };
}

// ── Birim eşlemesi ────────────────────────────────────────────────────────
//
// OTLP birimi → @grafana/data birim kimliği. Sözleşme dar BİLEREK:
// tanınan iki süre birimi ('s', 'ms') eşlenir, kalan her şey undefined
// döner ve panel eksenini ham sayı olarak (fmtSmart) çizip notta
// "birim tanınmadı" der.
//
// TAHMİN YOK: boş birimli bir süre metriği saniye de olabilir milisaniye
// de, ve yanlış ölçekli bir grafik ölçeksiz olandan kötüdür — operatör
// ona güvenir (v0.9.676'da silinen LatencyScaleToMs'in de gerekçesiydi;
// karar burada yaşamaya devam ediyor, ölçekleme değil ETİKETLEME olarak).
// withTotalPrefix — kırpma notuna "Toplam + " öneki (v0.9.798).
//
// Panelde artık N route serisinin YANINDA bir de "Toplam" çizgisi var.
// Not "10 seri · +55 daha" derse operatör çizilen 11 çizgiyi sayıp
// notu yalancı bulur — ve haklıdır: Toplam bir route DEĞİL, kırpmanın
// dışında duran ayrı bir ölçüm.
//
// Taban not YOKKEN (kırpma yok) önek de yok: lejant zaten seri sayısını
// yazıyor, "Toplam +" tek başına gürültü.
export function withTotalPrefix(note: string | null, hasTotal: boolean): string | null {
  if (!note || !hasTotal) return note;
  return `Toplam + ${note}`;
}

// metricAvgToMs — metrik ORTALAMASINI KPI karosunun milisaniyesine
// çevirir (v0.9.798). Bilinmeyen birimde null → çağıran span'e düşer.
//
// BU BİR ŞABLON DEĞİL, TAM DÖNÜŞÜM: 's' → ×1000, 'ms' → aynen. Bu kod
// tabanının kayıtlı dersi (feedback-unit-mixing-needs-both-branches) her
// iki dalın da ship anında test edilmesi — prod 's', lokal 'ms'
// üretiyor, yani eksen-dışı dal GERÇEKTEN canlıda çalışıyor ve testi
// yazılmazsa yanlış olan taraf prod tarafı olur.
//
// TAHMİN YOK: tanınmayan/boş birimde null döner. metricUnitToGrafana'nın
// gerekçesiyle aynı (v0.9.676'da silinen LatencyScaleToMs) — yanlış
// ölçekli bir sayı, ölçeksiz olandan kötüdür çünkü operatör ona güvenir.
// Karo "boş kalmaz": çağıran span türevli P99'a düşer ve bunu SÖYLER.
export function metricAvgToMs(value: number | null | undefined, unit: string | undefined): number | null {
  if (value == null || !isFinite(value)) return null;
  switch (metricUnitToGrafana(unit)) {
    case 'ms': return value;
    case 's': return value * 1000;
    default: return null;
  }
}

export function metricUnitToGrafana(u: string | undefined): 's' | 'ms' | undefined {
  switch ((u ?? '').trim().toLowerCase()) {
    case 's':
    case 'sec':
    case 'secs':
    case 'second':
    case 'seconds':
      return 's';
    case 'ms':
    case 'millisecond':
    case 'milliseconds':
      return 'ms';
    default:
      return undefined;
  }
}
