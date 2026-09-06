import { useMemo } from 'react';
import type { SpanMetricSeries, MetricExemplar, ChartAnnotation, OtlpExemplar } from '@/lib/types';
import type { TSSeries, TSThreshold } from '@/components/viz/TimeSeriesPanel';
import { seriesColor, seriesColorsFor } from '@/lib/chartFmt';
import { formulaSeries } from './formulaSeries';
import { alignFormulaLetters, fmtStep } from './stepAlign';
import {
  type BuilderState, type BuilderQuery, type PivotPair,
  produces, queryDesc, queryUnit,
  seriesGroupLabel, seriesGroupPairs, hasGroupedFilter, effectiveTopN,
} from './model';
import { valueAtCursor } from '@/lib/chart/cursorBus';
import { QueryPanel } from './QueryPanel';
import type { ExploreOverlay } from './useExploreQueries';

// PanelStack (explore-v2 Phase 2) — one stacked, cursor-synced QueryPanel per
// producing query + a dashed formula panel (the RedPanel stacked-synced
// precedent, generalised). Per-panel series are capped to the biggest
// PANEL_SERIES_CAP by area (plan perf guard: 4 panels × ≤10 series).

// PanelState (v0.9.804) — a panel's rendering branch. Before this the only
// signal was `loading: boolean`, derived from `data === undefined`, and TWO
// distinct non-loading situations decoded to "spinner forever":
//
//   idle  — the fan-out is DISABLED (paramless /explore keeps builderFrom at
//           0 so react-query never runs). data stays undefined for the life
//           of the page, so panel A span the moment the workspace rendered
//           and never stopped. Nothing was loading; nothing was going to.
//   error — the query ran and FAILED. useExploreQueries left data undefined
//           on isError, so the panel spun on top of a query that was already
//           dead, and only the FIRST failing letter got a message anywhere.
//
// Making the state explicit is what lets each branch say the true thing.
export type PanelState = 'idle' | 'loading' | 'error' | 'ready';

// PivotAffordance (v0.9.848) — bu serinin GroupTable satırından pivotlanıp
// pivotlanamayacağı ve pivotlanacaksa hangi (anahtar, değer) çiftleriyle.
//
// Kararın burada verilmesinin sebebi: pivot sorgunun splitBy'ını ve gruplu
// filtre kipini bilmek zorunda, ikisi de burada (q) elimizde. GroupTable
// sunum katmanı kalıyor — hangi sorgunun neye izin verdiğini o bilmiyor.
//
// disabled DOLU ise düğme ÇİZİLİR ama pasif ve sebebi title'da: kaybolan bir
// düğme "burada pivot yok" diye okunur, gri bir düğme "şu yüzden olmaz" der.
export interface PivotAffordance {
  pairs: PivotPair[];
  disabled?: string;
}

// PanelSeries — TSSeries + pivot kanalı. TSSeries'e alan EKLEMİYORUZ: o tip
// MQE + TimeSeriesPanel ile paylaşılıyor ve pivot Explore'a özgü.
export interface PanelSeries extends TSSeries {
  pivot?: PivotAffordance;
}

