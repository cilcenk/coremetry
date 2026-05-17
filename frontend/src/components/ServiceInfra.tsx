import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Sparkline } from './Sparkline';
import { ServiceRuntimeBadge } from './ServiceRuntimeBadge';
import { api } from '@/lib/api';
import { useServiceRuntime } from '@/lib/queries';
import { fmtBytes } from '@/lib/utils';
import type { InfraMetricSeries } from '@/lib/types';

// ServiceInfra renders curated runtime / process metrics for the
// inspected service alongside the trace-side panels on
// /service?name=…. Lets the SRE answer "p99 spiked at 14:32 — was
// the pod CPU starved?" in one glance, without leaving the page.
//
// Slots are canonical (cpu / memory / rps / runtime); the server
// picks the most-specific source per slot for the service's
// runtime (jvm.* for Java, process.runtime.* for Go, k8s.pod.*
// when available). Empty slots collapse silently.
export function ServiceInfra({ service, since = '15m' }: {
  service: string;
  since?: string;
}) {
  const [data, setData] = useState<InfraMetricSeries[] | null | undefined>(undefined);

  useEffect(() => {
    if (!service) return;
    setData(undefined);
    api.serviceInfraMetrics(service, since)
      .then(d => setData(d ?? []))
      .catch(() => setData(null));
  }, [service, since]);

  if (data === undefined || data === null || data.length === 0) return null;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 14,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
        <span style={{ fontSize: 13, fontWeight: 600 }}>
          Infra (last {since})
        </span>
        {/* Runtime badge — language + runtime version. Sits
            inline with the panel title so the operator
            instantly knows whether they're investigating a
            JVM service vs a Go binary vs a .NET app vs a
            Node.js process. Hidden when the resource attrs
            don't have enough info (some SDKs only emit one
            of language/runtime). */}
        <RuntimeBadge service={service} />
        <span style={{ fontSize: 11, color: 'var(--text3)', flex: 1 }}>
          process / pod metrics correlated with span timeline · click a tile to drill into the metric explorer
        </span>
      </div>
      <div style={{
        display: 'grid',
        gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
        gap: 12,
      }}>
        {data.map(s => (
          <InfraTile key={s.metric} s={s} service={service} />
        ))}
      </div>
      {/* CPU + Memory + RPS overlay (v0.5.194). Three lines on a
          single 15-min chart, each normalised to its own range
          so an operator can scan for correlated spikes ("CPU
          went up at the same minute as RPS — saturation, not a
          leak"). Shape > absolute values; the tiles above keep
          the absolute numbers if the operator wants them. */}
      <InfraTrendChart data={data} />
    </div>
  );
}

// InfraTrendChart — overlay of the three canonical infra
// metrics over the same time window. Each line normalised to
// [0..1] against its own max in the window so the shapes are
// directly comparable even when CPU is "0.4 cores" and RPS is
// "240 req/s". Renders nothing when fewer than two metrics are
// available — a single line would just duplicate the tile.
function InfraTrendChart({ data }: { data: InfraMetricSeries[] }) {
  // Pick the three canonical slots. The InfraMetricSeries
  // `metric` field is the slot name set server-side
  // (cpu / memory / rps); we just look them up in the array.
  const cpu = data.find(s => s.metric === 'cpu');
  const mem = data.find(s => s.metric === 'memory');
  const rps = data.find(s => s.metric === 'rps');
  const lines: Array<{ label: string; color: string; unit: string; series: InfraMetricSeries }> = [];
  if (cpu) lines.push({ label: 'CPU',    color: '#22c55e', unit: cpu.unit || '',    series: cpu });
  if (mem) lines.push({ label: 'Memory', color: '#3b82f6', unit: mem.unit || 'B',   series: mem });
  if (rps) lines.push({ label: 'RPS',    color: '#a855f7', unit: rps.unit || 'req/s', series: rps });
  if (lines.length < 2) return null;

  const W = 1000, H = 140, padL = 6, padR = 6, padT = 8, padB = 14;
  // Common time axis = min/max across the three series. Most
  // SDKs emit points at the same cadence; even when they
  // don't, picking the union range keeps every line in frame.
  const ts: number[] = [];
  for (const l of lines) for (const p of l.series.points) ts.push(p.t);
  if (ts.length === 0) return null;
  const minT = Math.min(...ts);
  const maxT = Math.max(...ts);
  if (maxT === minT) return null;
  const xOf = (t: number) => padL + ((t - minT) / (maxT - minT)) * (W - padL - padR);

  // Per-series y-normalisation [0..1] against the line's own
  // max. Mean and shape readable; absolute value lives on the
  // tile above.
  const norm = (series: InfraMetricSeries) => {
    let max = 0;
    for (const p of series.points) if (p.v > max) max = p.v;
    if (max === 0) max = 1;
    return series.points.map(p => ({ t: p.t, n: p.v / max, v: p.v }));
  };
  const yOf = (n: number) => padT + (1 - n) * (H - padT - padB);

  return (
    <div style={{ marginTop: 14, paddingTop: 10, borderTop: '1px dashed var(--border)' }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 12,
        fontSize: 11, color: 'var(--text3)', marginBottom: 6,
      }}>
        <span style={{ fontWeight: 600, color: 'var(--text2)' }}>
          Trend ({lines.length}-line overlay, normalised)
        </span>
        {lines.map(l => {
          const last = l.series.points[l.series.points.length - 1]?.v ?? 0;
          const lbl = l.label === 'Memory'
            ? fmtBytes(last)
            : l.label === 'RPS'
            ? `${last.toFixed(1)}`
            : `${(last * 100).toFixed(1)}%`;
          return (
            <span key={l.label} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <span style={{ width: 10, height: 2, background: l.color, display: 'inline-block' }} />
              <span>{l.label}</span>
              <span style={{ color: 'var(--text2)', fontFamily: 'ui-monospace, monospace' }}>{lbl}</span>
            </span>
          );
        })}
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H}
        preserveAspectRatio="none"
        style={{ display: 'block', background: 'var(--bg2)', borderRadius: 4 }}>
        {/* Horizontal guide lines at 25/50/75% normalised */}
        {[0.25, 0.5, 0.75].map(g => (
          <line key={g} x1={padL} x2={W - padR} y1={yOf(g)} y2={yOf(g)}
            stroke="var(--border)" strokeOpacity={0.35} />
        ))}
        {lines.map(l => {
          const pts = norm(l.series);
          if (pts.length === 0) return null;
          const d = pts.map((p, i) =>
            `${i === 0 ? 'M' : 'L'} ${xOf(p.t).toFixed(1)} ${yOf(p.n).toFixed(1)}`
          ).join(' ');
          return (
            <path key={l.label} d={d} fill="none" stroke={l.color}
              strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
          );
        })}
      </svg>
    </div>
  );
}

