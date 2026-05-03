'use client';
import { useEffect, useState } from 'react';
import Link from 'next/link';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { Problem, TimeRange } from '@/lib/types';

export default function ProblemsPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [statusFilter, setStatusFilter] = useState<'open' | 'all' | 'resolved'>('open');
  const [data, setData] = useState<Problem[] | null | undefined>(undefined);

  useEffect(() => {
    setData(undefined);
    api.problems({
      status: statusFilter === 'all' ? undefined : statusFilter,
      limit: 200,
    }).then(d => setData(d ?? [])).catch(() => setData(null));
    const t = setInterval(() => {
      api.problems({ status: statusFilter === 'all' ? undefined : statusFilter, limit: 200 })
        .then(d => setData(d ?? []))
        .catch(() => {});
    }, 30_000);
    return () => clearInterval(t);
  }, [statusFilter]);

  const open = data?.filter(p => p.status === 'open').length ?? 0;
  const resolved = data?.filter(p => p.status === 'resolved').length ?? 0;

  return (
    <>
      <Topbar title="Problems" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
            {(['open', 'resolved', 'all'] as const).map(s => (
              <button key={s} onClick={() => setStatusFilter(s)}
                className={statusFilter === s ? '' : 'sec'}
                style={{ borderRadius: 0, borderRight: '1px solid var(--border)' }}>
                {s.charAt(0).toUpperCase() + s.slice(1)}
              </button>
            ))}
          </div>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            {open} open · {resolved} resolved
          </span>
          <Link href="/alerts" className="sec" style={{
            marginLeft: 'auto', textDecoration: 'none', padding: '5px 12px',
            border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)',
          }}>🔔 Manage alert rules</Link>
        </div>

        {data === undefined && <Spinner />}
        {data && data.length === 0 && (
          <Empty icon="✓" title={statusFilter === 'open' ? 'No open problems — all clear!' : 'No problems'}>
            The evaluator runs once per minute. Built-in rules cover error rate and P99 latency.
          </Empty>
        )}
        {data && data.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Severity</th>
                  <th>Service</th>
                  <th>Metric</th>
                  <th>Value</th>
                  <th>Rule</th>
                  <th>Started</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {data.map(p => {
                  const isAnomaly = p.ruleId?.startsWith('anomaly:');
                  return (
                    <tr key={p.id}>
                      <td><SeverityBadge s={p.severity} /></td>
                      <td>
                        <Link href={`/service?name=${encodeURIComponent(p.service)}`}
                          style={{ fontWeight: 600 }}>
                          {p.service}
                        </Link>
                      </td>
                      <td className="mono">{p.metric}</td>
                      <td className="mono">
                        <b style={{ color: 'var(--err)' }}>{p.value.toFixed(2)}</b>
                        <span style={{ color: 'var(--text3)' }}> / {p.threshold.toFixed(2)}</span>
                      </td>
                      <td style={{ fontSize: 12 }}>
                        {isAnomaly && (
                          <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>
                        )}
                        {p.ruleName}
                        {isAnomaly && (
                          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                            {p.description}
                          </div>
                        )}
                      </td>
                      <td className="mono">{tsLong(p.startedAt)}</td>
                      <td>
                        {p.status === 'open'
                          ? <span className="badge b-err">OPEN</span>
                          : <span className="badge b-ok">RESOLVED</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}
