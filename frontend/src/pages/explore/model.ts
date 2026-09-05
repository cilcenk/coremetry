// pages/explore/model.ts — the Explore v2 multi-query builder state model.
//
// Phase-2 (explore-v2): BuilderState is what rides the URL as ?q= (see
// urlCodec.ts) and what the panel stack + group table render from. It is
// the MQE A–D + formula model (components/viz/MetricQueryEditor.tsx)
// ported onto span signals + catalogue metrics, per the 2026-06-10 plan.
//
// Pure types + helpers only — no React, no fetch — so urlCodec and
// formulaSeries stay unit-testable without the chart bundle.

import type { FilterExpr, FilterGroup, SpanAgg } from '@/lib/types';
import { isAdditiveUnit } from '@/lib/chart/legendStats';
import { isFlatAndGroup } from '@/lib/urlState';
import { metricQuery, type MetricQuery, type MetricAgg } from '@/lib/metricQuery';
import { TIER_DIM_KEYS, EXEMPLAR_AGGS } from '@/lib/resolverEligibility';
// Saf leaf (haritanın kendisi, @grafana bağımlılığı YOK) — model.ts'in
// "chart bundle olmadan test edilebilir" sözleşmesi bozulmuyor.
import { otlpUnitToGrafana } from '@/lib/chart/metricUnit';

// Per-query source: 'span' aggregates the spans table via api.spanMetric
// (rate / error_rate / percentiles over duration_ms or any numeric attr);
// 'metric' reads a catalogue metric via api.metricQuery.
export type QuerySource = 'span' | 'metric';

// Viz set — line/area/bars render on TimeSeriesPanel; stat/toplist render the
// per-series summary (SummaryViz, Phase 4); table is the GroupTable alone;
// heatmap keeps the LatencyHeatmap path (driven by query A).
export type ExploreViz = 'line' | 'area' | 'bars' | 'stacked' | 'stat' | 'toplist' | 'pie' | 'table' | 'heatmap';
// v0.8.427 (DE5) — 'stacked' rides TimeSeriesPanel's existing TSMode
// ('stacked' cum-sum bands, v0.8 Phase 1A); 'pie' is a SummaryViz
// projection (share of current value), no new chart deps.
export const EXPLORE_VIZ: ExploreViz[] = ['line', 'area', 'bars', 'stacked', 'stat', 'toplist', 'pie', 'table', 'heatmap'];

// Aggregations differ per source (plan ground-truth #10): the metric query
// API supports avg|sum|min|max|last|rate|increase|p50|p95|p99; span signals
// add count / errors / error_rate and the wider percentile set.
//
// v0.9.1201 — rate/increase eklendi. İki backend'de de yıllardır vardı
// (CH v0.9.106 metricrate, VM v0.9.1154 MetricsQL çevirisi) ama FE hiç
// sunmuyordu: kümülatif bir counter'ı sum ile çizmek "tırmanan toplam"
// gösterir, operatörün istediği hız eğrisini değil.
export type MetricCatalogAgg =
  'avg' | 'sum' | 'min' | 'max' | 'last' | 'rate' | 'increase' | 'p50' | 'p95' | 'p99';
export const METRIC_CATALOG_AGGS: MetricCatalogAgg[] =
  ['avg', 'sum', 'min', 'max', 'last', 'rate', 'increase', 'p50', 'p95', 'p99'];

export interface BuilderQuery {
  letter: string;          // 'A'..'D' — stable id the formula references
  source: QuerySource;
  enabled: boolean;
  // span source: the measured numeric field ('duration_ms' default; '' for
  // count-shaped aggs). metric source: the catalogue metric name.
  metric: string;
  // metric source: MetricInfo.unit — HAM katalog birimi (OTLP/UCUM), URL'e
  // de bu yazılır; görüntü kimliğine çeviri queryUnit()'in işi.
  // span source: agg'den türetilir (spanAggUnit), alan boş kalır.
  unit: string;
  agg: string;             // SpanAgg (span source) | MetricCatalogAgg (metric source)
  scope: string;           // service.name pin ('' = all) — synthesized into a filter at fetch
  splitBy: string[];       // group-by keys → series fan-out
  filters: FilterExpr[];   // AND-ed attribute filters
  // filterGroup — optional grouped AND/OR builder (v0.8.x gap-2, extended into
  // Explore). When set to a genuine OR / nested group it SUPERSEDES `filters`
  // at fetch (effectiveFilters stays the flat back-compat path). A flat-AND or
  // absent group is byte-identical to the legacy chip row — encodeFilterGroup
  // returns '' for it so the URL + fetch carry the flat `filters` only. A
  // non-flat group also disqualifies the spanmetrics resolver (exemplar) path:
  // OR / nested can't ride the rollup tiers.
  filterGroup?: FilterGroup;
  dsl: string;             // advanced span DSL (legacy decode surface; AND-joined with filters)
}