// RuntimeBadge renders a small "Java OpenJDK 21" / "Go 1.22"
// pill from the service's resource attributes. Layered detail:
//
//   • Language icon (text glyph) — fastest visual cue
//   • Runtime + version — the actionable detail (Java 17 vs
//     21 changes which debugger flags you reach for)
//   • Host + OS shown on hover via title attribute so the
//     panel header doesn't get crowded
//
// Falls back to nothing visible when the SDK didn't emit
// any usable metadata — the badge component returns null in
// that case rather than rendering "Unknown".
// RuntimeBadge here just adapts the data hook (single-service
// query) to the shared ServiceRuntimeBadge presenter. The
// presenter + the language→glyph/colour helpers all live in
// components/ServiceRuntimeBadge.tsx so /services listing can
// reuse them with a batch data hook.
function RuntimeBadge({ service }: { service: string }) {
  const q = useServiceRuntime(service);
  return <ServiceRuntimeBadge rt={q.data} />;
}

function InfraTile({ s, service }: { s: InfraMetricSeries; service: string }) {
  const last = s.points.length > 0 ? s.points[s.points.length - 1].v : 0;
  const max  = s.points.length > 0 ? Math.max(...s.points.map(p => p.v)) : 0;
  const min  = s.points.length > 0 ? Math.min(...s.points.map(p => p.v)) : 0;
  const label = LABELS[s.metric] ?? s.metric.toUpperCase();
  // Drill-down to the metrics explorer with this exact source
  // metric pre-loaded for the same service. The explorer reads
  // ?source/?service/?metric on mount (see /explore page).
  const href = `/explore?source=metrics&service=${encodeURIComponent(service)}&metric=${encodeURIComponent(s.source)}`;
  return (
    <Link to={href} title={`Open ${s.source} in metric explorer`}
      style={{
        padding: 10, border: '1px solid var(--border)',
        borderRadius: 6, background: 'var(--bg2)',
        textDecoration: 'none', color: 'inherit',
        display: 'block', cursor: 'pointer',
        transition: 'border-color 120ms, background 120ms',
      }}
      className="infra-tile">
      <div style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        {label}
      </div>
      <div style={{ fontSize: 18, fontWeight: 700, marginTop: 2 }}>
        {fmtValue(last, s.metric, s.unit)}
      </div>
      <div style={{ marginTop: 4 }}>
        <Sparkline values={s.points.map(p => p.v)}
                   color={COLORS[s.metric] ?? 'var(--accent2)'}
                   title={`${s.source} · last ${s.points.length} buckets`} />
      </div>
      <div style={{
        display: 'flex', justifyContent: 'space-between',
        fontSize: 10, color: 'var(--text3)', marginTop: 2,
        fontFamily: 'ui-monospace, monospace',
      }}>
        <span>min {fmtValue(min, s.metric, s.unit)}</span>
        <span>max {fmtValue(max, s.metric, s.unit)}</span>
      </div>
      <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 2 }} title={s.source}>
        src: {s.source} ↗
      </div>
    </Link>
  );
}

const LABELS: Record<string, string> = {
  cpu:     'CPU',
  memory:  'Memory',
  rps:     'Requests',
  runtime: 'Runtime',
  heap:    'Heap',
};

const COLORS: Record<string, string> = {
  cpu:     'var(--warn)',
  memory:  'var(--accent)',
  rps:     'var(--accent2)',
  runtime: 'var(--text2)',
  heap:    'var(--err)',
};

function fmtValue(v: number, slot: string, unit: string): string {
  if (!isFinite(v)) return '—';
  // CPU often comes as 0..1 ratio — display as %.
  if (slot === 'cpu' || unit === '%') {
    if (v >= 1) return `${v.toFixed(1)}%`;
    return `${(v * 100).toFixed(1)}%`;
  }
  if (slot === 'memory' || slot === 'heap' || unit === 'bytes') {
    return fmtBytes(v);
  }
  if (slot === 'rps' || unit === '/s') {
    return v >= 1000 ? `${(v / 1000).toFixed(1)}k/s` : `${v.toFixed(1)}/s`;
  }
  // generic numeric
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`;
  return v.toFixed(0);
}