export interface PanelData {
  key: string;             // letter, or 'ƒ' for the formula panel
  letter: string;
  desc: string;
  unit: string;
  isFormula: boolean;
  state: PanelState;
  // Set only when state === 'error' — the failing letter's own message.
  errorMessage?: string;
  // Set only when state === 'ready' && series.length === 0 — why the chart
  // area is blank, phrased for THIS panel (a formula with no overlapping
  // buckets is a different sentence from a query that matched nothing).
  emptyReason?: string;
  series: PanelSeries[];   // capped, labeled, coloured (+ pivot kanalı)
  more: number;            // series dropped by the cap
  // v0.9.458 (dürüstlük A1) — 50k satır tavanı doldu: seriler ALFABETİK
  // kesildi; more'un anlattığı alan-bazlı top-N kırpmasından ayrı yalan.
  rowsCapped?: boolean;
  deploys?: number[];      // ▼ deploy markers (Phase 3.3 — pinned-service queries)
  events?: ChartAnnotation[]; // operator-event annotation lines (A7 — v0.8.284)
  thresholds?: TSThreshold[]; // SLO latency threshold lines (Phase 3.3)
  // v0.9.824 — önceki dönemin serileri, zamanları BUGÜNÜN eksenine
  // kaydırılmış. CorePanelMulti'nin ghostItems kanalına iner: kesikli +
  // soluk (role 'muted') çizilir ve ada " (önceki)" eki O TARAFTA basılır —
  // burada eklemek çift ek yazdırırdı (v0.9.764 dili).
  ghosts?: TSSeries[];
  // Karşılaştırma açık ama hayalet ÇİZİLMEDİYSE sebebi. Sessiz düşürme
  // "önceki dönemde veri yokmuş" diye okunur — yani ölçülmemiş bir şeyi
  // ölçülmüş gibi gösterir.
  compareNote?: string;
}

// GhostBuild — buildGhostSeries'in dönüşü: çizilecek hayaletler + (varsa)
// neden çizilmediğinin cümlesi. İkisi AYNI fonksiyondan çıkar ki "hayalet
// yok" ile "hayalet yok ÇÜNKÜ" hiçbir zaman ayrışmasın.
export interface GhostBuild {
  ghosts: TSSeries[];
  note?: string;
}

// buildGhostSeries — SAF. Önceki dönemin serilerini bugünün eksenine bindirir.
//
// ÜÇ KAPI, bu sırayla:
//
//  1. ÇÖZÜNÜRLÜK. İki tarafın bucket ızgarası farklıysa hayalet ÇİZİLMEZ.
//     Bindirme `time + offset` ile yapılıyor; 15 saniyelik bir ızgarayı 60
//     saniyelik bir ızgaranın üstüne koymak, noktaları hizalıymış gibi
//     GÖSTERİR ama her hayalet nokta dört kat uzun bir pencerede sayılmış
//     bir değerdir. Grafik çizilir, kimse sorgulamaz, ve karşılaştırma
//     dörtte bir ölçekte yalan söyler — formül hizasının (v0.9.809) tam
//     olarak aynı sınıfı. İki taraf da AYNI effStep'i istiyor, ama üç okuma
//     yolu step'i ayrı kelepçeliyor, yani uyuşmazlık CANLI bir olasılık.
//     Step'i BİLİNMEYEN taraf düşürmez: bilmemek uyuşmamak değildir.
//
//  2. ETİKET EŞLEŞMESİ. Yalnız PANELDE GÖRÜNEN serilerin hayaleti çizilir.
//     Önceki dönemin fan-out'u kendi top-N'ini getirir; eşleşmeyenleri de
//     çizmek, güncelde karşılığı olmayan gri çizgiler eklerdi — operatör
//     onları "kaybolmuş seri" diye okur, oysa yalnızca bugünün top-N'ine
//     girememişlerdir.
//
//  3. KAYDIRMA. Nokta zamanları +offset. Saf toplama: compareOffsetNs UTC
//     nanosaniye üretiyor, takvim aritmetiği yok (bkz. model.ts).
export function buildGhostSeries(
  q: BuilderQuery, desc: string, shown: TSSeries[],
  compareSeries: SpanMetricSeries[] | undefined,
  curStepSec: number, cmpStepSec: number, offsetNs: number,
): GhostBuild {
  if (offsetNs <= 0) return { ghosts: [] };
  if (compareSeries === undefined) return { ghosts: [] };   // yükleniyor — not YOK
  // Güncel pencerede hiç seri yoksa hayalet de NOT da yok: panel zaten
  // "Bu pencerede veri yok" diyor, üstüne "önceki dönem eşleşmedi" eklemek
  // operatörü karşılaştırmanın bozuk olduğunu sanmaya götürürdü.
  if (shown.length === 0) return { ghosts: [] };
  if (curStepSec > 0 && cmpStepSec > 0 && curStepSec !== cmpStepSec) {
    return {
      ghosts: [],
      note: `önceki dönem ${fmtStep(cmpStepSec)} ızgarada geldi, güncel `
        + `${fmtStep(curStepSec)} — hayalet çizilmedi (farklı uzunlukta `
        + `pencereleri üst üste bindirmek sessiz bir ölçek hatasıdır). `
        + `Adımı elle sabitle.`,
    };
  }
  if (compareSeries.length === 0) {
    return { ghosts: [], note: 'önceki dönemde veri yok' };
  }
  const shownLabels = new Set(shown.map(s => s.label));
  const shownColor = new Map(shown.map(s => [s.label, s.color] as const)); // v0.10.510 (D7) — hayalet = ana serinin yuvası
  const ghosts: TSSeries[] = [];
  for (const s of compareSeries) {
    const label = seriesGroupLabel(q, s.groupKey, desc);
    if (!shownLabels.has(label)) continue;
    ghosts.push({
      label,
      color: shownColor.get(label) ?? seriesColor(label),
      points: s.points.map(p => ({ time: p.time + offsetNs, value: p.value })),
    });
  }
  if (ghosts.length === 0) {
    return { ghosts: [], note: 'önceki dönemin serileri görünen seri kümesiyle eşleşmedi' };
  }
  return { ghosts };
}