// ExploreCompare — önceki döneme karşılaştırma kipi (v0.9.824).
// undefined = KAPALI ve bu VARSAYILAN: açık bir kip, üreten her sorgu için
// İKİNCİ bir fan-out koşturur, yani sayfanın sorgu maliyetini ikiye katlar.
// ServiceCharts'ın CompareMode'uyla aynı üç seçenek (v0.8 dili) ama 'off'
// yok — yokluk zaten kapalı demek, ve iki temsili olan bir bayrak URL'de
// er geç ayrışır.
export type ExploreCompare = '24h' | '7d' | 'prev';
export const EXPLORE_COMPARE: ExploreCompare[] = ['24h', '7d', 'prev'];

export interface BuilderState {
  queries: BuilderQuery[];
  formula: string;         // '' = none. Expression over letters, e.g. "A / B * 100"
  viz: ExploreViz;
  step: number;            // seconds; 0 = auto. GLOBAL so formula buckets stay aligned.
  topN?: number;           // top-N series per panel by area (Uptrace top10). 0/undef = PANEL_SERIES_CAP.
  logY?: boolean;          // v0.8.418 (DE3) — log10 y-axis on the line/area/bars panels.
  // v0.9.824 — önceki döneme karşılaştırma. Yokluk = kapalı.
  cmp?: ExploreCompare;
}

// compareOffsetNs — pencerenin NE KADAR geriye kaydırılacağı, NANOSANİYE.
//
// SAF ve UTC. Takvim aritmetiği YOK, yerel saat YOK: '24h' tam 24×3600×1e9
// nanosaniyedir, DST geçişinde 23 ya da 25 saate DÖNMEZ. Bu bilinçli —
// hayalet serinin tek işi bugünün eksenine BİREBİR bindirilmek, ve bindirme
// `time + offset` ile yapılıyor. Takvim-duyarlı bir offset kullansaydık
// DST sınırını geçen bir pencerede kovalar bir saat kayar, hayalet çizgi
// güncel çizginin yanına DEĞİL arasına düşerdi; operatör bunu "veri kaymış"
// diye okur. Aynı gerekçe ServiceCharts'ın compareOffsetNs'inde de geçerli
// (v0.8, aynı hesap).
//
// 'prev' = pencerenin kendi genişliği (bitişik önceki pencere). Bozuk/ters
// bir aralıkta 0 döner — 0 hayaleti KAPATIR, uydurulmuş bir kaydırma değil.
export function compareOffsetNs(
  cmp: ExploreCompare | undefined, from: number, to: number,
): number {
  switch (cmp) {
    case '24h': return 24 * 3600 * 1e9;
    case '7d':  return 7 * 24 * 3600 * 1e9;
    case 'prev': return to > from ? to - from : 0;
    default:    return 0;
  }
}

// compareLabel — kipin insan adı (şerit başlığı + Δ sütunu title'ı).
export function compareLabel(cmp: ExploreCompare | undefined): string {
  switch (cmp) {
    case '24h': return '24 saat önce';
    case '7d':  return '7 gün önce';
    case 'prev': return 'önceki pencere';
    default:    return '';
  }
}

export const MAX_QUERIES = 4;
export const QUERY_LETTERS = ['A', 'B', 'C', 'D'];

