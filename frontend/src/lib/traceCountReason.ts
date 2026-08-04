// v0.9.638 — sayım REDDEDİLDİĞİNDE operatöre sebebini söyle.
//
// Bazı şekiller trace_summary_5m'de ucuza sayılamıyor. "Yanlış sayı,
// sayı yokluğundan kötüdür" ilkesinin devamı: PAHALI sayı da dürüst bir
// retten kötüdür — 25 saniye bekletip zaman aşımına düşmektense
// nedenini söyleyip listeyi hızlı tutuyoruz.
//
// Sessiz bir "—" da yeterli değildi: operatör sayının neden gelmediğini
// bilmezse filtreyi gevşetmeyi denemez.

import type { TraceCountResponse } from './types';

type Reason = NonNullable<TraceCountResponse['reason']>;

const HINTS: Record<Reason, string> = {
  // Filtre MV hızlı yolunu zaten kapatıyor; sayım ham spans üzerinde
  // olurdu ve tam da kaçındığımız pahalı tarama bu.
  'raw-path-filter':
    'Bu filtre kombinasyonu özet tablosundan karşılanamıyor (arama, trace id, env ya da attribute filtresi). ' +
    'Sayım ham span taraması gerektirir; listeyi yavaşlatmamak için atlandı.',
  // Süre trace_summary_5m'de iki merge state'in farkı — her satır için
  // finalize gerekir, LIMIT erken duramaz.
  'duration-filter':
    'Süre filtresi (min/max ms) açıkken toplam ucuza sayılamıyor: süre özet tablosunda hesaplanmış bir alan değil. ' +
    'Süre filtresini kaldırınca toplam görünür.',
  // Liste bunu iki tabloyu bağlayan bir alt-sorguyla karşılıyor; aynısı
  // sayımda DISTINCT'in erken durmasını öldürüyor.
  'service+filter':
    'Servis filtresi ile hata/kök filtresi birlikteyken toplam ucuza sayılamıyor. ' +
    'İkisinden birini kaldırınca toplam görünür.',
};

/** traceCountReasonHint — ret sebebini operatör diline çevirir. */
export function traceCountReasonHint(reason: string): string {
  return HINTS[reason as Reason] ?? 'Bu sorgu için toplam sayı hesaplanamıyor.';
}
