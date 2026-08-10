// sourceHref.ts — GroupTable satırından KAYNAĞA pivot (v0.9.930).
//
// SAF. React yok, navigate yok: bir BuilderQuery + satırın (anahtar, değer)
// çiftleri + pencere alır, bir /traces URL'i ya da "neden olmaz" cümlesi
// döndürür. Böylece her dal tabloyla sınanabiliyor.
//
// NE ÇÖZÜYOR. Panelde bir seri fırlamış; operatörün bir sonraki sorusu her
// zaman aynı: "bunu ÜRETEN spanlar hangileri?". Bugüne dek cevabı yoktu —
// ⊕ çipi paneli daraltıyor ama ham kayıtlara inmiyor. Enter artık o satırın
// popülasyonuna iniyor.
//
// ⊕/⊖ İLE ALTERNATİF DEĞİL, TAMAMLAYICI: onlar sorguyu YERİNDE daraltır
// (aynı sayfada kal, karşılaştırmaya devam et), bu ise sinyali DEĞİŞTİRİR
// (toplamdan ham kayda in). İkisi aynı satırda yan yana durur.
//
// EL YAPIMI QUERY-STRING YASAK. Hedef URL `tracesPivotHref` ile kuruluyor,
// çünkü pencereyi düşürmek bu depoda DÖRT kez ayrı ayrı gemiye gitti
// (v0.9.208, v0.9.213 ×3) ve başarısızlığı görünmez: hedef boş liste çizer,
// operatör bunu "böyle bir trace yok" diye okur, oysa yanlış saate bakıyordur.
// `window` o yüzden burada da ZORUNLU argüman.
//
// "OLMAZ"I SESSİZ NO-OP YAPMIYORUZ. Her reddin bir cümlesi var ve çağıran
// onu pasif düğmenin title'ına basıyor — v0.9.848'in çok boyutlu ⊖ kararıyla
// aynı dil: yaklaşık bir cevabı doğruymuş gibi göstermektense düğme gri kalır.

import { tracesPivotHref, type TracesPivot } from '@/lib/pivotHref';
import { encodeFilters, encodeFilterGroup } from '@/lib/urlState';
import type { FilterExpr, FilterGroup } from '@/lib/types';
import { hasGroupedFilter, type BuilderQuery, type PivotPair } from './model';

export type SourceTarget =
  | { ok: true; href: string }
  | { ok: false; why: string };

