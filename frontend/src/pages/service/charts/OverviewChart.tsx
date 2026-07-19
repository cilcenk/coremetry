import { useMemo, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { useThemeTick } from '@/lib/useThemeTick';
import { fmtXTicks, fmtAxisTick } from '@/lib/chartFmt';
import { overviewChartBuildSignature } from '@/lib/chartBuildSig';
import { resolveVar } from '@/lib/chart/resolveVar';
import { yRangeHeadroom } from '@/lib/chart/yRange';
import { xRangePinned, type XPin } from '@/lib/chart/xRange';
import { useChartEngine } from '@/lib/chart/engine';
import { sortedTooltipRows } from '@/lib/chart/tooltipModel';
import { placeTooltip } from '@/lib/chartTooltip';

// OverviewChart (v0.7.94) — the compact RED chart for the Service Overview.
// A purpose-built uPlot wrapper matching the design handoff: ~150px, clean
// (no axes chrome beyond 0/50/100 gridlines), a dashed-purple deploy marker
// with a ▼ flag, and a hover crosshair + per-series tooltip.
//
// v0.9.97 (chart-consolidation Adım 1) — new uPlot / destroy / ResizeObserver
// / setData fast-path (v0.8.531 rebuild-vs-setData + v0.9.78/79 zoom-koruma)
// İSKELETİ lib/chart/engine.ts::useChartEngine'e çıkarıldı. Bu bileşen artık
// bir PRESET: buildOptions (renk çözümü + opts) + data + signature + flag
// hook'ları verir; yaşam döngüsü motorda. DAVRANIŞ birebir korunur (aynı
// opts/data/hook'lar); kanıt: overviewChartBuildSignature kontratı +
// engine.seam.test. Motoru kullanan İLK preset — TC/MLC/TSP Adım 2-4'te.

export interface OvChartSeries {
  label: string;
  color: string;  // a CSS var() string, resolved at draw time
  // v0.9.87 — null = veri yok (Runtime paneli union-align boşlukları);
  // line/area GAP çizer, stacked 0 sayar (v0.9.73 TimeChart emsali).
  data: (number | null)[];
}

interface Props {
  times: number[];            // unix seconds, ascending — shared x axis
  series: OvChartSeries[];
  height?: number;            // default 150
  mode?: 'line' | 'area' | 'stacked';
  unit?: string;              // " ms", "%", " req/s" …
  deployAtSec?: number | null; // deploy time (unix sec) → dashed vline + flag
  deployLabel?: string;       // e.g. "v1.0.0"
  // v0.8.534 — drag-select zoom → parent range (fromSec, toSec).
  onZoom?: (fromSec: number, toSec: number) => void;
  // v0.9.83 pin — v0.9.94'te inert (veriye-fit revert); imza korunuyor.
  xRange?: XPin | null;
}

export function OverviewChart({
  times, series, height = 150, mode = 'line', unit = '', deployAtSec = null, deployLabel = 'deploy', onZoom, xRange,
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  // onZoom in a ref (v0.8.520 pattern) so the once-per-build setSelect hook
  // always calls the latest without re-registering.
  const onZoomRef = useRef(onZoom); onZoomRef.current = onZoom;
  const xRangeRef = useRef(xRange); xRangeRef.current = xRange;
  const ttRef = useRef<HTMLDivElement>(null);
  const flagRef = useRef<HTMLDivElement>(null);
  const themeTick = useThemeTick();

  // uPlot aligned data (stacked-aware) — memoised on the data inputs + mode.
  // Tooltip reads RAW per-series values, so keep the raw series in a ref too.
  const built = useMemo(() => {
    const stacked = mode === 'stacked';
    let matrix: (number | null)[][];
    if (stacked) {
      const cum: (number | null)[][] = [];
      for (let i = 0; i < series.length; i++) {
        const below = cum[i - 1];
        cum[i] = series[i].data.map((v, j) => (below ? (below[j] ?? 0) : 0) + (v ?? 0));
      }
      matrix = cum;
    } else {
      matrix = series.map(s => s.data);
    }
    return { data: [times, ...matrix] as uPlot.AlignedData };
  }, [times, series, mode]);
  const rawRef = useRef({ series }); rawRef.current = { series };

  // Build signature — series shape + mode + unit + deploy + height. Point
  // VALUES ride setData; `renderable` (≥2 x points) flips the sig for empty→data.
  const buildSig = overviewChartBuildSignature({
    series,
    height, mode, unit, deployAtSec, deployLabel,
    renderable: times.length >= 2 && series.length > 0,
    hasZoom: !!onZoom,
  });

  // Repositions the DOM ▼ deploy flag (above the canvas) at the marker x.
  // Called after build + after the setData fast-path + on resize (engine hooks).
  const placeFlag = (u: uPlot) => {
    const flag = flagRef.current;
    if (!flag) return;
    if (deployAtSec == null) { flag.style.display = 'none'; return; }
    const x = u.valToPos(deployAtSec, 'x', false);
    if (x < 0 || x > u.over.clientWidth) { flag.style.display = 'none'; return; }
    flag.style.display = 'block';
    flag.style.left = `${x}px`;
  };

  // buildOptions — renkleri REBUILD ANINDA çöz (tema flip'inde motor bu fn'i
  // yeniden çağırır, CSS-var'lar taze). Eski build effect'in opts'unun birebiri.
  const buildOptions = (width: number): uPlot.Options => {
    const colors = series.map(s => resolveVar(s.color));
    const gridc = resolveVar('var(--border)');
    const text3 = resolveVar('var(--text3)');
    const purple = resolveVar('var(--purple)');
    const stacked = mode === 'stacked';

    // Dashed-purple deploy marker, drawn under the series (re-paints on every
    // redraw incl. setData, tracking the live x-scale).
    const deployPlugin: uPlot.Plugin = {
      hooks: {
        draw: u => {
          if (deployAtSec == null) return;
          const ctx = u.ctx;
          const x = Math.round(u.valToPos(deployAtSec, 'x', true));
          if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) return;
          ctx.save();
          ctx.strokeStyle = purple;
          ctx.globalAlpha = 0.8;
          ctx.lineWidth = 1.4 * devicePixelRatio;
          ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
          ctx.beginPath();
          ctx.moveTo(x, u.bbox.top);
          ctx.lineTo(x, u.bbox.top + u.bbox.height);
          ctx.stroke();
          ctx.restore();
        },
      },
    };

    return {
      width,
      height,
      cursor: {
        x: true, y: false, points: { show: true, size: 7 },
        // v0.8.534 — x-only drag-zoom with instant local rescale.
        drag: { x: true, y: false, setScale: true },
      },
      legend: { show: false },
      // v0.9.93 (uPlot Aşama 3) — stacked'te pxAlign:0 komşu bant dolguları
      // arası 1px saç-teli beyaz çizgiyi kaldırır; non-stacked'te crisp 1.
      pxAlign: stacked ? 0 : 1,
      scales: {
        x: { time: true, range: (u, mn, mx) => xRangePinned(u.data[0] as number[], xRangeRef.current, mn, mx) },
        y: { range: yRangeHeadroom },
      },
      axes: [
        {
          stroke: text3, grid: { show: false }, ticks: { show: false }, size: 22,
          font: '10px ui-monospace, monospace',
          // v0.9.88 — çıplak ":30" yerine ev formatlayıcı fmtXTicks.
          values: (_u, sp) => fmtXTicks(sp as number[]),
        },
        {
          stroke: text3, size: 34, font: '10px ui-monospace, monospace',
          grid: { stroke: gridc, width: 1, dash: [3, 4] },
          ticks: { show: false },
          // splits derive from the LIVE scale max so a setData re-fit updates
          // the gridlines (the old build-time `max` went stale). Positions stay
          // [0, mid, max] (layout unchanged); only the FORMAT is now smart.
          splits: u => { const mx = u.scales.y.max ?? 1; return [0, mx / 2, mx]; },
          // v0.9.102 (Grafana-parity #3) — smart unit-aware ticks (fmtAxisTick):
          // "125ms" / "12.5%" / "1.2k"; was unitless inline toFixed.
          values: (_u, sp) => sp.map(v => fmtAxisTick(v, unit)),
        },
      ],
      series: [
        {},
        ...series.map((s, i) => ({
          label: s.label,
          stroke: colors[i],
          width: 1.8,
          points: { show: false },
          // area → gradient fill to baseline; stacked → only the BOTTOM series
          // fills to baseline, the rest via the bands between cumulative lines.
          ...(mode === 'area'
            ? { fill: (u: uPlot, si: number) => {
                const ctx = u.ctx;
                const g = ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
                g.addColorStop(0, colors[si - 1] + '47');  // ~28% alpha
                g.addColorStop(1, colors[si - 1] + '00');
                return g;
              } }
            : stacked && i === 0
            ? { fill: colors[0] + '47' }
            : {}),
        })),
      ],
      // Stacked bands: fill between cumulative line k and k-1 in series k's
      // colour (uPlot series are 1-based; data series i is uPlot series i+1).
      bands: stacked
        ? series.slice(1).map((_s, k) => ({ series: [k + 2, k + 1] as [number, number], fill: colors[k + 1] + '47' }))
        : undefined,
      hooks: {
        // v0.8.534 — drag-zoom release → hand [from,to] (unix sec) to parent
        // (updates ?range=); reset the grey band. Only when onZoom is set.
        setSelect: onZoom ? [
          (u: uPlot) => {
            const sel = u.select;
            if (!sel || sel.width < 4) return; // tiny accidental drag
            const x0 = u.posToVal(sel.left, 'x');
            const x1 = u.posToVal(sel.left + sel.width, 'x');
            if (!isFinite(x0) || !isFinite(x1)) return;
            onZoomRef.current?.(Math.min(x0, x1), Math.max(x0, x1));
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ] : undefined,
        setCursor: [
          u => {
            const tt = ttRef.current;
            if (!tt) return;
            const idx = u.cursor.idx;
            if (idx == null || u.cursor.left == null || u.cursor.left < 0) { tt.style.display = 'none'; return; }
            const xs = u.data[0] as number[];
            const tSec = xs[idx] as number;
            if (tSec == null) { tt.style.display = 'none'; return; }
            // Read RAW values from the ref (stacked draws cumulative into u.data)
            // — fresh on the fast-path; labels/colours are structural.
            const raw = rawRef.current.series;
            // v0.9.101 (Grafana-parity Adım 1) — sorted "all series" tooltip via
            // the shared model: value desc + fmtSmart units (was naive in-order
            // toFixed); empty series drop out.
            const rows = sortedTooltipRows(
              series.map((s, i) => ({ label: s.label, color: colors[i], value: raw[i]?.data[idx] ?? null, unit })),
            );
            if (rows.length === 0) { tt.style.display = 'none'; return; }
            const ts = new Date(tSec * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            tt.innerHTML = `<div class="ov-tt-t">${ts}</div>` + rows.map(r =>
              `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${r.color}"></i>${r.label}</span><b>${r.text}</b></div>`,
            ).join('');
            tt.style.display = 'block';
            // placeTooltip: flip/clamp so the panel never sits under the cursor
            // or off-canvas (MLC/TSP parity). Host is the uPlot mount; the
            // tooltip is absolute in the same wrapper at the host's origin.
            const host = hostRef.current;
            if (host) {
              const p = placeTooltip(
                u.cursor.left ?? 0, u.cursor.top ?? 0,
                tt.offsetWidth, tt.offsetHeight,
                u.over.clientWidth, u.over.clientHeight,
                u.over.offsetLeft, u.over.offsetTop,
                host.clientWidth, host.clientHeight,
              );
              tt.style.left = `${p.x}px`;
              tt.style.top = `${p.y}px`;
            } else {
              tt.style.left = `${u.cursor.left}px`;
              tt.style.top = `${Math.max(8, u.cursor.top ?? 20)}px`;
            }
          },
        ],
      },
      plugins: [deployPlugin],
    };
  };

  // Yaşam döngüsü motorda (v0.9.97). refitScales verilmez → varsayılan tüm
  // seriler tek y eksenine (OVC tek eksenli); flag üç noktada da yeniden konumlanır.
  useChartEngine(hostRef, {
    signature: buildSig,
    height,
    renderable: times.length >= 2 && series.length > 0,
    data: built.data,
    buildOptions,
    afterBuild: placeFlag,
    afterData: placeFlag,
    onResize: placeFlag,
  }, themeTick);

  return (
    <div className="ov-chart-wrap" style={{ position: 'relative' }}>
      <div ref={hostRef} style={{ width: '100%' }} />
      <div ref={ttRef} className="ov-tt" style={{ display: 'none' }} />
      {deployAtSec != null && (
        <div ref={flagRef} className="ov-deploy-flag" style={{ top: 0, display: 'none' }}>▼ {deployLabel}</div>
      )}
    </div>
  );
}
