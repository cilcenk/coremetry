'use client';

// Tiny inline SVG sparkline — no chart library. Auto-scales to its own
// y-range so a service with 5 spans/min looks just as readable as one
// with 50k. Renders a faint area fill under the line for legibility on
// dense table rows.
//
// Single empty bucket → "—" placeholder so a brand-new service doesn't
// render an ambiguous flat line.

interface Props {
  values: number[];      // y-values; index = time bucket
  width?: number;        // default 80
  height?: number;       // default 22
  color?: string;        // default --accent2
  title?: string;        // tooltip text (e.g. "spans/5m: 12 → 47")
  className?: string;
}

export function Sparkline({
  values, width = 80, height = 22, color, title, className,
}: Props) {
  const stroke = color || 'var(--accent2)';
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

  // SVG path — line then close back along the baseline for the area fill.
  const linePoints = values.map((v, i) => `${i * step},${yOf(v)}`).join(' L ');
  const linePath = `M ${linePoints}`;
  const areaPath = `${linePath} L ${(values.length - 1) * step},${height} L 0,${height} Z`;

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className={className}
      style={{ display: 'block', cursor: 'pointer' }}
      role="img"
      aria-label={title || 'sparkline'}
    >
      {title && <title>{title}</title>}
      <path d={areaPath} fill={stroke} fillOpacity={0.15} stroke="none" />
      <path d={linePath} fill="none" stroke={stroke} strokeWidth={1.25}
            strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}
