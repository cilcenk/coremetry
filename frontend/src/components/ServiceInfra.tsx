import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Sparkline } from './Sparkline';
import { ServiceRuntimeBadge } from './ServiceRuntimeBadge';
import { api } from '@/lib/api';
import { useServiceRuntime } from '@/lib/queries';
import { metricCatalogueHref } from '@/pages/explore/urlCodec';
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
  // Drill-down to Explore as a real builder query A (source=metric) scoped to
  // this service — Phase 5 collapse moved this off the retired source=metrics
  // panel onto the canonical /explore?q= seed.
  const href = metricCatalogueHref(s.source, { service });
  return (
    <Link to={href} title={`Open ${s.source} in Explore`}
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

