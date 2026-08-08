// pages/explore/heatmapSig.ts — ısı haritasını GERÇEKTEN belirleyen imza
// (v0.9.810).
//
// Explore'un heatmap effect'i `[builderActive, debounced, exploreRange]`
// bağımlılığıyla koşuyordu. `debounced` TÜM builder state'i: dört sorgunun
// filtreleri, agg'leri, splitBy'ları, formül metni, topN, logY, step…
// Oysa /api/spans/heatmap YALNIZ ÜRETEN İLK SORGUNUN (A) filtrelerini ve
// DSL'ini alıyor, artı pencereyi. Yani B'nin agg'ini değiştirmek, C'ye bir
// çip eklemek ya da formülü yazmak — heatmap'in girdisine hiç dokunmayan
// düzenlemeler — sayfanın en pahalı taramasını (ham spans, log-ölçek
// bucket ızgarası) yeniden tetikliyordu. Debounce bunu geciktiriyordu,
// engellemiyordu.
//
// İmza SAF ve DAR: yalnız isteğe giden alanlar. Bir alan isteğe girecekse
// imzaya da girmek ZORUNDA — testteki "istek alanları = imza alanları"
// kapısı bunu çiviler.
//
// viz de imzada, çünkü effect ona GÖRE kapanıyor: heatmap'ten çizgiye
// geçiş effect'i yeniden çalıştırıp bekleyen kutu-seçimini (boxSel)
// temizlemeli. İmzada olmasaydı seçim bir sonraki heatmap'e sarkardı.

import type { BuilderState } from './model';
import { produces, effectiveFilters } from './model';

// heatmapQuerySig — effect'in bağımlılığı olacak, insan-okur imza dizesi.
// Üreten sorgu yoksa 'none' döner: o durumda istek de atılmıyor.
export function heatmapQuerySig(
  state: BuilderState,
  fromNs: number,
  toNs: number,
  buckets: number,
): string {
  if (state.viz !== 'heatmap') return `viz=${state.viz}`;
  const a = state.queries.find(produces);
  if (!a) return 'viz=heatmap|none';
  // effectiveFilters ZATEN kapsam pinini katlıyor (isteğe giden değerin
  // aynısı) — burada ikinci bir türetme yazmıyoruz.
  const filters = effectiveFilters(a);
  return [
    'viz=heatmap',
    `f=${JSON.stringify(filters)}`,
    `dsl=${a.dsl.trim()}`,
    `w=${fromNs}-${toNs}`,
    `b=${buckets}`,
  ].join('|');
}
