import { useMemo, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { useThemeTick } from '@/lib/useThemeTick';
import { fmtXTicks, fmtAxisTick } from '@/lib/chartFmt';
import { timeChartBuildSignature } from '@/lib/chartBuildSig';
import { resolveVar } from '@/lib/chart/resolveVar';
import { yRangeHeadroom } from '@/lib/chart/yRange';
import { yRefitScale } from '@/lib/chart/zoomState';
import { xRangePinned, type XPin } from '@/lib/chart/xRange';
import { useChartEngine } from '@/lib/chart/engine';
import { StatsLegend } from '@/components/chart/StatsLegend';
import { stepGapsRefiner, nearestFilledIdx } from '@/lib/chart/gapPolicy';
import { sortedTooltipRows } from '@/lib/chart/tooltipModel';
import { placeTooltip } from '@/lib/chartTooltip';

// TimeChart (v0.8.91) — the ONE time-series primitive. Per-series type
// (bar | line | area), an optional right (dual) axis, drag-to-brush, deploy
// markers, a hover crosshair + per-series tooltip, cross-chart cursor sync.
//
// v0.8.531 — rebuild-vs-setData split (STRUCTURE signature + theme tick
// rebuild; data-only refresh rides setData; live callbacks via refs).
//
// v0.9.98 (chart-consolidation Adım 2) — new uPlot / destroy / ResizeObserver
// / setData fast-path (v0.9.78-79 zoom-koruma) İSKELETİ engine.ts::
// useChartEngine'e çıkarıldı; bu bileşen bir PRESET. TC dual-axis olduğu için
// refitScales'i override eden İLK preset (y/y2 seri-kümeleri ayrı refit).
// DAVRANIŞ birebir korunur (OVC Adım 1 ile aynı seam).

export interface TimeChartSeries {
  key: string;
  label: string;
  // aligned to `times`. null = veri yok → line/area GAP çizer (bar 0).
  // v0.9.73 — sparse metrik serilerinde (p50 gibi trafik-boş bucket)
  // 0 basıp çizgiyi tabana çakmak yerine gerçek boşluk gösterir.
  data: (number | null)[];
  color: string;           // a CSS var() token, resolved at draw time
  type: 'bar' | 'line' | 'area';
  axis?: 'left' | 'right'; // default 'left'
  width?: number;          // line width (line/area)
  // v0.9.73 — line/area üzerinde nokta göster (seyrek serilerde her
  // gerçek örnek okunur; bar'da yok sayılır).
  pointsShow?: boolean;
}

interface Props {
  times: number[];                  // unix seconds, ascending — shared x axis
  series: TimeChartSeries[];
  height?: number;                  // default 150
  leftUnit?: string;
  rightUnit?: string;
  deployMarkers?: number[];         // unix seconds → dashed red vlines
  onBrush?: (fromMs: number, toMs: number) => void;
  syncKey?: string;                 // uPlot.sync group for cross-chart crosshair
  fmtLeft?: (v: number) => string;  // y label formatter (left)
  fmtRight?: (v: number) => string; // y label formatter (right)
  // Optional x-tick formatter override (unix seconds → label). Default
  // stays the house day-boundary formatter (fmtXTicks); the Problems
  // detail passes its windowed rule (problemTime.fmtHistTick) here.
  fmtX?: (tsSec: number) => string;
  // v0.9.83 pin — v0.9.94'te inert (veriye-fit revert); imza korunuyor.
  xRange?: XPin | null;
}

// v0.9.75 (chart-consolidation Adım 0) — cssVar/yRange lib/chart/'a çıkarıldı
// (OVC ile byte-identical'dı). v0.9.102 (#3) — local kfmt yerine paylaşılan
// fmtAxisTick (birim-farkında, SI); tek tick formatlayıcı tüm panellerde.

export function TimeChart({
  times, series, height = 150, leftUnit = '', rightUnit = '',
  deployMarkers, onBrush, syncKey, fmtLeft, fmtRight, fmtX, xRange,
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const ttRef = useRef<HTMLDivElement>(null);
  // Live callbacks + formatters held in refs (v0.8.531): the once-per-build
  // hooks / axis formatters read `.current`, so a caller passing a fresh arrow
  // each render (VolumeChart's inline `fmtRight`) never churns a rebuild — only
  // its PRESENCE (tracked in the build signature) does.
  const onBrushRef = useRef(onBrush); onBrushRef.current = onBrush;
  const xRangeRef = useRef(xRange); xRangeRef.current = xRange;
  const fmtLeftRef = useRef(fmtLeft); fmtLeftRef.current = fmtLeft;
  const fmtRightRef = useRef(fmtRight); fmtRightRef.current = fmtRight;
  const fmtXRef = useRef(fmtX); fmtXRef.current = fmtX;
  const themeTick = useThemeTick();

  // uPlot's aligned data — memoised on the DATA inputs only so a poll recomputes
  // it once and either seeds a rebuild (structure changed) or rides setData.
  const chartData = useMemo<uPlot.AlignedData>(
    () => [times, ...series.map(s => s.data)] as uPlot.AlignedData,
    [times, series],
  );

  // Build signature — everything that forces a full re-create. Series POINT
  // VALUES + the x `times` array are absent (they ride setData). `renderable`
  // (≥2 x points) flips the sig so an empty→data first paint creates the plot.
  const buildSig = timeChartBuildSignature({
    series,
    height, leftUnit, rightUnit, deployMarkers, syncKey,
    hasBrush: !!onBrush, hasFmtLeft: !!fmtLeft, hasFmtRight: !!fmtRight, hasFmtX: !!fmtX,
    renderable: times.length >= 2 && series.length > 0,
  });

  // buildOptions — renkleri REBUILD ANINDA çöz (tema flip'te motor yeniden
  // çağırır); eski build effect'in opts'unun birebiri. width motordan gelir.
  const buildOptions = (width: number): uPlot.Options => {
    const colors = series.map(s => resolveVar(s.color));
    const gridc = resolveVar('var(--border)');
    const text3 = resolveVar('var(--text3)');
    const hasRight = series.some(s => s.axis === 'right');

    const barPath = uPlot.paths.bars!({ size: [0.86, Infinity], align: 0 });

    // Deploy markers — dashed red vlines under the series. Drawn in a hook so
    // they re-paint on every redraw (incl. setData), tracking the live x-scale.
    const deployPlugin: uPlot.Plugin = {
      hooks: {
        draw: u => {
          if (!deployMarkers?.length) return;
          const ctx = u.ctx;
          ctx.save();
          ctx.strokeStyle = resolveVar('var(--err)');
          ctx.globalAlpha = 0.8;
          ctx.lineWidth = 1.4 * devicePixelRatio;
          ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
          for (const sec of deployMarkers) {
            const x = Math.round(u.valToPos(sec, 'x', true));
            if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) continue;
            ctx.beginPath();
            ctx.moveTo(x, u.bbox.top);
            ctx.lineTo(x, u.bbox.top + u.bbox.height);
            ctx.stroke();
          }
          ctx.restore();
        },
      },
    };

    // Axis whose ticks/splits derive from the LIVE scale max so a setData
    // re-fit updates the gridlines (the old build-time `max` closure would go
    // stale on the fast-path). fmt read through its ref for live formatting.
    const yAxis = (scale: string, side: 0 | 1, fmtRef: React.MutableRefObject<((v: number) => string) | undefined>, showGrid: boolean, unit: string): uPlot.Axis => ({
      scale, side, stroke: text3, size: 38, font: '10px ui-monospace, monospace',
      grid: showGrid ? { stroke: gridc, width: 1, dash: [3, 4] } : { show: false },
      ticks: { show: false },
      splits: u => { const mx = (u.scales[scale].max ?? 1); return [0, mx / 2, mx]; },
      // Consumer fmtLeft/fmtRight still wins; v0.9.102 (Grafana-parity #3) the
      // FALLBACK is now the shared unit-aware fmtAxisTick (was local kfmt) —
      // "125ms" / "1.2k" instead of a bare count.
      values: (_u, sp) => sp.map(v => (fmtRef.current ? fmtRef.current(v) : fmtAxisTick(v, unit))),
    });

    const axes: uPlot.Axis[] = [
      {
        stroke: text3, grid: { show: false }, ticks: { show: false }, size: 20,
        font: '10px ui-monospace, monospace',
        // v0.8.402 — house day-boundary formatter (fmtXTicks stamps MM-DD on
        // the first tick of each new day); space thins ticks so wider
        // date+time labels never overlap.
        values: (_u, sp) => (fmtXRef.current ? sp.map(fmtXRef.current) : fmtXTicks(sp)),
        space: fmtX ? 90 : 70,
      },
      yAxis('y', 0, fmtLeftRef, true, leftUnit),
    ];
    if (hasRight) axes.push(yAxis('y2', 1, fmtRightRef, false, rightUnit));

    return {
      width,
      height,
      cursor: {
        x: true, y: false, points: { show: true, size: 7 },
        drag: { x: !!onBrush, y: false, setScale: false },
        // v0.9.84 (madde 4) — seyrek (null'lu) line/area seride hover en
        // yakın DOLU örneğe snap'ler (±2 bucket, sınırsız arama yok).
        dataIdx: (u, sidx, idx) => {
          const st = series[sidx - 1];
          if (!st || st.type === 'bar') return idx;
          return nearestFilledIdx(u.data[sidx] as (number | null)[], idx, 2);
        },
        ...(syncKey ? { sync: { key: syncKey } } : {}),
      },
      legend: { show: false },
      scales: {
        x: { time: true, range: (u, mn, mx) => xRangePinned(u.data[0] as number[], xRangeRef.current, mn, mx) },
        y: { range: yRangeHeadroom },
        ...(hasRight ? { y2: { range: yRangeHeadroom } } : {}),
      },
      axes,
      series: [
        {},
        ...series.map((s, i) => {
          const scale = s.axis === 'right' ? 'y2' : 'y';
          if (s.type === 'bar') {
            return { label: s.label, scale, stroke: colors[i], fill: colors[i], width: 0, paths: barPath, points: { show: false } } as uPlot.Series;
          }
          const base: uPlot.Series = {
            label: s.label, scale, stroke: colors[i], width: s.width ?? 1.8,
            points: s.pointsShow ? { show: true, size: 4 } : { show: false },
            // null = gap; v0.9.84 (madde 3) — tek kaçmış scrape köprülenir
            // (< 1.5×step), gerçek kesinti kırık kalır (stepGapsRefiner).
            gaps: stepGapsRefiner,
          };
          if (s.type === 'area') base.fill = colors[i] + '33';
          return base;
        }),
      ],
      hooks: {
        setSelect: onBrush ? [u => {
          const w = u.select.width;
          if (w < 2) return;
          const a = u.posToVal(u.select.left, 'x');
          const b = u.posToVal(u.select.left + w, 'x');
          onBrushRef.current?.(Math.round(a * 1000), Math.round(b * 1000));
          u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
        }] : undefined,
        setCursor: [u => {
          const tt = ttRef.current;
          if (!tt) return;
          const idx = u.cursor.idx;
          if (idx == null || u.cursor.left == null || u.cursor.left < 0) { tt.style.display = 'none'; return; }
          // Read x + values LIVE from u.data — the setData fast-path may have
          // swapped them without rebuilding this closure. series labels/colours/
          // axis are structural (rebuild on change), so closing over them is safe.
          const xs = u.data[0] as number[];
          const tSec = xs[idx] as number;
          if (tSec == null) { tt.style.display = 'none'; return; }
          // v0.8.402 — include the DAY when the chart spans more than one.
          const dd = new Date(tSec * 1000);
          const sameDay = xs.length > 1 &&
            new Date((xs[0] as number) * 1000).toDateString() === new Date((xs[xs.length - 1] as number) * 1000).toDateString();
          const hm = dd.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
          const ts = sameDay ? hm
            : `${String(dd.getMonth() + 1).padStart(2, '0')}-${String(dd.getDate()).padStart(2, '0')} ${hm}`;
          // v0.9.101 (Grafana-parity Adım 1) — sorted "all series" tooltip via
          // the shared model: value desc + fmtSmart units (per-axis left/right
          // unit); was naive in-order kfmt with 0-for-gap. Per-series snapped
          // idx (v0.9.84); a genuine gap now drops out instead of reading "0".
          const rows = sortedTooltipRows(series.map((s, i) => {
            const si = u.cursor.idxs?.[i + 1] ?? idx;
            return {
              label: s.label, color: colors[i],
              value: (u.data[i + 1] as (number | null)[])?.[si] ?? null,
              unit: s.axis === 'right' ? rightUnit : leftUnit,
            };
          }));
          if (rows.length === 0) { tt.style.display = 'none'; return; }
          tt.innerHTML = `<div class="ov-tt-t">${ts}</div>` + rows.map(r =>
            `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${r.color}"></i>${r.label}</span><b>${r.text}</b></div>`,
          ).join('');
          tt.style.display = 'block';
          // placeTooltip flip/clamp (MLC/TSP parity) — host is the uPlot mount.
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
        }],
      },
      plugins: [deployPlugin],
    };
  };

  // Yaşam döngüsü motorda (v0.9.98). TC dual-axis → refitScales OVERRIDE:
  // zoomlu fast-path'te y (left seriler) + y2 (right seriler) ayrı refit
  // (motorun batch/setData(false)'i içinde çağrılır). series canlı okunur;
  // chartData zaten series'ten türer → [chartData] dep'i yeterli.
  useChartEngine(hostRef, {
    signature: buildSig,
    height,
    renderable: times.length >= 2 && series.length > 0,
    data: chartData,
    buildOptions,
    refitScales: (u, data) => {
      const leftIdxs: number[] = [];
      const rightIdxs: number[] = [];
      series.forEach((s, i) => (s.axis === 'right' ? rightIdxs : leftIdxs).push(i + 1));
      u.setScale('y', yRefitScale(data as (number | null)[][], leftIdxs));
      if (rightIdxs.length && u.scales.y2) {
        u.setScale('y2', yRefitScale(data as (number | null)[][], rightIdxs));
      }
    },
  }, themeTick);

  return (
    <>
      <div className="ov-chart-wrap" style={{ position: 'relative' }}>
        <div ref={hostRef} style={{ width: '100%' }} />
        <div ref={ttRef} className="ov-tt" style={{ display: 'none' }} />
      </div>
      {/* v0.9.103 (Grafana-parity #1) — grafik altında seri istatistikleri;
          birim seri-başı (dual eksende left/right). */}
      <StatsLegend series={series.map(s => ({
        label: s.label, color: s.color, values: s.data,
        unit: s.axis === 'right' ? rightUnit : leftUnit,
      }))} />
    </>
  );
}