// v0.9.1162 (operatör talimatı: "kırpmasın hepsini göstersin") —
// VARSAYILAN artık sessizce 10'a kırpmıyor: Top N seçilmediyse tavana
// (TOP_N_MAX) dek TÜM seriler çizilir; "+N daha" yalnız 50'yi aşan
// gerçek patlamalarda görünür. Sert tavan uPlot bütçesini korumaya
// devam ediyor; daraltmak isteyen operatör araç çubuğundaki Top N
// seçicisini kullanır. PANEL_SERIES_CAP artık yalnız o seçicinin
// "10" seçeneğinin adı — varsayılan DEĞİL.
export const PANEL_SERIES_CAP = 10;
export const TOP_N_MAX = 50;
export const TOP_N_OPTIONS = [5, 10, 20, 50];

// effectiveTopN — the operator's chosen cap, clamped to [1, TOP_N_MAX];
// unset = show everything up to the hard ceiling (v0.9.1162).
export function effectiveTopN(topN?: number): number {
  if (!topN || topN <= 0) return TOP_N_MAX;
  return Math.min(topN, TOP_N_MAX);
}

export function blankQuery(letter: string, source: QuerySource = 'span'): BuilderQuery {
  return {
    letter, source, enabled: true,
    metric: source === 'span' ? 'duration_ms' : '',
    unit: '', agg: source === 'span' ? 'count' : 'avg',
    scope: '', splitBy: [], filters: [], dsl: '',
  };
}

export function defaultBuilderState(): BuilderState {
  return { queries: [blankQuery('A')], formula: '', viz: 'line', step: 0 };
}

export function nextLetter(queries: BuilderQuery[]): string | null {
  const used = new Set(queries.map(q => q.letter));
  for (const l of QUERY_LETTERS) if (!used.has(l)) return l;
  return null;
}

// duplicateQueryAt (v0.9.847) — i. sorgunun DERİN kopyasını hemen ALTINA
// ekler, sıradaki boş harfle.
//
// NEDEN: "+ Sorgu" bugüne dek sabit bir blankQuery basıyordu, oysa ikinci
// sorgunun gerçek hayattaki hâli neredeyse her zaman "A'nın aynısı ama p50"
// ya da "A'nın aynısı ama status=error". Operatör scope + 3 filtre + split'i
// elle bir kez daha kuruyordu; çoğaltma o kopyalama işini kaldırıyor.
//
// KOPYA DERİN olmak ZORUNDA: filters / splitBy / filterGroup referans
// tiplerdir, sığ bir `{...src}` iki sorguyu AYNI dizilere bağlardı ve
// kopyaya bir çip eklemek sessizce orijinali de değiştirirdi (ve tersi).
// structuredClone, iç içe FilterGroup ağacını da elle gezmeden çözer.
//
// Harf yalnız bir KİMLİKTİR (formülün başvurduğu şey), sıra numarası değil:
// [A,B]'de A çoğaltılırsa dizi [A,C,B] olur. Kopyanın kaynağının HEMEN
// altında durması, listenin sonuna atılmasından daha okunur — düzenlenecek
// satır göz hizasında kalır.
//
// Dolu (MAX_QUERIES) ya da geçersiz indekste GİRDİ AYNEN döner (yeni dizi
// bile ayrılmaz) — çağıran setBuilder içinde bail-out eder, render olmaz.
export function duplicateQueryAt(queries: BuilderQuery[], i: number): BuilderQuery[] {
  const src = queries[i];
  if (!src) return queries;
  const letter = nextLetter(queries);
  if (!letter) return queries;
  const out = queries.slice();
  out.splice(i + 1, 0, { ...structuredClone(src), letter });
  return out;
}

// spanNeedsField — latency-style span aggs measure a field; count-style
// don't (mirrors presets.needsField, kept here so model.ts stays leaf).
export function spanNeedsField(agg: string): boolean {
  return !['count', 'rate', 'per_min', 'errors', 'error_rate', 'apdex'].includes(agg);
}

// spanAggUnit — the y-unit a span aggregation produces (matches
// presets.AGG_OPTIONS). Metric-source queries carry MetricInfo.unit instead.
export function spanAggUnit(agg: string): string {
  if (agg === 'rate') return '/s';
  if (agg === 'per_min') return '/min';
  if (agg === 'error_rate') return '%';
  if (agg === 'apdex') return '';  // 0–1 score, unitless
  if (['avg', 'p50', 'p90', 'p95', 'p99', 'p999', 'min', 'max', 'sum', 'band'].includes(agg)) return 'ms';
  return '';
}

