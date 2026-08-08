// pages/metricsCatalog.ts — /metrics KATALOG görünümünün saf çekirdeği:
// facet sınıflandırması, URL kodeki ve DÜRÜST sayaç metinleri.
//
// Neden ayrı modül:
//
//  1. DÜRÜSTLÜK. Katalog sunucudan `ORDER BY metric LIMIT n` ile gelir.
//     Sayfa facet sayılarını ve sıralamayı GELEN SAYFA üzerinde
//     hesaplıyordu ama başlıkta bunu hiç söylemiyordu — sunucunun
//     döndürdüğü `total` atılıyordu. 114 metriklik bir kurulumda fark
//     görünmez; binlerce metrikli bir kurulumda "HTTP 37" yazısı tüm
//     kataloğun sayısıymış gibi okunur ve YANLIŞTIR. Sayıyı üreten
//     fonksiyonlar burada, testli.
//
//  2. URL = TEK KAYNAK. Servis/arama/facet seçimi paylaşılabilir
//     olmalı (ev kuralı: her operatör seçimi replace:true ile URL'e).
//     Kodek saf olunca gidiş-dönüş tablo ile çivilenir — "yazarken
//     varsayılanı atla, okurken varsayılana düş" ikilisinin ayrışması
//     bu repoda dört kez bug oldu (v0.8.253/256/265/267, v0.9.561).
//
//  3. YABANCI PARAM KORUMA. applyCatalogParams her zaman `prev`
//     kopyalar; `range` / `editor` gibi bu modülün bilmediği paramlar
//     asla düşmez.

export type MGroup = 'http' | 'rpc' | 'runtime' | 'db' | 'messaging' | 'other';
export type MFacet = MGroup | 'all';

// Çip seti metricGroup'un TÜM çıktılarını kapsar. 'other' eskiden
// eksikti: sınıflandırma onu üretiyor, sayaç onu sayıyor, ama çipi
// yoktu — yani iş-alanı metrikleri (banking.*, cache.*) facet ile
// ERİŞİLEMEZDİ ve `?facet=other` sessizce 'all'a düşerdi. Kapsayan bir
// çip seti kodeğin de tam olmasını sağlar (gidiş-dönüş testi).
export const METRIC_FACETS: { key: MFacet; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'http', label: 'HTTP' },
  { key: 'rpc', label: 'RPC' },
  { key: 'runtime', label: 'Runtime' },
  { key: 'db', label: 'Database' },
  { key: 'messaging', label: 'Messaging' },
  { key: 'other', label: 'Other' },
];

const FACET_KEYS = new Set<string>(METRIC_FACETS.map(f => f.key));

/** Sayfa boyu — sunucu ORDER BY metric prefix'i bu adımlarla büyür. */
export const CATALOG_PAGE = 200;
/** Sunucu tavanı (internal/api/api.go getMetricNames: limit > 1000 → 1000). */
export const CATALOG_MAX = 1000;

// Classify a metric into a facet group by its OTel name prefix (moved
// verbatim from the retired pages/metrics/MetricsExplorer, then here).
export function metricGroup(name: string): MGroup {
  const n = name.toLowerCase();
  if (n.startsWith('http')) return 'http';
  if (n.startsWith('rpc')) return 'rpc';
  if (n.startsWith('db') || n.startsWith('database') || /(redis|oracle|postgres|mysql|mongo)/.test(n)) return 'db';
  if (n.startsWith('messaging') || /(kafka|rabbit|queue|consumer)/.test(n)) return 'messaging';
  if (/^(jvm|process|go\.|system|runtime|dotnet|nodejs|python)/.test(n)) return 'runtime';
  return 'other';
}

/** URL → facet. Tanınmayan değer 'all'a düşer (elle yazılmış link boş ekran vermez). */
export function decodeFacet(raw: string | null | undefined): MFacet {
  return raw && FACET_KEYS.has(raw) ? (raw as MFacet) : 'all';
}

/** Facet'in URL değeri. `null` = parametreyi SİL (varsayılan 'all'). */
export function facetUrlValue(f: MFacet): string | null {
  return f === 'all' ? null : f;
}

export interface CatalogParams {
  search: string;
  facet: MFacet;
  service: string;
}