// buildPanels — pure projection BuilderState + fetch results → panel data.
// Exported so Explore memoises ONE array feeding both the stack and the
// GroupTable (same labels = same row keys).
// exemplarMarkersFor — the ◆ markers for ONE series (identified by label).
// Maps each MetricExemplar's groupKey onto a series label via the shared
// seriesGroupLabel (same derivation the chart series use, so a marker lands on
// exactly the right line), anchors the glyph at the series value nearest the
// bucket time, and prefers the error trace over the slow one when a bucket has
// both (one ◆ per bucket; error is the more actionable doorway).
function exemplarMarkersFor(
  q: BuilderQuery, desc: string, label: string,
  points: { time: number; value: number | null }[],
  exemplars: MetricExemplar[],
): NonNullable<TSSeries['exemplars']> {
  const out: NonNullable<TSSeries['exemplars']> = [];
  for (const e of exemplars) {
    if (seriesGroupLabel(q, e.groupKey, desc) !== label) continue;
    const traceId = e.errorTraceId || e.slowTraceId;
    if (!traceId) continue;
    const v = valueAtCursor(points, e.time / 1e9);
    if (!isFinite(v)) continue;
    out.push({ time: e.time, value: v, traceId, kind: e.errorTraceId ? 'error' : 'slow' });
  }
  return out;
}

// otlpMarkersFor — v0.8.332 (pivot Phase 3): ◆ markers from REAL OTLP
// exemplars for a single-series catalogue-metric panel. Anchored at the
// SERIES value nearest the exemplar timestamp — same convention as
// exemplarMarkersFor above: the glyph must sit ON the rendered line (the
// exemplar's own recorded value can be wildly off-scale against an avg/sum-
// aggregated series). kind:'otlp' renders in --purple, distinct from the
// span-derived slow/error tints.
function otlpMarkersFor(
  points: { time: number; value: number | null }[],
  exemplars: OtlpExemplar[],
): NonNullable<TSSeries['exemplars']> {
  const out: NonNullable<TSSeries['exemplars']> = [];
  for (const e of exemplars) {
    if (!e.traceId) continue;
    const v = valueAtCursor(points, e.ts / 1e9);
    if (!isFinite(v)) continue;
    out.push({ time: e.ts, value: v, traceId: e.traceId, kind: 'otlp' });
  }
  return out;
}