// produces — does this query yield series? Span queries always can (count of
// all spans is a valid signal); metric queries need a picked metric.
export function produces(q: BuilderQuery): boolean {
  return q.enabled && (q.source === 'span' || !!q.metric);
}

// effectiveFilters — the filter set actually sent to the backend: the scope
// pin synthesized as a service.name chip + the explicit chips. The scope chip
// is byte-identical to what the legacy single-query workspace sent, so cache
// keys and results line up with pre-v2 behaviour.
export function effectiveFilters(q: BuilderQuery): FilterExpr[] {
  const scoped: FilterExpr[] = q.scope
    ? [{ k: 'service.name', op: '=', v: [q.scope] }]
    : [];
  return [...scoped, ...q.filters];
}

// hasGroupedFilter — true only when the query carries a GENUINE OR / nested
// group (a flat-AND or absent group is indistinguishable from the legacy chip
// row). The one place that decides whether the grouped builder is "active":
//   - fetch: send filterGroup (effectiveFilterGroup) instead of flat filters
//   - resolver: a grouped query can't ride the rollup tiers → no exemplars
//   - signature: the group folds into the cache key
// so they can never drift.
export function hasGroupedFilter(q: BuilderQuery): boolean {
  return !isFlatAndGroup(q.filterGroup);
}

// effectiveFilterGroup — the grouped predicate actually sent to the backend
// when hasGroupedFilter(q): the scope pin synthesized as a top-level
// service.name AND leaf (matching effectiveFilters) plus the query's own
// group. Returns null when the query has no genuine OR / nested group so the
// caller falls back to the flat effectiveFilters path. The scope leaf is added
// at the TOP-LEVEL join (always AND-combined with the rest), so an inner OR
// can't bind across it — identical scoping semantics to the flat path.
export function effectiveFilterGroup(q: BuilderQuery): FilterGroup | null {
  if (!hasGroupedFilter(q) || !q.filterGroup) return null;
  const g = q.filterGroup;
  if (!q.scope) return g;
  const scopeLeaf: FilterExpr = { k: 'service.name', op: '=', v: [q.scope] };
  return { ...g, filters: [scopeLeaf, ...(g.filters ?? [])] };
}

// querySignature — stable serialization of every fetch-relevant input, used
// as the react-query cache key component (lib/queries/keys.ts explore.query).
// Letter intentionally EXCLUDED: two letters with identical inputs share one
// fetch.
export function querySignature(q: BuilderQuery, step: number): string {
  return JSON.stringify({
    s: q.source, m: q.metric, a: q.agg, sc: q.scope,
    by: q.splitBy, f: q.filters, d: q.dsl, st: step,
    // Only a GENUINE OR / nested group enters the key — a flat-AND / absent
    // group is byte-identical to the flat `f` path, so two queries differing
    // only by an inert group still share one fetch (and the signature stays
    // byte-identical to a pre-group query, so warm caches survive).
    ...(hasGroupedFilter(q) ? { fg: q.filterGroup } : {}),
  });
}

// queryUnit — resolved DISPLAY unit for a query's series.
//
// q.unit metrik kaynağında HAM KATALOG birimidir (OTLP/UCUM: 's', 'ms',
// 'By', '1', '{request}'). Panel/legend/eksen ise @grafana/data'nın birim
// KİMLİĞİNİ bekler. v0.9.801'e kadar ham değer doğrudan geçiyordu:
// 's'/'ms' iki alfabede de aynı yazıldığı için o dal kazara doğruydu,
// 'By' Grafana'ya bilinmeyen bir kimlik olarak iniyor, '1' ise ham sayının
// yanına "1" yazdırıyordu. Çeviri TEK yerde, burada — çağıranların
// hiçbiri q.unit'i kendi eliyle eşlemez.
//
// Bilinmeyen birim '' döner (birimsiz): uydurulmuş bir birim, birimsiz
// olandan kötüdür.
export function queryUnit(q: BuilderQuery): string {
  if (q.source === 'span') return spanAggUnit(q.agg);
  return metricAggUnit(q.agg, otlpUnitToGrafana(q.unit));
}

