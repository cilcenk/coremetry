// topicHref.ts — v0.10.575 (Messaging topic detay sayfası). SAF: `/messaging/topic`
// rotasının URL sözleşmesi — kimlik, sekme ve pencere.
//
// KİMLİK ÜÇ AYRI PARAM (`?system=&cluster=&destination=`), çekmecenin
// `?destination=<enc|enc|enc>` BİLEŞİK kodeği (destinationParam.ts) DEĞİL.
// Gerekçe: o kodek çekmecenin AÇILIŞ parametresi — tek bir alanda "hangi satır
// açık" taşıyor. Sayfa ise adresin KENDİSİ: elle düzenlenen, paylaşılan ve
// başka yüzeylerden kurulan bir link üç alanı ayrı ayrı göstermeli, yoksa
// operatör hangi alanı değiştirdiğini göremez. İki kodek yan yana yaşıyor ve
// birbirine dönüşmüyor — bileşik olan yalnız /messaging listesinin çekmecesinde.
//
// `cluster` MECBURİ: sunucu yüklemi TAM EŞİTLİK (v0.9.973 dersi). Boş bir
// cluster ile açılan sayfa, çok-cluster kurulumda canlı bir topic için
// sıfırlanmış sayılar gösterir ve bu "topic boşta" ile ayırt edilemez. Bu
// yüzden ref çözümlemesi cluster boşken null döner, sayfa da "linkte cluster
// yok" der — sessizce "(default)" varsaymaz.
import { windowRangeParam } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';

export const MSG_TOPIC_PATH = '/messaging/topic';

/** Sekme ekseni. Varsayılan `producers` — URL'de YAZILMAZ (Service.tsx deseni). */
export type MsgTopicTab = 'producers' | 'consumers' | 'operations' | 'clients' | 'partitions' | 'spannames';

export const MSG_TOPIC_TABS: readonly MsgTopicTab[] = [
  'producers', 'consumers', 'operations', 'clients', 'partitions', 'spannames',
];

export const MSG_TOPIC_DEFAULT_TAB: MsgTopicTab = 'producers';

/**
 * parseTopicTab — `?tab=` ham değerini sekmeye çevirir.
 *
 * Tanınmayan değer VARSAYILANA düşer: eski/bozuk bir derin link boş bir
 * gövde çizmez. (Bilinmeyen bir sekme adında hiçbir sekme `active` olmaz ve
 * hiçbir tab gövdesi render edilmezdi — sessiz boş sayfa.)
 */
export function parseTopicTab(raw: string | null | undefined): MsgTopicTab {
  const t = (raw ?? '').trim();
  return (MSG_TOPIC_TABS as readonly string[]).includes(t) ? (t as MsgTopicTab) : MSG_TOPIC_DEFAULT_TAB;
}

export interface MsgTopicRef {
  system: string;
  cluster: string;
  destination: string;
}

/**
 * parseTopicRef — `?system=&cluster=&destination=` üçlüsü. Üçünden biri boşsa
 * null: sayfa "linkte topic yok" boş durumunu çizer, YARIM bir sorgu atmaz.
 * `search` `?`'li ya da `?`'siz gelebilir (useSearchParams.toString() `?`'siz).
 */
export function parseTopicRef(search: string): MsgTopicRef | null {
  const sp = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);
  const system = (sp.get('system') ?? '').trim();
  const cluster = (sp.get('cluster') ?? '').trim();
  const destination = (sp.get('destination') ?? '').trim();
  if (!system || !cluster || !destination) return null;
  return { system, cluster, destination };
}

/**
 * messagingTopicHref — sayfaya giden TEK link üreticisi.
 *
 * `range` OPSİYONEL ama çağıranların hepsi geçiriyor: pencereyi düşüren bir
 * link, custom pencerede sessizce başka bir zamanı gösterir (serviceHref /
 * tracesPivotHref ailesinin dört kez ısıran sınıfı). windowRangeParam
 * kullanılıyor ki ns→ms yuvarlaması pencereyi DARALTMASIN.
 */
export function messagingTopicHref(p: {
  system: string;
  cluster: string;
  destination: string;
  range?: TimeRange | { fromNs: number; toNs: number } | string | null;
  tab?: MsgTopicTab;
}): string {
  const sp = new URLSearchParams();
  sp.set('system', p.system);
  sp.set('cluster', p.cluster);
  sp.set('destination', p.destination);
  if (p.range) {
    const r = typeof p.range === 'string' ? p.range : windowRangeParam(p.range);
    if (r) sp.set('range', r);
  }
  // Varsayılan sekme URL'e YAZILMAZ — link kısa kalır ve "tab yok" ile
  // "tab=producers" aynı adresi üretir (iki farklı paylaşılabilir URL olmaz).
  if (p.tab && p.tab !== MSG_TOPIC_DEFAULT_TAB) sp.set('tab', p.tab);
  return `${MSG_TOPIC_PATH}?${sp.toString()}`;
}
