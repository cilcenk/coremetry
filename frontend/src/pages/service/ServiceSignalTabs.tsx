import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceFlow } from './ServiceFlow';

// Service-scoped Traces / Logs / Topology tabs (v0.7.98) — the design's
// tab strip beyond Overview/Operations/Details. Read-only.

// ── Traces: slowest traces for this service ─────────────────────────────
export function ServiceTracesTab({ service, range }: { service: string; range: TimeRange }) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const rangeParam = encodeRange(range);
  const q = useQuery({
    queryKey: ['service-tab-traces', service, from, to],
    queryFn: () => api.traces({ service, from, to, sort: 'duration', order: 'desc', limit: 25, count: 'skip' }),
    enabled: !!service,
    staleTime: 30_000,
  });
  const traces = q.data?.traces ?? [];
  const maxDur = useMemo(() => Math.max(1, ...traces.map(t => t.durationMs)), [traces]);

  return (
    <div className="card" style={{ marginTop: 4 }}>
      <div className="ov-card-h">
        <h3>Slowest traces</h3>
        <span className="ov-right">
          <Link className="ov-sub" to={`/traces?service=${encodeURIComponent(service)}&range=${rangeParam}`}>Open in Traces →</Link>
        </span>
      </div>
      {q.isLoading ? (
        <div className="ov-card-b"><Spinner /></div>
      ) : traces.length === 0 ? (
        <div className="ov-card-b"><Empty icon="⋮" title="No traces in this window" /></div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <colgroup>
              <col style={{ width: 150 }} /><col /><col style={{ width: 70 }} />
              <col style={{ width: 80 }} /><col style={{ width: 160 }} />
            </colgroup>
            <thead><tr>
              <th style={{ textAlign: 'left' }}>Trace</th>
              <th style={{ textAlign: 'left' }}>Root operation</th>
              <th className="num">Spans</th>
              <th className="num">Status</th>
              <th className="num">Duration</th>
            </tr></thead>
            <tbody>
              {traces.map(t => (
                <tr key={t.traceId} style={{ cursor: 'pointer' }}>
                  <td><Link className="mono" style={{ color: 'var(--accent)' }} to={`/trace?id=${t.traceId}`}>{t.traceId.slice(0, 16)}…</Link></td>
                  <td><span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'block' }} title={t.rootName}>{t.rootName || '—'}</span></td>
                  <td className="num">{t.spanCount}</td>
                  <td className="num"><span className={`badge ${t.hasError ? 'b-err' : 'b-ok'}`}>{t.hasError ? 'ERROR' : 'OK'}</span></td>
                  <td>
                    <div className="ov-barcell">
                      <span className="mono" style={{ minWidth: 56 }}>{t.durationMs.toFixed(0)} ms</span>
                      <span className="ov-minibar"><i style={{ width: `${(t.durationMs / maxDur) * 100}%`, background: t.hasError ? 'var(--err)' : 'var(--accent)' }} /></span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ── Logs: CTA into the Logs workspace ───────────────────────────────────
export function ServiceLogsTab({ service, range }: { service: string; range: TimeRange }) {
  const rangeParam = encodeRange(range);
  return (
    <div className="card" style={{ marginTop: 4 }}>
      <div className="ov-card-b" style={{ display: 'grid', placeItems: 'center', textAlign: 'center', padding: '48px 16px', gap: 12 }}>
        <div style={{ fontSize: 32 }}>≡</div>
        <div style={{ fontSize: 16, fontWeight: 700 }}>Logs stream</div>
        <div style={{ color: 'var(--text2)', fontSize: 13, maxWidth: 460 }}>
          Open the Logs workspace to query structured logs for <b style={{ color: 'var(--text)' }}>{service}</b> —
          full KQL search, severity facets, patterns, and trace correlation.
        </div>
        <Link to={`/logs?service=${encodeURIComponent(service)}&range=${rangeParam}`}
          style={{ marginTop: 4, padding: '7px 16px', borderRadius: 6, background: 'var(--accent)', color: '#fff', textDecoration: 'none', fontSize: 13, fontWeight: 600 }}>
          ≡ Open in Logs
        </Link>
      </div>
    </div>
  );
}

// ── Topology: the 1-hop flow + a link to the full graph ─────────────────
export function ServiceTopologyTab({ service, range }: { service: string; range: TimeRange }) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const rangeParam = encodeRange(range);
  return (
    <div style={{ marginTop: 4 }}>
      <ServiceFlow service={service} range={range} from={from} to={to} />
      <div className="card">
        <div className="ov-card-b" style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
          <span style={{ color: 'var(--text2)', fontSize: 13 }}>
            The map above is this service's direct (1-hop) callers + dependencies. Explore the full graph, multi-hop flows, and noise filtering in Topology.
          </span>
          <Link to={`/topology?focus=${encodeURIComponent(service)}&preset=${encodeURIComponent(range.preset)}`}
            style={{ marginLeft: 'auto', padding: '6px 14px', borderRadius: 6, background: 'var(--accent)', color: '#fff', textDecoration: 'none', fontSize: 13, fontWeight: 600 }}>
            ⋔ Open full Topology →
          </Link>
        </div>
      </div>
    </div>
  );
}
