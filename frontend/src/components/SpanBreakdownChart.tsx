import { useEffect, useMemo, useState } from 'react';
import { Spinner } from './Spinner';
import { api } from '@/lib/api';
import { fmtSmart } from '@/lib/chartFmt';

// SpanBreakdownChart — Elastic-APM-style "where does this service
// spend its time?" stacked-area view. Each band is a span category
// (db:postgres / queue:kafka / http / client / server / internal),
// stack height is cumulative ms of duration per bucket. Answers
// the operator's first triage question without flipping panels.
//
// Renders as a plain SVG — no chart library. The view is small
// (one chart per service detail) and the data shape (sparse map
// per bucket) doesn't fit MultiLineChart cleanly, so we build it
// inline. Hover crosshair surfaces per-band ms + share at the
// pointer's time bucket.

export interface BreakdownPoint {
  time: number;                  // unix ns (bucket start)
  kinds: Record<string, number>; // category → ms summed
}

// Stable category color palette so the legend lines up across
// renders. Categories not in the table fall through to a hashed
// colour so a new tag doesn't all-render as one shade.
const CATEGORY_COLORS: Record<string, string> = {
  internal:    '#6366f1',
  client:      '#22c55e',
  server:      '#3b82f6',
  producer:    '#a855f7',
  consumer:    '#ec4899',
  http:        '#0ea5e9',
};
// Family prefixes — "db:*", "queue:*" — share a hue so 8 different
// DBs read as "DB load" at a glance.
const FAMILY_COLORS: Record<string, string> = {
  db:    '#f97316',
  queue: '#eab308',
};

function colorFor(cat: string): string {
  if (CATEGORY_COLORS[cat]) return CATEGORY_COLORS[cat];
  const colonIdx = cat.indexOf(':');
  if (colonIdx > 0) {
    const family = cat.slice(0, colonIdx);
    if (FAMILY_COLORS[family]) return FAMILY_COLORS[family];
  }
  // Stable hash → HSL. Same input → same colour across reloads.
  let h = 0;
  for (let i = 0; i < cat.length; i++) h = (h * 31 + cat.charCodeAt(i)) >>> 0;
  return `hsl(${h % 360}, 55%, 55%)`;
}

// Per-service collapsed/expanded preference. localStorage so
// operators who want span breakdown hidden by default on a busy
// service detail page keep that preference across refreshes.
// v0.5.193 — added the collapse toggle; previously the chart was
// always expanded, which made the service detail's vertical
// scroll-length painful on services where the breakdown was a
// once-a-week glance.
const COLLAPSED_KEY = 'coremetry-span-breakdown-collapsed';

function readCollapsedSet(): Set<string> {
  try {
    const raw = localStorage.getItem(COLLAPSED_KEY);
    return new Set(raw ? JSON.parse(raw) : []);
  } catch { return new Set(); }
}
function writeCollapsedSet(s: Set<string>) {
  try { localStorage.setItem(COLLAPSED_KEY, JSON.stringify(Array.from(s))); } catch { /* ignore */ }
}

