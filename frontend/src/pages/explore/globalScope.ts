// pages/explore/globalScope.ts — global (sayfa-dışı) daraltmaların
// Explore sorgu hattına ENJEKSİYONU. UX denetimi B2 / K9, v0.9.942.
//
// SORUN: Explore `env` sözcüğünü hiç tanımıyordu. Topbar'ın EnvPicker'ı
// bu sayfada da basılıyor ve `?env=uat` adres çubuğunda duruyordu; sorgu
// ise TÜM ortamları okuyordu. Aynısı `?cluster=` için: EndpointDetail'in
// "Explore →" pivotu (pages/endpoints/links.ts) ikisini de URL'e yazıyor,
// Explore ikisini de yok sayıyordu — yani cluster=A altında okunan bir
// endpoint'in p99'u Explore'da BÜTÜN cluster'ların p99'u olarak açılıyordu.
// Kapsam yalanı, boş liste değil: en pahalı tür.
//
// ÇÖZÜM ŞEKLİ: daraltma BuilderState'e değil, FETCH'e giden kopyaya
// girer. Sebep üç katlı:
//
//  1. `?q=` temiz kalır — paylaşılan link operatörün KURDUĞU sorguyu
//     taşır, o an hangi env'de durduğunu değil. Env zaten kendi
//     paramında ve kendi picker'ında yaşıyor (tek kaynak).
//  2. Çip satırında GÖRÜNMEZ, dolayısıyla silinemez. Silinebilir bir
//     çip olsaydı operatör onu silip picker'ı "uat" gösterirken tüm
//     ortamları okurdu — düzeltmeye çalıştığımız yalanın aynısı.
//  3. querySignature ENJEKTE EDİLMİŞ sorgudan hesaplanıyor (çağıran
//     kapsanmış state'i veriyor), yani env/cluster cache anahtarına
//     KENDİLİĞİNDEN giriyor. Ayrı bir anahtar eki yazmak v0.5.187
//     çapraz-zehirlenme sınıfını yeniden açardı.
//
// NEDEN ÇİP, neden ayrı bir alan değil: env/cluster ÜÇ fetch yolunun
// (resolveMetric / spanMetricTopN / metricQueryFull) hepsinde
// filtre olarak geçerli.
//   • `deployment.environment` → spans'te typed `deploy_env` kolonu
//     (chstore/filterexpr.go wellKnown), metric_points'te res dizisi.
//   • `cluster` → iki tarafta da derive ifadesi (metricClusterExpr
//     zaten vardı; spans ikizi v0.9.942'de eklendi).
// Ayrıca çip olması ROLLUP HIZLI YOLUNU KENDİLİĞİNDEN diskalifiye
// ediyor: exemplarDescriptor TIER_DIM_KEYS dışındaki her anahtarda
// null döner, sorgu ham yola düşer ve daraltma GERÇEKTEN uygulanır.
// Rollup'ta env/cluster boyutu YOK (chstore/rollup_fastpath_test.go);
// sessizce yok sayılan bir daraltma, uygulanmayan bir daraltmadan
// beterdir.
//
// SAF modül — tablo testleri globalScope.test.ts.

import type { FilterExpr, FilterGroup } from '@/lib/types';
import { isFlatAndGroup } from '@/lib/urlState';
import type { BuilderState, BuilderQuery } from './model';

// ENV_FILTER_KEY — semconv ≥1.27 ADI DEĞİL, eski yazım. Bilinçli:
// backend iki yazımı da AYNI `deploy_env` kolonuna eşliyor
// (filterexpr.go wellKnown), ama metric_points tarafında eşleme
// yazıma göre res dizisinde aranıyor. Traces sayfası v0.8.383'ten beri
// bu yazımı gönderiyor; ikinci bir yazım göndermek aynı soruyu iki
// farklı cache anahtarına bölerdi.
export const ENV_FILTER_KEY = 'deployment.environment';
export const CLUSTER_FILTER_KEY = 'cluster';

/**
 * scopeChips — global daraltmaların filtre çipi karşılığı. Boş değer
 * çip ÜRETMEZ (yokluk = "hepsi", bir değer değil).
 */
export function scopeChips(env: string, cluster: string): FilterExpr[] {
  const out: FilterExpr[] = [];
  const e = (env ?? '').trim();
  const c = (cluster ?? '').trim();
  if (e) out.push({ k: ENV_FILTER_KEY, op: '=', v: [e] });
  if (c) out.push({ k: CLUSTER_FILTER_KEY, op: '=', v: [c] });
  return out;
}

// injectQuery — tek sorguya çipleri ekler.
//
// İKİ DALA DA yazılır ve bu ŞART: fetch katmanı gruplu bir sorguda düz
// `filters`i GÖNDERMEZ (effectiveFilterGroup supersedes — backend de
// filterGroup'u tercih eder). Yalnız düz listeye yazsaydık, OR'lu bir
// filtre kuran operatörde daraltma SESSİZCE düşerdi. Yalnız gruba
// yazsaydık gruppsuz sorgular daralmazdı. Çipler grubun EN ÜST
// seviyesine, yani her zaman AND tarafına iner — içerideki bir OR
// daraltmanın üstünden atlayamaz (effectiveFilterGroup'un scope leaf'i
// ile aynı gerekçe).
function injectQuery(q: BuilderQuery, chips: FilterExpr[]): BuilderQuery {
  const next: BuilderQuery = { ...q, filters: [...q.filters, ...chips] };
  if (q.filterGroup && !isFlatAndGroup(q.filterGroup)) {
    const g: FilterGroup = q.filterGroup;
    next.filterGroup = { ...g, filters: [...chips, ...(g.filters ?? [])] };
  }
  return next;
}

/**
 * applyGlobalScope — fetch'e gidecek BuilderState kopyası.
 *
 * Daraltma yoksa GİRDİ NESNESİ AYNEN döner (yeni nesne değil): çağıran
 * useMemo'da tutuyor ve useExploreQueries'in memo bağımlılığı `state`
 * kimliği. Yeni bir nesne döndürmek, env boşken bile her render'da
 * memo'yu düşürürdü.
 */
export function applyGlobalScope(
  st: BuilderState, env: string, cluster: string,
): BuilderState {
  const chips = scopeChips(env, cluster);
  if (chips.length === 0) return st;
  return { ...st, queries: st.queries.map(q => injectQuery(q, chips)) };
}