// PanelInputs — everything the projection needs beyond the builder state.
// v0.9.804 turned the old positional tail (six defaulted Record params) into
// a named bag: `from` and `errorByLetter` had to join it, and a
// seven-then-eight-positional-argument signature is where a call site
// silently passes the wrong map.
export interface PanelInputs {
  byLetter: Record<string, SpanMetricSeries[] | undefined>;
  // from — the fetch window start in unix ns, exactly as handed to
  // useExploreQueries. 0 means the fan-out is DISABLED (paramless /explore),
  // which is the difference between "waiting" and "not asked yet".
  from?: number;
  // letter → error message for a query whose fetch FAILED.
  errorByLetter?: Record<string, string>;
  exemplarsByLetter?: Record<string, MetricExemplar[]>;
  overlaysByLetter?: Record<string, ExploreOverlay>;
  // letter → pre-trim series count. The server caps a high-cardinality span
  // groupBy to TOP_N_MAX, so ranked.length here is already ≤50; the true
  // "+N more" must come from this total, not the capped slice. Falls back to
  // the received series count when absent (resolver / metric paths).
  totalByLetter?: Record<string, number | undefined>;
  // v0.8.332 (pivot Phase 3) — letter → REAL OTLP exemplars for catalogue-
  // metric queries (useExploreQueries gates the fetch to single-service,
  // no-splitBy queries; [] everywhere else).
  otlpExemplarsByLetter?: Record<string, OtlpExemplar[]>;
  cappedByLetter?: Record<string, boolean>;
  // v0.9.1157 (VM Faz 2) — letter → SUNUCUNUN "bu neden boş" cümlesi.
  // Yalnız VictoriaMetrics + yüzdelik birleşiminde dolu gelir; her yerde
  // undefined ve panel varsayılan cümlesini kullanır.
  noteByLetter?: Record<string, string | undefined>;
  // v0.9.809 — letter → o sorgunun GERÇEK bucket çözünürlüğü (saniye,
  // 0 = bilinmiyor). Formül paneli + hayalet kapısı okur: farklı
  // çözünürlükteki serileri birleştirmek sessiz bir ölçek hatasıdır
  // (stepAlign.ts).
  stepByLetter?: Record<string, number>;
  // v0.9.824 — önceki dönem (hayalet). compareOffsetNs 0 iken üçü de
  // atıldır ve hiçbir panel ghost taşımaz.
  compareByLetter?: Record<string, SpanMetricSeries[] | undefined>;
  compareStepByLetter?: Record<string, number>;
  compareOffsetNs?: number;
}

// IDLE_HINT — the paramless-workspace sentence. Deliberately an instruction,
// not an apology: the operator has landed on a tool with nothing asked yet.
export const IDLE_HINT = 'Sorgunu kur — çalıştıkça burada çizilir';