// exploreSourceHref — satırın kaynağına giden bağlantı, ya da gerekçe.
//
// pairs BOŞ olabilir: splitsiz bir panelin tek satırı da bir popülasyondur
// (sorgunun kendi filtreleri). "Çift yoksa pivot yok" deseydik split
// kullanmayan operatör bu yolu hiç göremezdi.
export function exploreSourceHref(
  q: BuilderQuery,
  pairs: PivotPair[],
  window: TracesPivot['window'],
): SourceTarget {
  // METRİK SORGUSUNUN HAM KAYNAK LİSTESİ YOK. metric_points bir zaman
  // serisidir; "bu noktayı üreten kayıtlar" diye gezilebilir bir yüzeyi
  // yoktur. Spana pivotlamak (exemplar) ancak sorgu rollup katmanlarından
  // servis edilebiliyorsa geçerlidir ve o AYRI bir yol — burada yalanmış
  // gibi davranmaktansa sebebi söylüyoruz.
  if (q.source !== 'span') {
    return { ok: false, why: 'Metrik sorgusunun gezilebilir ham kaynağı yok — kaynağa iniş yalnız span sinyalinde' };
  }
  // DSL /traces'e TAŞINAMAZ. Sayfanın okuduğu paramlar arasında `dsl` yok
  // (Traces.tsx URL sözleşmesi), yani DSL'li bir sorgudan pivot, panelin
  // gösterdiğinden DAHA GENİŞ bir liste açardı — "hedef başka bir soruyu
  // cevaplıyor" sınıfının ta kendisi.
  if (q.dsl.trim()) {
    return { ok: false, why: 'Gelişmiş DSL /traces sorgusuna taşınamıyor — kaynağa iniş panelden daha geniş bir liste açardı' };
  }

  const pairChips: FilterExpr[] = pairs.map(p => ({ k: p.k, op: '=' as const, v: [p.v] }));

  // Scope `service=` paramıyla taşınıyor (filtre çipi olarak DEĞİL): /traces
  // kendi servis kontrolünü o paramdan besliyor, yani hedefte narrowing
  // GÖRÜNÜR oluyor. Çipe gömseydik filtre satırında kaybolurdu.
  const base = {
    window,
    service: q.scope || undefined,
    view: 'list' as const,
    // Explore'un span sorgusu HERHANGİ bir spanı ölçer (çocuklar dahil);
    // /traces'in rootOnly varsayılanı açık kalsaydı liste boş çıkardı —
    // v0.8.585 sınıfı.
    rootOnly: false,
  };

  if (!hasGroupedFilter(q)) {
    return {
      ok: true,
      href: tracesPivotHref({ ...base, filters: encodeFilters([...q.filters, ...pairChips]) }),
    };
  }

  // GRUPLU (gerçek OR / iç içe) filtre. /traces `filterGroup=` okuyor, yani
  // ⊕ pivotunun aksine bu hedef gruplu predicate'i İFADE EDEBİLİYOR — o
  // yüzden burada reddetmiyoruz (pivotQuery reddediyor çünkü düz bir çip
  // Explore'un kendi fetch'ine hiç girmezdi).
  const g = q.filterGroup as FilterGroup;
  const merged = mergeAndChips(g, pairChips);
  if (!merged) {
    return { ok: false, why: 'İç içe gruplu filtre + satır daraltması /traces\'in tek seviyeli grup kodlamasına sığmıyor' };
  }
  const encoded = encodeFilterGroup(merged);
  // encodeFilterGroup düz-AND gruba '' döndürür (back-compat: onu `filters=`
  // taşır). hasGroupedFilter true iken buraya normalde düşmeyiz, ama boş
  // string'i sessizce `filterGroup=` yapmak FİLTRESİZ bir liste açardı —
  // messagingTracesHref'i ısıran tuzağın aynısı. Düz parama geri düşüyoruz.
  if (!encoded) {
    return {
      ok: true,
      href: tracesPivotHref({ ...base, filters: encodeFilters([...(merged.filters ?? []), ...pairChips]) }),
    };
  }
  return { ok: true, href: tracesPivotHref({ ...base, filterGroup: encoded }) };
}

// mergeAndChips — çipleri gruba AND'leyerek ekle, DERİNLİĞİ ARTIRMADAN.
// null = ifade edilemez.
//
// Neden derinlik kritik: `encodeFilterGroup` alt grupları yalnız
// {join, filters} olarak seri hâle getiriyor — bir alt grubun KENDİ
// `groups`u sessizce DÜŞÜYOR (types.ts: "≤1 level of nesting in v1").
// Mevcut grubu körlemesine sarsaydık iç içe bir sorguda daraltmanın bir
// kısmı yolda kaybolur ve hedef sessizce DAHA GENİŞ bir liste açardı.
export function mergeAndChips(g: FilterGroup, chips: FilterExpr[]): FilterGroup | null {
  if (chips.length === 0) return g;
  // Kök AND ise çipler kökün yanına gider: derinlik değişmez.
  if ((g.join ?? 'AND') === 'AND') {
    return { ...g, filters: [...(g.filters ?? []), ...chips] };
  }
  // Kök OR ise çipleri AND'lemek bir sarmalama gerektirir. Yalnız OR'un
  // kendi alt grubu YOKSA güvenli — varsa sarmalama onları düşürürdü.
  if (g.groups && g.groups.length > 0) return null;
  return { join: 'AND', filters: chips, groups: [{ join: 'OR', filters: g.filters ?? [] }] };
}
