import type { SortState } from '@/lib/dataTable';

// aggSort — v0.9.878 (tutarlılık denetimi BT6, icra denetiminin R1 riski).
//
// /traces aggregate tablosu SUNUCU-SIRALI: `sort`/`order` API'ye gidiyor ve
// backend LIMIT 200 ile en ağır grupları döndürüyor. Yani sıralama bir
// görüntüleme tercihi değil, HANGİ 200 SATIRIN geldiğini belirleyen bir
// sorgu parametresi. Bu yüzden useDataTable `serverSort` kipinde kullanılıyor:
// sıralama UX'inin tamamı (URL, localStorage, başlık tıkı, ok yönü) primitiften
// geliyor ama satırlar YENİDEN SIRALANMIYOR — sayfa yeni sırayla re-fetch ediyor.
//
// R1 — SESSİZ KANAL ÇAKIŞMASI. Traces.tsx'in kendi URL yazıcısı sorgu
// dizesini HER state yazımında SIFIRDAN kuruyor (buildQuery listesi).
// Primitif `s_traces-agg` parametresini yazdıktan bir render sonra o yazıcı
// listede olmayan her parametreyi siler — yani paylaşılan sıralama linki
// sessizce kaybolurdu. Alıcının tarayıcısı kendi localStorage'ındaki sırayla
// açar: BAŞLIK p99 der, sunucu count'a göre sıralar. Ekranda hiçbir şey
// bozulmaz; sayılar yanlış sırada durur.
//
// Çözüm: kanal TEK. Sayfanın yazıcısı `s_traces-agg`i aggSort/aggOrder
// durumundan üretiyor, primitif de onu okuyor. Eski `?aggSort=`/`?aggOrder=`
// linkleri `decodeLegacyAggSort` köprüsüyle yaşıyor (Services'in
// decodeLegacyServicesSort emsali, v0.8.251).

// Kolon id'leri AggSort değerleriyle BİREBİR aynı tutuldu: araya bir eşleme
// tablosu koymak, iki tarafın ayrı ayrı bakım gerektiren bir kopyası olurdu.
export const AGG_SORT_IDS = [
  'name', 'count', 'perMin', 'errorRate', 'avg', 'p50', 'p95', 'p99', 'max',
] as const;

export type AggSort = typeof AGG_SORT_IDS[number];

const VALID = new Set<string>(AGG_SORT_IDS);

/**
 * Bir kolon id'sini (ya da URL'den gelen ham dizeyi) AggSort'a daraltır.
 * Tanınmayan değer için null — sunucuya bilinmeyen bir `sort` göndermek
 * 400 döndürür, bu yüzden daraltma sessiz düşmemeli, çağıran fallback'lemeli.
 */
export function toAggSort(colId: string | null | undefined): AggSort | null {
  return colId && VALID.has(colId) ? (colId as AggSort) : null;
}

/**
 * Eski (v0.9.877 öncesi) `?aggSort=p99&aggOrder=asc` linklerini primitifin
 * SortState'ine çevirir. useDataTable bunu `urlSortFallback` olarak alıyor:
 * önceliği `s_traces-agg`in ALTINDA ama localStorage'ın ÜSTÜNDE — paylaşılan
 * bir linkin niyeti, alıcının kişisel varsayılanını yenmeli.
 */
export function decodeLegacyAggSort(
  aggSort: string | null | undefined,
  aggOrder: string | null | undefined,
): SortState | null {
  const id = toAggSort(aggSort);
  if (!id) return null;
  return { id, dir: aggOrder === 'asc' ? 'asc' : 'desc' };
}