export function buildPanels(state: BuilderState, inputs: PanelInputs): PanelData[] {
  const {
    byLetter, from = 0, errorByLetter = {}, exemplarsByLetter = {},
    overlaysByLetter = {}, totalByLetter = {}, otlpExemplarsByLetter = {},
    cappedByLetter = {}, noteByLetter = {}, stepByLetter = {},
    compareByLetter = {}, compareStepByLetter = {}, compareOffsetNs = 0,
  } = inputs;
  // active === false → nothing was ever requested. Every producing query is
  // idle, whatever the (necessarily empty) result maps say.
  const active = from > 0;
  const out: PanelData[] = [];
  for (const q of state.queries) {
    if (!produces(q)) continue;
    const data = byLetter[q.letter];
    const unit = queryUnit(q);
    const desc = queryDesc(q);
    const base = { key: q.letter, letter: q.letter, desc, unit, isFormula: false };
    if (!active) {
      out.push({ ...base, state: 'idle', emptyReason: IDLE_HINT, series: [], more: 0 });
      continue;
    }
    // Error BEFORE the undefined check: react-query leaves data undefined on
    // a failed fetch, so an errored letter looks exactly like a loading one.
    const err = errorByLetter[q.letter];
    if (err) {
      out.push({ ...base, state: 'error', errorMessage: err, series: [], more: 0 });
      continue;
    }
    if (data === undefined) {
      out.push({ ...base, state: 'loading', series: [], more: 0 });
      continue;
    }
    const exemplars = exemplarsByLetter[q.letter] ?? [];
    // v0.9.848 — pivot kapısı SORGU BAŞINA: gruplu (OR/iç içe) bir filtre
    // açıkken fetch'e giden şey filterGroup'tur, düz `filters` değil; oraya
    // yazılan bir çip ekranda görünür ama sorguya HİÇ girmezdi.
    const pivotBlocked = hasGroupedFilter(q)
      ? 'Gruplu AND/OR filtresi açıkken pivot düz çipe yazılamaz — önce ⊟ Flatten to chips'
      : undefined;
    const labeled: PanelSeries[] = data.map(s => {
      const label = seriesGroupLabel(q, s.groupKey, desc);
      const points = s.points.map(p => ({ time: p.time, value: p.value }));
      const ex = exemplars.length ? exemplarMarkersFor(q, desc, label, points, exemplars) : [];
      // Çiftler etiketle AYNI türetmeden (seriesGroupPairs ↔ seriesGroupLabel,
      // aynı band soyması) — satırda okunan ile filtreye yazılan hiç ayrışmaz.
      const pairs = seriesGroupPairs(q, s.groupKey);
      return {
        label,
        color: seriesColor(label),
        unit: unit || undefined,
        points,
        exemplars: ex.length ? ex : undefined,
        // splitBy'sız panelde daraltılacak boyut yok — kanal hiç açılmaz.
        pivot: pairs.length ? { pairs, disabled: pivotBlocked } : undefined,
      };
    });
    // v0.10.510 (D7) — panel içi yuva ataması: aynı panelde sekize kadar
    // çakışma yok; ad tercih ettiği yuvayı ister; satır/dilim renkleri
    // (GroupTable/SummaryViz) bu seri rengini okur — tek türetim korunur.
    {
      const cmap = seriesColorsFor(labeled.map(s => s.label));
      for (const s of labeled) s.color = cmap.get(s.label) ?? s.color;
    }
    // v0.8.332 (pivot Phase 3) — single-series panels attach the OTLP ◆
    // wholesale. v0.8.432 (audit Faz B) — grouped items now arrive with a
    // per-item groupKey (the /by-series endpoint's server-side fp→gk join);
    // attribute those to their line via the SAME seriesGroupLabel
    // derivation the chart series use. Legacy keyless items keep the
    // single-unambiguous-series guard.
    const otlp = otlpExemplarsByLetter[q.letter] ?? [];
    if (otlp.length > 0) {
      const keyless = otlp.filter(e => !e.groupKey);
      if (keyless.length > 0 && labeled.length === 1) {
        const ex = otlpMarkersFor(labeled[0].points, keyless);
        if (ex.length > 0) {
          labeled[0] = { ...labeled[0], exemplars: [...(labeled[0].exemplars ?? []), ...ex] };
        }
      }
      const keyed = otlp.filter(e => e.groupKey);
      if (keyed.length > 0) {
        const byLabel = new Map<string, OtlpExemplar[]>();
        for (const e of keyed) {
          const label = seriesGroupLabel(q, e.groupKey!, desc);
          const list = byLabel.get(label) ?? [];
          list.push(e);
          byLabel.set(label, list);
        }
        for (let li = 0; li < labeled.length; li++) {
          const mine = byLabel.get(labeled[li].label);
          if (!mine?.length) continue;
          const ex = otlpMarkersFor(labeled[li].points, mine);
          if (ex.length > 0) {
            labeled[li] = { ...labeled[li], exemplars: [...(labeled[li].exemplars ?? []), ...ex] };
          }
        }
      }
    }
    // Biggest-by-area win the panel slots (MQE precedent).
    const ranked = labeled
      .map(s => ({ s, area: s.points.reduce((a, p) => a + Math.abs(p.value ?? 0), 0) }))
      .sort((a, b) => b.area - a.area);
    const ov = overlaysByLetter[q.letter];
    const cap = effectiveTopN(state.topN);
    // The server may have already trimmed to TOP_N_MAX, so ranked.length
    // undercounts the real series total on a high-card groupBy. Use the
    // reported pre-trim total (defaulting to ranked.length) so "+N more"
    // reflects what actually exists, not just what came over the wire.
    const total = totalByLetter[q.letter] ?? ranked.length;
    const shown = Math.min(cap, ranked.length);
    const shownSeries = ranked.slice(0, cap).map(x => x.s);
    // v0.9.824 — hayalet, GÖRÜNEN seri kümesinden SONRA kurulur: top-N
    // kırpması hayaleti de kapsamalı, yoksa panelde karşılığı olmayan gri
    // çizgiler kalırdı.
    const gb = buildGhostSeries(
      q, desc, shownSeries, compareByLetter[q.letter],
      stepByLetter[q.letter] ?? 0, compareStepByLetter[q.letter] ?? 0, compareOffsetNs);
    out.push({
      ...base, state: 'ready',
      // v0.9.1157 — SUNUCUNUN sebebi varsayılan cümlenin YERİNE geçer.
      // Varsayılan ("aralığı genişlet veya filtreleri azalt") bir tavsiye,
      // ve VictoriaMetrics'te bulunamayan bir `_bucket` serisi için YANLIŞ
      // tavsiye: pencere ne kadar genişlerse genişlesin o seri yok, filtre
      // azaltmak da getirmez. Operatörü ölçmediği bir şeyi ölçmeye
      // göndermek, boş panelden daha pahalı.
      emptyReason: shownSeries.length === 0
        ? (noteByLetter[q.letter]
          ?? 'Bu pencerede veri yok — aralığı genişlet veya filtreleri azalt')
        : undefined,
      series: shownSeries,
      more: Math.max(0, total - shown),
      rowsCapped: cappedByLetter[q.letter] || undefined,
      deploys: ov?.deploys?.length ? ov.deploys : undefined,
      events: ov?.events?.length ? ov.events : undefined,
      thresholds: ov?.thresholds?.length ? ov.thresholds : undefined,
      ghosts: gb.ghosts.length ? gb.ghosts : undefined,
      compareNote: gb.note,
    });
  }
  // Formula panel — dashed, gaps where a referenced bucket is missing.
  const expr = state.formula.trim();
  if (expr) {
    // v0.9.809 — ÇÖZÜNÜRLÜK HİZASI, hesaptan ÖNCE. formulaSeries bucket'ları
    // KESİŞİMLE eşliyor: A 15 saniyelik, B 60 saniyelik bucket'larda gelirse
    // zaman damgaları yine örtüşür ve A/B hesaplanır — ama "15 saniyede
    // sayılan"ı "60 saniyede sayılan"a bölerek. Sonuç boş panel değil,
    // dörtte bir büyüklüğünde MAKUL GÖRÜNEN bir sayıdır; kimse sorgulamaz.
    // Üç okuma yolu step'i ayrı kelepçelediği için bu canlı bir olasılık
    // (gerekçe: stepAlign.ts). Uyuşmuyorsa formül HESAPLANMAZ ve panel
    // nedenini söyler.
    const align = alignFormulaLetters(expr, stepByLetter);
    const pts = align.aligned ? formulaSeries(expr, byLetter) : [];
    const label = `ƒ: ${expr}`;
    // The formula depends on its referenced letters, so it inherits their
    // situation: idle while nothing is asked, error as soon as ANY letter
    // failed (its inputs can't arrive), loading while one is still in
    // flight. Only then is an empty result genuinely "no overlap".
    const failed = Object.keys(errorByLetter);
    const fState: PanelState = !active
      ? 'idle'
      : failed.length > 0
        ? 'error'
        : !align.aligned
          ? 'ready' // hesaplanamaz ama HATA değil — açıklaması emptyReason'da
          : pts.length === 0 && Object.values(byLetter).some(v => v === undefined)
            ? 'loading'
            : 'ready';
    out.push({
      key: 'ƒ', letter: 'ƒ', desc: expr, unit: '', isFormula: true,
      state: fState,
      errorMessage: fState === 'error'
        ? `Girdi sorgusu ${failed.sort().join(', ')} hata verdi`
        : undefined,
      emptyReason: fState === 'idle'
        ? IDLE_HINT
        : !align.aligned && fState === 'ready'
          ? align.note ?? undefined
          : fState === 'ready' && pts.length === 0
            ? 'Formül için ortak zaman aralığında veri yok'
            : undefined,
      series: pts.length ? [{
        label, color: seriesColor(label), dash: [6, 4],
        points: pts.map(p => ({ time: p.time, value: p.value })),
      }] : [],
      more: 0,
      // v0.9.824 — FORMÜLE HAYALET UYGULANMAZ, bilerek. Önceki dönemin
      // A/B'sini çizmek için harflerin hayalet serilerini formülden
      // geçirmek gerekirdi; o da hem KESİŞİM eşlemesini (formulaSeries) hem
      // çözünürlük hizasını (alignFormulaLetters) İKİNCİ bir veri kümesi
      // üzerinde tekrar kurmak demek. İki hesabın bir gün ayrışması, "A/B
      // geçen haftaya göre iyileşmiş" gibi okunan ve DOĞRULANAMAYAN bir
      // sayı üretir. Formül panelinin sözleşmesi net kalıyor: yalnız güncel.
      // Harflerin kendi panelleri hayaleti zaten taşıyor.
    });
  }
  return out;
}