export function decodeCatalogParams(sp: URLSearchParams): CatalogParams {
  return {
    search: sp.get('search') ?? '',
    facet: decodeFacet(sp.get('facet')),
    service: sp.get('service') ?? '',
  };
}

/**
 * Seçimleri `prev` üstüne yazar. Boş/varsayılan değer parametreyi SİLER,
 * yani temiz bir katalog URL'i temiz kalır ve kodek gidiş-dönüş yapar.
 * `prev`'teki her yabancı param (range, editor, …) korunur.
 */
export function applyCatalogParams(prev: URLSearchParams, p: CatalogParams): URLSearchParams {
  const next = new URLSearchParams(prev);
  const put = (k: string, v: string | null) => { if (v) next.set(k, v); else next.delete(k); };
  put('search', p.search.trim());
  put('facet', facetUrlValue(p.facet));
  put('service', p.service.trim());
  return next;
}

/**
 * Başlığın DÜRÜST sayacı.
 *
 * `total` sunucunun eşleşen metrik sayısıdır; `listed` gerçekten inen
 * satır sayısı. İkisi eşitse tek sayı yeter; değilse ekranın bir
 * PREFIX olduğu açıkça yazılır — eski kod `total`ı hiç göstermiyordu.
 */
export function catalogCountLabel(total: number, listed: number): string {
  const t = Math.max(0, Math.floor(total));
  const l = Math.max(0, Math.floor(listed));
  if (l === 0) return t > 0 ? `${t.toLocaleString()} metrics` : 'No metrics';
  if (l < t) return `showing ${l.toLocaleString()} of ${t.toLocaleString()} metrics`;
  return `${t.toLocaleString()} metric${t === 1 ? '' : 's'}`;
}

/**
 * Facet sayıları TÜM kataloğu mu kapsıyor?
 *
 * false ise çipler yalnız inen satırları sayar ve arayüz bunu söylemek
 * ZORUNDADIR — sunucu-taraflı facet ayrı bir dilim (bilinçli olarak
 * bu sürümde yok).
 */
export function facetCountsComplete(total: number, listed: number): boolean {
  return listed >= total;
}

/**
 * Sunucunun katalog tazelik TAVANI — chstore.metricNameLookback (7 gün).
 * Bundan uzun süredir susan bir metrik listeye HİÇ girmez; "Son veri"
 * kolonunun üst sınırı budur ve arayüz bunu söylemek zorundadır, yoksa
 * operatör kataloğu "her metrik" sanır.
 */
export const CATALOG_LOOKBACK_MS = 7 * 24 * 60 * 60 * 1000;

/**
 * Bu eşiği aşan satır SOLUK çizilir: "bugün veri gelmedi".
 *
 * Neden 24 saat, 7 gün değil: 7 gün sunucunun ELEME eşiği. 7 günü aşan
 * satır zaten listede olmadığı için "7g+ soluk" kuralı hiçbir satırı
 * boyayamazdı — ölü bir görsel. 24 saat ulaşılabilir ve anlamlı:
 * dünden beri susan metrik bakılmayı hak eder.
 */
export const METRIC_STALE_MS = 24 * 60 * 60 * 1000;

/**
 * Satır bayat mı? Bilinmeyen (0 / alan yok, v0.9.833 öncesi sunucu)
 * BAYAT DEĞİLDİR — bilmemek ile susmuş olmak aynı şey değil, ve
 * arayüz bilinmeyeni "—" basar.
 */
export function metricIsStale(lastSeenNs: number | undefined, nowMs: number): boolean {
  if (!lastSeenNs || lastSeenNs <= 0) return false;
  return nowMs - lastSeenNs / 1e6 > METRIC_STALE_MS;
}

/**
 * "Load more" bir sonraki limiti verir; daha fazlası yoksa null.
 *
 * Tavan sunucununkiyle AYNI (1000). Tavana varınca buton kaybolur ve
 * arayüz aramayı daraltmayı söyler — sessizce kırpmak bu sürümün
 * kaldırdığı yalanın ta kendisi.
 */
export function nextCatalogLimit(limit: number, hasMore: boolean): number | null {
  if (!hasMore) return null;
  if (limit >= CATALOG_MAX) return null;
  return Math.min(limit + CATALOG_PAGE, CATALOG_MAX);
}