export function SpanBreakdownChart({ service, fromNs, toNs }: {
  service: string; fromNs: number; toNs: number;
}) {
  const [data, setData] = useState<BreakdownPoint[] | null | undefined>(undefined);
  const [hover, setHover] = useState<{ x: number; idx: number } | null>(null);
  const [collapsed, setCollapsed] = useState<boolean>(() => readCollapsedSet().has(service));

  // Persist toggle state per-service so a fresh visit reopens
  // in the operator's last orientation.
  const toggle = () => {
    setCollapsed(prev => {
      const next = !prev;
      const cur = readCollapsedSet();
      if (next) cur.add(service); else cur.delete(service);
      writeCollapsedSet(cur);
      return next;
    });
  };

  // Skip the data fetch when collapsed — the chart isn't on
  // screen, no point asking the backend for a stacked-area
  // payload every range change. Re-expand triggers the fetch
  // via the dependency on `collapsed`.
  useEffect(() => {
    if (!service || collapsed) return;
    setData(undefined);
    api.spanBreakdown(service, fromNs, toNs)
      .then(d => setData(d ?? []))
      .catch(() => setData(null));
  }, [service, fromNs, toNs, collapsed]);

  // Discover the active categories + their total ms across the
  // window. The legend ranks categories by total so the busiest
  // band is anchored to the bottom of the stack (visually
  // stable — the slim "internal" doesn't shift up when a noisy
  // category appears).
  //
  // v0.5.237 — moved ABOVE the early-return for the collapsed
  // mode. Conditional hook ordering was a Rules-of-Hooks
  // violation: toggling collapsed changed the hook count
  // between renders and React threw at runtime.
  const stack = useMemo(() => {
    if (!data || data.length === 0) return null;
    const totals: Record<string, number> = {};
    for (const p of data) {
      for (const [k, v] of Object.entries(p.kinds)) {
        totals[k] = (totals[k] ?? 0) + v;
      }
    }
    const cats = Object.keys(totals).sort((a, b) => totals[b] - totals[a]);
    const stackedMax = data.reduce((m, p) => {
      const sum = cats.reduce((s, c) => s + (p.kinds[c] ?? 0), 0);
      return Math.max(m, sum);
    }, 0);
    return { cats, totals, stackedMax };
  }, [data]);

  // Collapsed mode = just the header with a click-to-expand
  // affordance. Doesn't render the SVG, doesn't paint the
  // legend — saves a measurable chunk of layout time on
  // services with 20+ categories. All hooks above so the
  // ordering stays stable across collapsed ↔ expanded.
  if (collapsed) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 12, marginTop: 18,
      }}>
        <button type="button" onClick={toggle}
          style={{
            display: 'flex', alignItems: 'baseline', gap: 8,
            width: '100%', textAlign: 'left',
            background: 'transparent', border: 'none', cursor: 'pointer',
            color: 'var(--text)', padding: 0,
          }}>
          <span style={{ fontSize: 11, color: 'var(--text3)',
            fontFamily: 'ui-monospace, monospace' }}>▶</span>
          <span style={{ fontSize: 13, fontWeight: 700 }}>Span breakdown</span>
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            collapsed — click to expand
          </span>
        </button>
      </div>
    );
  }

  if (data === undefined) return <div style={{ padding: 14 }}><Spinner /></div>;
  if (data === null) return (
    <div style={{ padding: 12, fontSize: 12, color: 'var(--err)' }}>
      Failed to load span breakdown.
    </div>
  );
  if (!stack || data.length === 0) return (
    <div style={{ padding: 12, fontSize: 12, color: 'var(--text3)' }}>
      No spans for <code>{service}</code> in this window.
    </div>
  );

  const W = 920;       // logical width; SVG scales to container
  const H = 180;
  const padL = 56, padR = 12, padT = 8, padB = 22;
  const innerW = W - padL - padR;
  const innerH = H - padT - padB;

  // Time axis: linear across the bucket array index. Buckets are
  // already uniformly spaced server-side so we don't need
  // wall-time projection here — just index → x.
  const xOf = (i: number) =>
    padL + (data.length > 1 ? (i / (data.length - 1)) * innerW : innerW / 2);
  const yOf = (v: number) =>
    padT + innerH - (v / Math.max(1, stack.stackedMax)) * innerH;

  // Build stacked paths bottom-up. For each category band we
  // generate two paths (upper edge + lower edge) and close them.
  const bands = stack.cats.map((cat, ci) => {
    const upper: string[] = [];
    const lower: string[] = [];
    for (let i = 0; i < data.length; i++) {
      let bottom = 0;
      for (let cj = 0; cj < ci; cj++) bottom += data[i].kinds[stack.cats[cj]] ?? 0;
      const top = bottom + (data[i].kinds[cat] ?? 0);
      upper.push(`${xOf(i)},${yOf(top)}`);
      lower.push(`${xOf(i)},${yOf(bottom)}`);
    }
    const d = `M ${upper.join(' L ')} L ${lower.reverse().join(' L ')} Z`;
    return { cat, d, color: colorFor(cat) };
  });

  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
    const x = e.clientX - rect.left;
    const scaledX = x * (W / rect.width);
    const idx = Math.max(0, Math.min(data.length - 1,
      Math.round(((scaledX - padL) / innerW) * (data.length - 1))));
    setHover({ x: xOf(idx), idx });
  };
  const onLeave = () => setHover(null);

  // Y-axis ticks at 0%, 25%, 50%, 75%, 100% of stackedMax.
  const yTicks = [0, 0.25, 0.5, 0.75, 1].map(p => p * stack.stackedMax);

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 12, marginTop: 18,
    }}>
      <button type="button" onClick={toggle}
        style={{
          display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8,
          width: '100%', textAlign: 'left',
          background: 'transparent', border: 'none', cursor: 'pointer',
          color: 'var(--text)', padding: 0,
        }}>
        <span style={{ fontSize: 11, color: 'var(--text3)',
          fontFamily: 'ui-monospace, monospace' }}>▼</span>
        <span style={{ fontSize: 13, fontWeight: 700 }}>Span breakdown</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          where the service spends its time — stacked by category
        </span>
      </button>
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H}
           preserveAspectRatio="none"
           onMouseMove={onMove} onMouseLeave={onLeave}
           style={{ display: 'block' }}>
        {/* Y-axis ticks + labels */}
        {yTicks.map((v, i) => (
          <g key={i}>
            <line x1={padL} x2={W - padR} y1={yOf(v)} y2={yOf(v)}
                  stroke="var(--border)" strokeOpacity={0.4} />
            <text x={padL - 4} y={yOf(v) + 3} textAnchor="end"
                  fontSize={10} fill="var(--text3)"
                  fontFamily="ui-monospace, SFMono-Regular, monospace">
              {fmtSmart(v, 'ms')}
            </text>
          </g>
        ))}
        {/* Stacked bands */}
        {bands.map(b => (
          <path key={b.cat} d={b.d} fill={b.color} fillOpacity={0.85} stroke="none" />
        ))}
        {/* Hover crosshair */}
        {hover && (
          <line x1={hover.x} x2={hover.x} y1={padT} y2={padT + innerH}
                stroke="var(--text)" strokeOpacity={0.4} strokeDasharray="3 3" />
        )}
      </svg>

      {/* Legend + hover tooltip below — legend always visible,
          tooltip occupies the same row so the layout doesn't
          jump when hovering. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 8, fontSize: 11 }}>
        {stack.cats.map(cat => (
          <span key={cat} style={{
            display: 'inline-flex', alignItems: 'center', gap: 4,
            color: 'var(--text2)',
          }}>
            <span style={{
              width: 10, height: 10, borderRadius: 2,
              background: colorFor(cat), display: 'inline-block',
            }} />
            <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>{cat}</span>
            <span style={{ color: 'var(--text3)' }}>
              {hover
                ? fmtSmart(data[hover.idx].kinds[cat] ?? 0, 'ms')
                : fmtSmart(stack.totals[cat], 'ms')}
            </span>
          </span>
        ))}
      </div>
    </div>
  );
}
