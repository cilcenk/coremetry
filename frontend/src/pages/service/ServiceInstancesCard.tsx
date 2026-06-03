import { useId, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { InfraMetricSeries } from '@/lib/types';
import { Spinner } from '@/components/Spinner';

// ServiceInstancesCard (Overview tab) — the per-pod infra card from the
// design handoff. Two CPU/Mem trend mini-charts (service-level, reusing
// serviceInfraMetrics) above one row per pod with CPU% / Memory gauges
// (serviceInstances → metric_points grouped by host_name). Token-only,
// light+dark safe. Per-pod throughput is intentionally omitted: a per-host
// rate needs a raw-spans GROUP BY, which would violate the MV-bypass
// invariant on this hot page — the KPI row above already shows throughput.

// Responsive full-bleed area mini-chart (no axes — the prototype's small
// trend look). preserveAspectRatio="none" stretches the fixed viewBox to
// the column width.
function MiniArea({ data, color, height = 60 }: { data: number[]; color: string; height?: number }) {
  const gid = useId();
  if (data.length < 2) {
    return <div style={{ height, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 11 }}>no data</div>;
  }
  const W = 200, H = 60, pad = 2;
  const mn = Math.min(...data), mx = Math.max(...data), rng = mx - mn || 1;
  const xs = (i: number) => pad + (i / (data.length - 1)) * (W - pad * 2);
  const ys = (v: number) => H - pad - ((v - mn) / rng) * (H - pad * 2 - 2);
  const line = data.map((v, i) => `${i ? 'L' : 'M'}${xs(i).toFixed(1)},${ys(v).toFixed(1)}`).join(' ');
  const area = `${line} L${xs(data.length - 1).toFixed(1)},${H} L${xs(0).toFixed(1)},${H} Z`;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ width: '100%', height, display: 'block' }} aria-hidden="true">
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.25" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${gid})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

// Pull one infra slot's points out of the serviceInfraMetrics payload and
// scale them (cpu utilisation fraction → %, memory bytes → MiB).
function slotData(data: InfraMetricSeries[] | undefined | null, slot: string, scale: (v: number) => number): number[] {
  const s = data?.find(d => d.metric === slot);
  return (s?.points ?? []).map(p => scale(p.v));
}

export function ServiceInstancesCard({ service, since }: { service: string; since: string }) {
  const infraQ = useQuery({
    queryKey: ['svc-instances-infra', service, since],
    queryFn: () => api.serviceInfraMetrics(service, since),
    enabled: !!service, staleTime: 30_000,
  });
  const podQ = useQuery({
    queryKey: ['svc-instances-pods', service, since],
    queryFn: () => api.serviceInstances(service, since),
    enabled: !!service, staleTime: 30_000,
  });

  const pods = useMemo(() => podQ.data ?? [], [podQ.data]);
  const cpuTrend = useMemo(() => slotData(infraQ.data, 'cpu', v => v * 100), [infraQ.data]);
  const memTrend = useMemo(() => slotData(infraQ.data, 'memory', v => v / 1048576), [infraQ.data]);
  // Memory gauge baseline: real % when a limit is reported, else relative to
  // the busiest pod so the bars stay comparable across Go services too.
  const maxMem = useMemo(() => Math.max(1, ...pods.map(p => p.memBytes)), [pods]);

  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>Instances</h3>
        <span className="ov-sub">
          {pods.length} pod{pods.length === 1 ? '' : 's'}{pods[0]?.zone ? ` · ${pods[0].zone}` : ''}
        </span>
        <span className="ov-right">
          <span className="ov-legend">
            <span><i className="ov-sw" style={{ background: 'var(--accent)' }} />CPU</span>
            <span><i className="ov-sw" style={{ background: 'var(--purple)' }} />Mem</span>
          </span>
        </span>
      </div>
      <div className="ov-card-b" style={{ paddingBottom: 8 }}>
        {/* Two trend mini-charts (service-level) */}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginBottom: 14 }}>
          <div>
            <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 2 }}>CPU · %</div>
            <MiniArea data={cpuTrend} color="var(--accent)" />
          </div>
          <div>
            <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 2 }}>Memory · MiB</div>
            <MiniArea data={memTrend} color="var(--purple)" />
          </div>
        </div>

        {/* Per-pod rows */}
        {podQ.isLoading ? (
          <Spinner />
        ) : pods.length === 0 ? (
          <div style={{ color: 'var(--text2)', fontSize: 13 }}>No per-pod metrics for {service} in this window.</div>
        ) : (
          pods.map(p => {
            const memGauge = p.memPct > 0 ? p.memPct : (p.memBytes / maxMem) * 100;
            return (
              <div className="ov-inst" key={p.id} style={{ gridTemplateColumns: '1fr auto auto' }}>
                <div className="ov-id">
                  <span className={`ov-dot ${p.up ? 'green' : 'red'}`} />
                  {p.id}
                  {p.zone && <span style={{ color: 'var(--text3)', fontWeight: 400 }}>&nbsp;· {p.zone}</span>}
                </div>
                <div className="ov-mini">
                  <div className="l">CPU</div>
                  <div className="v">{p.cpuPct.toFixed(0)}%</div>
                  <div className="ov-gauge"><i style={{ width: `${p.cpuPct}%`, background: p.cpuPct > 70 ? 'var(--warn)' : 'var(--accent)' }} /></div>
                </div>
                <div className="ov-mini">
                  <div className="l">Mem{p.memPct > 0 ? '' : ' · MiB'}</div>
                  <div className="v">{p.memPct > 0 ? `${p.memPct.toFixed(0)}%` : (p.memBytes / 1048576).toFixed(0)}</div>
                  <div className="ov-gauge"><i style={{ width: `${memGauge}%`, background: 'var(--purple)' }} /></div>
                </div>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}