export function PanelStack({ panels, viz, hiddenKeys, focusKey, zoomWindow, onZoom, onZoomReset, onExemplarClick, logScale, onPin, pinnableLetters, xRange }: {
  panels: PanelData[];
  viz: 'line' | 'area' | 'bars' | 'stacked';
  hiddenKeys: Set<string>;          // `${letter}:${label}`
  focusKey: string | null;          // `${letter}:${label}`
  zoomWindow: { from: number; to: number } | null;
  // v0.9.83 — sorgu penceresi (unix sec); panellerin x-ekseni buna sabitlenir.
  xRange?: { from: number; to: number } | null;
  onZoom: (fromSec: number, toSec: number) => void;
  // Grafana-parite M1 — çift-tık: Explore'un lokal zoom geri-yığınını pop
  // eder (bir adım geri; yığın boşken tam görünüme döner).
  onZoomReset?: () => void;
  onExemplarClick?: (traceId: string) => void;   // open an exemplar ◆ trace
  logScale?: boolean;               // v0.8.418 (DE3) — log10 y-axis, all panels
  // v0.8.419 (DE4) — pin a query to a dashboard. pinnableLetters gates the
  // affordance per panel (formula / OR-group queries have no equivalent).
  onPin?: (letter: string) => void;
  pinnableLetters?: Set<string>;
}) {
  // Per-panel projections of the global hidden/focus keys.
  const perPanel = useMemo(() => panels.map(p => {
    const hidden = new Set<string>();
    for (const s of p.series) {
      if (hiddenKeys.has(`${p.letter}:${s.label}`)) hidden.add(s.label);
    }
    let focused: string | null = null;
    if (focusKey && focusKey.startsWith(`${p.letter}:`)) {
      focused = focusKey.slice(p.letter.length + 1);
    }
    return { hidden, focused };
  }), [panels, hiddenKeys, focusKey]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      {panels.map((p, i) => (
        <QueryPanel key={p.key} panel={p} mode={viz}
          hiddenLabels={perPanel[i].hidden}
          focusedLabel={perPanel[i].focused}
          zoomWindow={zoomWindow}
          xRange={xRange}
          onZoom={onZoom}
          onZoomReset={onZoomReset}
          onExemplarClick={onExemplarClick}
          logScale={logScale}
          onPin={onPin && !p.isFormula && pinnableLetters?.has(p.letter)
            ? () => onPin(p.letter) : undefined} />
      ))}
    </div>
  );
}