// metricAggUnit — v0.9.1201. rate değeri SANİYELİK'tir; taban birimi aynen
// göstermek (bytes çizip B/s kastetmek) v0.9.774 dürüstlük deseninin tam
// ihlali olurdu. Tablo bilinçli dar: bytes → Bps, birimsiz → /s; süre/yüzde
// counter'ının hızı oran-benzeridir ve birim UYDURULMAZ ('' döner).
// increase pencere-toplamıdır — taban birim doğru kalır.
export function metricAggUnit(agg: string, baseUnit: string | undefined): string {
  if (agg !== 'rate') return baseUnit ?? '';
  if (baseUnit === 'bytes') return 'Bps';
  if (baseUnit === undefined || baseUnit === '') return '/s';
  return '';
}

// queryDesc — one-line human summary ("p95 of duration_ms by service.name").
// Drives the panel header + the recent-queries history label.
export function queryDesc(q: BuilderQuery): string {
  const what = q.source === 'span'
    ? (spanNeedsField(q.agg) ? `${q.agg} of ${q.metric || 'duration_ms'}` : q.agg)
    : `${q.agg}(${q.metric || '?'})`;
  const scope = q.scope ? ` · ${q.scope}` : '';
  const split = q.splitBy.length ? ` by ${q.splitBy.join(', ')}` : '';
  return `${what}${scope}${split}`;
}

// builderDesc — history-ring label for a whole builder state. Stable for the
// same state so re-runs bump in the ring instead of duplicating.
export function builderDesc(s: BuilderState): string {
  const parts = s.queries.filter(produces).map(q => `${q.letter}: ${queryDesc(q)}`);
  if (s.formula.trim()) parts.push(`ƒ=${s.formula.trim()}`);
  return `${parts.join(' · ') || 'empty'} · ${s.viz}`;
}

// seriesGroupLabel — the ONE label derivation for a (query, groupKey) series.
// PanelStack (chart series), the GroupTable rows AND the exemplar mapping all
// go through this so an exemplar's groupKey lands on exactly the series label
// the panel rendered (a one-character drift = invisible glyphs).
export function seriesGroupLabel(q: BuilderQuery, groupKey: string[], desc: string): string {
  // band (v0.8.411): the resolver folds the quantile label into the
  // LAST groupKey element (one dimension more than splitBy). Peel it
  // off so split labels stay aligned and the quantile names the line
  // ("p95" alone, or "service=checkout - p95" when grouped).
  let keys = groupKey;
  let bandLbl = '';
  if (q.agg === 'band' && groupKey.length === q.splitBy.length + 1) {
    bandLbl = groupKey[groupKey.length - 1];
    keys = groupKey.slice(0, -1);
  }
  const grp = keys
    .map((val, gi) => `${(q.splitBy[gi] ?? 'g').replace(/^.*\./, '')}=${val}`)
    .join(', ');
  if (bandLbl) return grp ? grp + " \u00b7 " + bandLbl : bandLbl;
  return grp || desc;
}

// PivotPair (v0.9.848) — bir serinin (splitBy anahtarı, değer) çifti.
// GroupTable satırından filtre çipine giden pivotun para birimi.
export interface PivotPair { k: string; v: string; }

// seriesGroupPairs — seriesGroupLabel'in ÇİFT hâli: aynı groupKey'i etikete
// değil (anahtar, değer) listesine çevirir. İkisi AYNI dosyada ve AYNI band
// soyma kuralıyla, çünkü ayrışırlarsa satırda okunan etiket ile o satırdan
// üretilen filtre farklı boyutlara işaret ederdi — ekranda hiçbir belirtisi
// olmayan bir hata sınıfı.
//
// splitBy'da karşılığı OLMAYAN bir groupKey elemanı ATLANIR: adlandıramadığımız
// bir boyutu 'g' gibi uydurma bir anahtarla filtreye yazmak, backend'de hiçbir
// şeyle eşleşmeyen bir çip üretirdi (boş panel, sebep yok).
export function seriesGroupPairs(q: BuilderQuery, groupKey: string[]): PivotPair[] {
  // band (v0.8.411): resolver quantile etiketini SON groupKey elemanına
  // katlar — seriesGroupLabel'deki soymanın birebir aynısı.
  const keys = (q.agg === 'band' && groupKey.length === q.splitBy.length + 1)
    ? groupKey.slice(0, -1)
    : groupKey;
  const out: PivotPair[] = [];
  for (let i = 0; i < keys.length; i++) {
    const k = q.splitBy[i];
    if (k) out.push({ k, v: keys[i] });
  }
  return out;
}

