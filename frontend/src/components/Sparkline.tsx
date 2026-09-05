import { useId, useRef, useState } from 'react';
import { SPARK_DEFAULT_WIDTH, barGeometry, barIndexAt, classifyThreshold, downsampleBuckets, maxBarsForWidth, maxLinePointsForWidth, sparkRenderMode } from '@/lib/sparkline';
import { downsampleXY } from '@/lib/perf/lttb';

// Tiny inline SVG sparkline — no chart library. Auto-scales to its own
// y-range so a service with 5 spans/min looks just as readable as one
// with 50k. Deliberately SVG (not uPlot): table pages render hundreds
// of instances per screen, so per-row weight matters more than chart
// features. No animation anywhere — prefers-reduced-motion is
// trivially satisfied.
//
// Single empty bucket → "—" placeholder so a brand-new service doesn't
// render an ambiguous flat line.
//
// v0.5.485 — SRE toolkit additions:
//   • Hover tooltip — shows the exact bucket value at the cursor.
//   • threshold + comparator — line/area shift to red when any value
//     in the window crosses it. At-a-glance SLO health.
//   • onClick — click handler for "drill to /metrics" affordances.
//   • showDelta — inline `+35%` chip vs first non-zero bucket.
//
// Granular-sparklines sweep (M4, 2026-07-24 — operator: "sadece
// çizgisel gösteriyor, daha granüle olsun"): the backend grid widened
// to ≤120 buckets and the component grew three render modes:
//   • mode='area'  (default) — the classic line, now with a gradient
//     area fill (series colour → transparent) and an endpoint dot
//     emphasising the latest bucket. Trend cells (rate / latency).
//   • mode='bars'  — per-bucket mini-bars; with `threshold` set,
//     breach buckets colour var(--err) (≥ threshold), approaching
//     buckets var(--warn) (≥ 70%), the rest stay faint grey. The
//     classification is ≥-based regardless of thresholdComparator
//     (which keeps its meaning for area mode's whole-window shift).
//     Threshold cells (error rate).
//   • mode='count' — single-colour bar distribution for counter cells
//     (e.g. fires per hour); zero buckets stay empty on purpose.
// Geometry + classification are pure helpers in lib/sparkline.ts
// (vitest-pinned). All pre-sweep props keep their exact meaning.
//
// v0.9.207 review-fix — bar modes are capped at a width-derived bar
// budget (maxBarsForWidth, default 80px → 40): raw 5-min feeds merge
// adjacent buckets ('bars' → max, 'count' → sum) before geometry, so
// wide presets (7d = 2016 buckets) can't balloon the DOM or smear
// sub-pixel bars over the threshold colouring. See lib/sparkline.ts
// downsampleBuckets for the reducer semantics.

interface Props {
  values: (number | null)[]; // y-values; index = time bucket; null = veri yok (v0.10.385)
  width?: number;        // default 80
  height?: number;       // default 22
  color?: string;        // default --accent2
  title?: string;        // SVG <title>; used as base tooltip text
  className?: string;
  // Optional unit for the hover tooltip's value formatting ("ms", "%", etc).
  unit?: string;
  // v0.5.485 — threshold colouring. Area mode: when any value
  // satisfies the comparator vs threshold, the line and area shift to
  // red so the sparkline reads as "unhealthy" without the operator
  // needing to mouse over. Bars mode: colours the individual breach
  // buckets instead (see header comment).
  threshold?: number;
  thresholdComparator?: '>' | '>=' | '<' | '<=';
  // v0.5.485 — click affordance. When set, cursor:pointer and the
  // sparkline becomes a button. Use for "drill into /metrics for
  // the full view".
  onClick?: () => void;
  // v0.9.65 — paylaşılan y-ölçeği (compare gölge overlay'i): verilirse
  // seri kendi min/max'ı yerine [0, domainMax]'a normalize edilir; iki
  // Sparkline aynı domainMax ile üst üste bindiğinde seviye
  // karşılaştırması gerçek olur (kendi-ölçeğinde iki seri aynı
  // genlikte görünüp "değişmemiş" okutuyordu — review bulgusu).
  domainMax?: number;
  // v0.5.485 — inline delta chip (`+35%`) next to the SVG. Compares
  // last non-zero bucket to first non-zero bucket. Caller-driven so
  // dense tables can opt out per-column.
  showDelta?: boolean;
  // M4 sweep — render mode; see header comment. Default 'area'.
  mode?: 'area' | 'bars' | 'count';
}

