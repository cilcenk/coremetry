import { useRef, useState } from 'react';

// Tiny inline SVG sparkline — no chart library. Auto-scales to its own
// y-range so a service with 5 spans/min looks just as readable as one
// with 50k. Renders a faint area fill under the line for legibility on
// dense table rows.
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

interface Props {
  values: number[];      // y-values; index = time bucket
  width?: number;        // default 80
  height?: number;       // default 22
  color?: string;        // default --accent2
  title?: string;        // SVG <title>; used as base tooltip text
  className?: string;
  // Optional unit for the hover tooltip's value formatting ("ms", "%", etc).
  unit?: string;
  // v0.5.485 — threshold colouring. When any value satisfies the
  // comparator vs threshold, the line and area shift to red so the
  // sparkline reads as "unhealthy" without the operator needing to
  // mouse over.
  threshold?: number;
  thresholdComparator?: '>' | '>=' | '<' | '<=';
  // v0.5.485 — click affordance. When set, cursor:pointer and the
  // sparkline becomes a button. Use for "drill into /metrics for
  // the full view".
  onClick?: () => void;
  // v0.5.485 — inline delta chip (`+35%`) next to the SVG. Compares
  // last non-zero bucket to first non-zero bucket. Caller-driven so
  // dense tables can opt out per-column.
  showDelta?: boolean;
}

export function Sparkline({
  values, width = 80, height = 22, color, title, className,
  unit, threshold, thresholdComparator = '>', onClick, showDelta,
}: Props) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);

  const baseStroke = color || 'var(--accent2)';
  const nonZero = values.filter(v => v > 0);
  if (values.length === 0 || nonZero.length === 0) {
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
  const max = Math.max(...values);
  const min = Math.min(...values);
  // Avoid a divide-by-zero (perfectly flat series) by collapsing the
  // range to 1 — flat lines render along the bottom, which reads as
  // "stable, low" at a glance and matches the eye's expectation.
  const range = max - min || 1;
  const step = values.length > 1 ? width / (values.length - 1) : 0;
  const pad = 1; // 1px top/bottom inset so the stroke isn't clipped
  const yOf = (v: number) =>
    height - pad - ((v - min) / range) * (height - 2 * pad);

  // v0.5.485 — threshold-cross detection. Any single bucket past
  // the threshold flips the colour. Latest-bucket-only would
  // hide a fresh dip; we report the window's health, not just
  // the trailing edge.
  const crossed = threshold !== undefined && values.some(v => {
    switch (thresholdComparator) {
      case '>':  return v >  threshold;
      case '>=': return v >= threshold;
      case '<':  return v <  threshold;
      case '<=': return v <= threshold;
    }
  });
  const stroke = crossed ? 'var(--err)' : baseStroke;

  // SVG path — line then close back along the baseline for the area fill.
  const linePoints = values.map((v, i) => `${i * step},${yOf(v)}`).join(' L ');
  const linePath = `M ${linePoints}`;
  const areaPath = `${linePath} L ${(values.length - 1) * step},${height} L 0,${height} Z`;

  // Optional threshold line — dashed horizontal at the threshold's
  // y-position. Only renders when the threshold is within the
  // visible y-range; otherwise it'd be visually off-chart and
  // misleading.
  const thresholdY = threshold !== undefined && threshold >= min && threshold <= max
    ? yOf(threshold) : null;

  // Inline delta chip — first non-zero to last non-zero.
  const deltaPct: number | null = (() => {
    if (!showDelta) return null;
    const firstNZ = values.find(v => v !== 0);
    const lastNZ = [...values].reverse().find(v => v !== 0);
    if (firstNZ == null || lastNZ == null || firstNZ === 0) return null;
    return ((lastNZ - firstNZ) / Math.abs(firstNZ)) * 100;
  })();

  const onMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const el = svgRef.current;
    if (!el || step === 0) return;
    const rect = el.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const i = Math.round(x / step);
    if (i >= 0 && i < values.length) setHoverIdx(i);
  };
  const onMouseLeave = () => setHoverIdx(null);

  const hoverValue = hoverIdx != null ? values[hoverIdx] : null;
  // Plain tooltip when not hovered; live value when cursor over
  // a bucket. svg <title> renders as a native tooltip after a
  // short delay — good enough for tiny sparklines without
  // pulling in a positioned overlay layer.
  const liveTitle = hoverValue != null
    ? `bucket ${hoverIdx! + 1}/${values.length}: ${fmtVal(hoverValue, unit)}`
    : title || 'sparkline';

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
      onClick={onClick}
    >
      <title>{liveTitle}</title>
      <path d={areaPath} fill={stroke} fillOpacity={crossed ? 0.20 : 0.15} stroke="none" />
      {thresholdY !== null && (
        <line x1={0} x2={width} y1={thresholdY} y2={thresholdY}
          stroke="var(--err)" strokeWidth={0.75} strokeDasharray="2 2"
          opacity={0.55} />
      )}
      <path d={linePath} fill="none" stroke={stroke} strokeWidth={1.25}
        strokeLinejoin="round" strokeLinecap="round" />
      {hoverIdx != null && step > 0 && (
        <circle cx={hoverIdx * step} cy={yOf(values[hoverIdx])} r={2}
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