// ── Phase-3 — per-query context pins (SLO thresholds + deploy markers) ──────

// pinnedService — the single service this query is unambiguously scoped to:
// the scope slot, else exactly one `service.name =` chip. '' = not pinned
// (deploys/SLO overlays need a service; an OR/IN/multi-service query has no
// single deploy stream to draw).
export function pinnedService(q: BuilderQuery): string {
  if (q.scope) return q.scope;
  const eq = q.filters.filter(f =>
    (f.k === 'service.name' || f.k === 'resource.service.name') && f.op === '=' && f.v.length === 1);
  return eq.length === 1 ? eq[0].v[0] : '';
}

// pinnedOperation — exactly one `name =` chip, for operation-scoped SLO
// matching (an SLO with .operation only applies when the chart is on it).
export function pinnedOperation(q: BuilderQuery): string {
  const eq = q.filters.filter(f => f.k === 'name' && f.op === '=' && f.v.length === 1);
  return eq.length === 1 ? eq[0].v[0] : '';
}

// ── Phase-3 — exemplar eligibility ──────────────────────────────────────────
// Exemplar trace_ids only exist on the spanmetrics rollup tiers (argMax
// states; chstore/metricresolve.go). A builder span query can ride that path
// iff the resolver's planner would accept it: equality-only filters and
// splitBy keys on the five rollup dimensions, a rollup-served agg, and the
// measured field being duration (the rollups carry no other numeric attr).
// Anything else returns null — the panel simply renders without ◆ glyphs.

// GRAN-D (v0.8.249) — TIER_DIM_KEYS + EXEMPLAR_AGGS moved to
// lib/resolverEligibility.ts (imported above) so non-Explore surfaces
// (ServiceCharts' RED family) share the eligibility contract without
// importing this page's builder model. Re-exported verbatim for existing
// consumers; exemplarDescriptor below is behaviour-identical.
export { TIER_DIM_KEYS, EXEMPLAR_AGGS };

export function exemplarDescriptor(q: BuilderQuery): MetricQuery | null {
  if (q.source !== 'span') return null;
  if (q.dsl.trim()) return null;
  // A genuine OR / nested filter group can't be expressed as the resolver's
  // equality-only filter map — it must fall to the raw spanMetric path (which
  // honours boolean structure) and renders without ◆ exemplar glyphs.
  if (hasGroupedFilter(q)) return null;
  if (!EXEMPLAR_AGGS.has(q.agg)) return null;
  if (spanNeedsField(q.agg) && q.metric && q.metric !== 'duration_ms') return null;
  const filters: Record<string, string> = {};
  for (const f of effectiveFilters(q)) {
    if (f.op !== '=' || f.v.length !== 1 || !TIER_DIM_KEYS.has(f.k)) return null;
    if (f.k in filters && filters[f.k] !== f.v[0]) return null; // contradictory dupes would silently collapse
    filters[f.k] = f.v[0];
  }
  for (const k of q.splitBy) if (!TIER_DIM_KEYS.has(k)) return null;
  return metricQuery({
    source: 'spanmetrics',
    metric: spanNeedsField(q.agg) ? 'duration_milliseconds_bucket' : 'calls_total',
    agg: q.agg as MetricAgg,
    filters,
    groupBy: q.splitBy.length ? q.splitBy : undefined,
  });
}

// SpanAgg type re-export convenience for consumers narrowing span aggs.
export type { SpanAgg };

// vizDisabledFor — v0.10.393 (dış skill denetimi A10): pay (pie) ve yığın
// (stacked) yalnız TOPLANABİLİR birimde anlamlı. p99'un "payı %31" ya da
// gecikmelerin yığını sayı üretir ama anlam üretmez; isAdditiveUnit
// (lejant Σ'sının kapısı) burada da karar verir. Dönüş: viz → sebep.
export function vizDisabledFor(units: readonly string[]): Partial<Record<ExploreViz, string>> {
  const bad = units.filter(u => !isAdditiveUnit(u));
  if (bad.length === 0) return {};
  const why = `Birim toplanamaz (${[...new Set(bad)].join(', ')}): pay/yığın anlamsız — oran, sayaç ya da bayt seçin`;
  return { pie: why, stacked: why };
}