export function Sparkline({
  values, width = SPARK_DEFAULT_WIDTH, height = 22, color, title, className,
  unit, threshold, thresholdComparator = '>', onClick, showDelta, domainMax,
  mode = 'area',
}: Props) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  // Gradient ids must be document-unique; useId's colons are stripped
  // because url(#…) references choke on them.
  const gid = 'slg-' + useId().replace(/[^a-zA-Z0-9_-]/g, '');

  const baseStroke = color || 'var(--accent2)';
  const barMode = mode === 'bars' || mode === 'count';
  // v0.9.498 — "veri yok" ile "veri var, hepsi sıfır" AYRI şeyler.
  // Karar saf tarafta (lib/sparkline.ts sparkRenderMode), tablo-güdümlü
  // testi orada; burada yalnız o kararın çizimi var.
  const renderMode = sparkRenderMode(values);
  if (renderMode === 'nodata') {
    return (
      <span
        title={title || 'no data'}
        style={{ display: 'inline-block', width, height,
                 lineHeight: `${height}px`, textAlign: 'center',
                 color: 'var(--text3)', fontSize: 11 }}
        className={className}
      >—</span>
    );
  }
  if (renderMode === 'zero') {
    // Gerçek ölçüm, tamamen sıfır → tabanda düz çizgi. Sağlıklı bir
    // "hiç hata yok" penceresi böyle okunur; "—" ise ölçüm yokluğudur.
    const y = height - 1.5;
    return (
      <svg width={width} height={height} className={className}
           role="img" aria-label={title || 'all zero'} style={{ display: 'inline-block' }}>
        <title>{title || 'sıfır — ölçüm var, değer yok'}</title>
        <line x1={1} y1={y} x2={width - 1} y2={y}
              stroke={baseStroke} strokeWidth={1.5} strokeLinecap="round" opacity={0.55} />
      </svg>
    );
  }
  // v0.9.207 review-fix — bar modes cap the rendered bar count at a
  // width-derived budget (default 80px → 40 rects). Raw 5-min feeds
  // (Services error-rate: 7d = 2016 buckets) previously emitted one
  // <rect> per non-zero bucket — unbounded DOM across a 50-row table —
  // while sub-1px slots overlapped ~25:1 into a smear that erased the
  // per-bucket threshold colouring. Adjacent buckets merge via 'max'
  // for mode='bars' (a breached 5-min bucket must stay red after the
  // merge) and 'sum' for mode='count' (fires add across the wider
  // window). Area mode is untouched (drawn === values).
  const drawn = barMode
    ? downsampleBuckets(values, maxBarsForWidth(width), mode === 'count' ? 'sum' : 'max')
    : values;
  // v0.9.65 — domainMax verildiyse [0, domainMax] sabit ölçeği
  // (compare overlay'inin paylaşılan ekseni); yoksa eski kendi-aralığı
  // davranışı birebir korunur. Bar modları HER ZAMAN sıfır-tabanlı
  // (min-normalize bar büyüklük konusunda yalan söyler). Ölçek drawn
  // üzerinden — count-modunda merge toplamları raw max'ı aşabilir,
  // threshold çizgisi çizilen barlarla aynı eksende kalmalı.
  // v0.10.385 (dış skill denetimi A7) — null kova ölçeğe ve çizgiye
  // girmez; çizgi delikte KESİLİR ("trafik yok" ile "%0" aynı görünmesin).
  const finiteDrawn = drawn.filter((v): v is number => v != null && Number.isFinite(v));
  const max = domainMax !== undefined ? domainMax : (finiteDrawn.length ? Math.max(...finiteDrawn) : 0);
  const min = domainMax !== undefined || barMode ? 0 : (finiteDrawn.length ? Math.min(...finiteDrawn) : 0);
  // Avoid a divide-by-zero (perfectly flat series) by collapsing the
  // range to 1 — flat lines render along the bottom, which reads as
  // "stable, low" at a glance and matches the eye's expectation.
  const range = max - min || 1;
  const step = values.length > 1 ? width / (values.length - 1) : 0;
  const pad = 1; // 1px top/bottom inset so the stroke isn't clipped
  const yOf = (v: number) =>
    height - pad - ((v - min) / range) * (height - 2 * pad);

  // v0.5.485 — threshold-cross detection (area mode). Any single
  // bucket past the threshold flips the colour. Latest-bucket-only
  // would hide a fresh dip; we report the window's health, not just
  // the trailing edge. Bar modes colour per-bucket instead.
  const crossed = !barMode && threshold !== undefined && values.some(v => {
    if (v == null) return false;
    switch (thresholdComparator) {
      case '>':  return v >  threshold;
      case '>=': return v >= threshold;
      case '<':  return v <  threshold;
      case '<=': return v <= threshold;
    }
  });
  const stroke = crossed ? 'var(--err)' : baseStroke;

  // v0.9.500 — çizgi/alan modunda nokta bütçesi. `step` ve y-ölçeği HAM
  // seri üzerinden hesaplanır (yukarıda), seyreltme yalnız ÇİZİLEN nokta
  // kümesini küçültür — LTTB seçtiği noktaların ORİJİNAL indeksini
  // döndürdüğü için x ekseni (zaman) birebir yerinde kalır. Eşik-geçme
  // tespiti de (crossed) ham seride: seyreltme bir spike'ı elerse
  // renk sinyali kaybolmamalı.
  const lineBudget = maxLinePointsForWidth(width);
  // v0.10.385 — yalnız dolu kovalar çizilir; seyreltme yoksa ardışık
  // olmayan indeksler arasında yol KESİLİR (delik = veri yok).
  let px: number[] = [];
  let py: number[] = [];
  values.forEach((v, i) => { if (v != null && Number.isFinite(v)) { px.push(i); py.push(v); } });
  const thinned = !barMode && lineBudget > 0 && py.length > lineBudget;
  if (thinned) {
    const ds = downsampleXY(px, py, lineBudget);
    px = ds.xs;
    py = ds.ys as number[];
  }

  // SVG path — line then close back along the baseline for the area fill.
  const segments: string[] = [];
  let seg: string[] = [];
  for (let k = 0; k < py.length; k++) {
    if (!thinned && k > 0 && px[k] - px[k - 1] > 1 && seg.length) { segments.push('M ' + seg.join(' L ')); seg = []; }
    seg.push(`${px[k] * step},${yOf(py[k])}`);
  }
  if (seg.length) segments.push('M ' + seg.join(' L '));
  const linePath = segments.join(' ');
  const lastX = px.length ? px[px.length - 1] * step : 0;
  const firstX = px.length ? px[0] * step : 0;
  const areaPath = `${linePath} L ${lastX},${height} L ${firstX},${height} Z`;

  // Bar-mode geometry (pure helper, vitest-pinned). Values past a
  // shared domainMax clamp to full height rather than overflowing.
  // Runs on the budget-capped `drawn` series (v0.9.207 review-fix).
  const bars = barMode ? barGeometry(drawn.map(v => v ?? 0), width, height, { domainMax, pad }) : [];
  const barFill = (v: number): { fill: string; opacity: number } => {
    if (mode === 'bars' && threshold !== undefined) {
      const cls = classifyThreshold(v, threshold);
      if (cls === 'err') return { fill: 'var(--err)', opacity: 0.92 };
      if (cls === 'warn') return { fill: 'var(--warn)', opacity: 0.88 };
      // Soluk gri normal bucket — mockup'taki rgba(text3,.55) muadili.
      return { fill: 'var(--text3)', opacity: 0.45 };
    }
    return { fill: baseStroke, opacity: 0.7 };
  };

  // Optional threshold line — dashed horizontal at the threshold's
  // y-position. Only renders when the threshold is within the
  // visible y-range; otherwise it'd be visually off-chart and
  // misleading. (Bar modes share the zero-based scale, so yOf works
  // for both.)
  const thresholdY = threshold !== undefined && threshold >= min && threshold <= max
    ? yOf(threshold) : null;

  // Inline delta chip — first non-zero to last non-zero.
  const deltaPct: number | null = (() => {
    if (!showDelta) return null;
    const firstNZ = values.find(v => v != null && v !== 0);
    const lastNZ = [...values].reverse().find(v => v != null && v !== 0);
    if (firstNZ == null || lastNZ == null || firstNZ === 0) return null;
    return ((lastNZ - firstNZ) / Math.abs(firstNZ)) * 100;
  })();

  const onMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const el = svgRef.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    const x = e.clientX - rect.left;
    if (barMode) {
      setHoverIdx(barIndexAt(x, width, drawn.length));
      return;
    }
    if (step === 0) return;
    const i = Math.round(x / step);
    if (i >= 0 && i < values.length) setHoverIdx(i);
  };
  const onMouseLeave = () => setHoverIdx(null);

  // Hover reads the drawn series (drawn === values outside bar modes);
  // `?? null` guards a stale index across a length-changing refetch.
  const hoverValue = hoverIdx != null ? (drawn[hoverIdx] ?? null) : null;
  // Plain tooltip when not hovered; live value when cursor over
  // a bucket. svg <title> renders as a native tooltip after a
  // short delay — good enough for tiny sparklines without
  // pulling in a positioned overlay layer. When bar buckets were
  // merged (v0.9.207 review-fix), the tooltip says so — a "bucket"
  // is then the max/sum over ~k source buckets, not a single 5m one.
  const mergedK = barMode && drawn.length > 0 && drawn.length < values.length
    ? Math.ceil(values.length / drawn.length) : 1;
  const liveTitle = hoverValue != null
    ? `bucket ${hoverIdx! + 1}/${drawn.length}: ${fmtVal(hoverValue, unit)}` +
      (mergedK > 1 ? ` (${mode === 'count' ? 'sum' : 'max'} of ${mergedK} buckets)` : '')
    : title || 'sparkline';

  const lastIdx = values.length - 1;
  // v0.10.385 — uç-nokta vurgusu son DOLU kovada (son kova null olabilir).
  const lastFilled = (() => {
    for (let i = lastIdx; i >= 0; i--) {
      const v = values[i];
      if (v != null && Number.isFinite(v)) return { i, v };
    }
    return null;
  })();
  const wrapped = (
    <svg
      ref={svgRef}
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className={className}
      style={{ display: 'block', cursor: onClick ? 'pointer' : 'default' }}
      role="img"
      aria-label={liveTitle}
      onMouseMove={onMouseMove}
      onMouseLeave={onMouseLeave}
      // v0.6.14 — stop propagation so a wrapping row's onClick
      // doesn't double-fire alongside the sparkline drill. The
      // operator clicked the sparkline; that intent overrides
      // any ambient row navigation behind it.
      onClick={onClick ? (e) => { e.stopPropagation(); onClick(); } : undefined}
    >
      <title>{liveTitle}</title>
      {!barMode && (
        <>
          {/* M4 — alan gradyanı: seri renginden şeffafa. stop-color
              CSS var'ı style üzerinden alır (presentation attribute
              olarak var() bazı tarayıcılarda çözülmüyor). */}
          <defs>
            <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" style={{ stopColor: stroke, stopOpacity: crossed ? 0.38 : 0.32 } as React.CSSProperties} />
              <stop offset="100%" style={{ stopColor: stroke, stopOpacity: 0 } as React.CSSProperties} />
            </linearGradient>
          </defs>
          <path d={areaPath} fill={`url(#${gid})`} stroke="none" />
        </>
      )}
      {barMode && bars.map(b => {
        const { fill, opacity } = barFill(b.v);
        return (
          <rect key={b.i} x={b.x} y={b.y} width={b.w} height={b.h}
            fill={fill} fillOpacity={hoverIdx === b.i ? Math.min(1, opacity + 0.25) : opacity} />
        );
      })}
      {thresholdY !== null && (
        <line x1={0} x2={width} y1={thresholdY} y2={thresholdY}
          stroke="var(--err)" strokeWidth={0.75} strokeDasharray="2 2"
          opacity={0.55} />
      )}
      {!barMode && (
        <>
          <path d={linePath} fill="none" stroke={stroke} strokeWidth={1.25}
            strokeLinejoin="round" strokeLinecap="round" />
          {/* M4 — uç-nokta vurgusu: son bucket'ın dot'u "şu an nerede"
              sorusunu tablo satırında okutur. Hover aynı noktadaysa
              hover halkası üstte kalır. */}
          {lastFilled != null && (
            <circle cx={lastFilled.i * step} cy={yOf(lastFilled.v)} r={2}
              fill={stroke} />
          )}
        </>
      )}
      {!barMode && hoverIdx != null && step > 0 && values[hoverIdx] != null && (
        <circle cx={hoverIdx * step} cy={yOf(values[hoverIdx] as number)} r={2}
          fill={stroke} stroke="var(--bg)" strokeWidth={0.75} />
      )}
    </svg>
  );

  if (!showDelta || deltaPct === null) return wrapped;
  const deltaColour =
    Math.abs(deltaPct) < 5 ? 'var(--text3)'
    : deltaPct > 0 ? 'rgb(220,38,38)'
    : 'rgb(46,160,67)';
  const arrow = Math.abs(deltaPct) < 5 ? '~' : deltaPct > 0 ? '↑' : '↓';
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      {wrapped}
      <span style={{
        fontSize: 10, color: deltaColour,
        fontFamily: 'ui-monospace, monospace',
      }} title={`first → last in window`}>
        {arrow} {Math.abs(deltaPct).toFixed(0)}%
      </span>
    </span>
  );
}

function fmtVal(v: number, unit?: string): string {
  if (!isFinite(v)) return '—';
  if (unit === 'ms' || unit === 's' || unit === '%') {
    return v.toFixed(v >= 100 ? 0 : 1) + (unit ? ' ' + unit : '');
  }
  if (Math.abs(v) >= 1_000_000_000) return (v / 1_000_000_000).toFixed(1) + 'B';
  if (Math.abs(v) >= 1_000_000)     return (v / 1_000_000).toFixed(1) + 'M';
  if (Math.abs(v) >= 1_000)         return (v / 1_000).toFixed(1) + 'k';
  return Number.isInteger(v) ? String(v) : v.toFixed(2);
}
