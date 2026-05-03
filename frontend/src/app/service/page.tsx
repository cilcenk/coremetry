'use client';
import { Suspense, useEffect, useState } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { Service, ServiceEdgeStats, Problem, TimeRange } from '@/lib/types';

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

function ServiceDetailInner() {
  const searchParams = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [info, setInfo] = useState<Service | null>(null);
  const [callers, setCallers] = useState<ServiceEdgeStats[]>([]);
  const [callees, setCallees] = useState<ServiceEdgeStats[]>([]);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!svc) return;
    setLoading(true);
    const since = SINCE_MAP[range.preset] ?? '24h';
    Promise.all([
      api.services(timeRangeToNs(range)),
      api.serviceCallers(svc, since),
      api.serviceCallees(svc, since),
      api.problems({ service: svc, limit: 50 }),
    ]).then(([all, up, down, probs]) => {
      setInfo((all ?? []).find(s => s.name === svc) ?? null);
      setCallers(up ?? []);
      setCallees(down ?? []);
      setProblems(probs ?? []);
    }).finally(() => setLoading(false));
  }, [svc, range]);

  if (!svc) {
    return (
      <>
        <Topbar title="Service" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing service name" /></div>
      </>
    );
  }

  const openProbs = problems.filter(p => p.status === 'open');

  return (
    <>
      <Topbar title={`Service · ${svc}`} range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          <Link href="/services" className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← All services</Link>
          {info && (
            <>
              <KPI label="Spans" value={fmtNum(info.spanCount)} />
              <KPI label="Errors" value={`${info.errorRate.toFixed(2)}%`}
                cls={info.errorRate > 5 ? 'err' : info.errorRate > 0 ? 'warn' : 'ok'} />
              <KPI label="Avg" value={`${info.avgDurationMs.toFixed(1)}ms`} />
              <KPI label="P99" value={`${info.p99DurationMs.toFixed(1)}ms`} />
            </>
          )}
          <Link href={`/traces?service=${encodeURIComponent(svc)}`} style={{
            marginLeft: 'auto', fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }}>⋮ View traces</Link>
        </div>

        {openProbs.length > 0 && (
          <div className="trace-lock" style={{
            borderColor: 'rgba(255,82,82,.4)', background: 'rgba(255,82,82,.06)',
          }}>
            <span style={{ color: 'var(--err)', fontWeight: 600 }}>⚠ {openProbs.length} open problem(s)</span>
            {openProbs.slice(0, 3).map(p => (
              <span key={p.id} style={{ color: 'var(--text2)', fontSize: 11 }}>
                · {p.ruleName}
              </span>
            ))}
            <Link href="/problems" style={{ marginLeft: 'auto', fontSize: 11 }}>View all →</Link>
          </div>
        )}

        {loading && <Spinner />}
        {!loading && (
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <DependencyTable
              title="Upstream callers"
              hint="Services that send requests to this one"
              icon="←"
              rows={callers}
              empty="No upstream callers in this window" />
            <DependencyTable
              title="Downstream dependencies"
              hint="Services / backends this service calls"
              icon="→"
              rows={callees}
              empty="No outgoing calls in this window" />
          </div>
        )}
      </div>
    </>
  );
}

function KPI({ label, value, cls }: { label: string; value: string; cls?: string }) {
  return (
    <div style={{
      padding: '4px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', fontSize: 12,
    }}>
      <span style={{ color: 'var(--text2)' }}>{label}: </span>
      <b style={{
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)'
          : cls === 'ok' ? 'var(--ok)' : 'var(--text)',
      }}>{value}</b>
    </div>
  );
}

function DependencyTable({ title, hint, icon, rows, empty }: {
  title: string; hint: string; icon: string;
  rows: ServiceEdgeStats[]; empty: string;
}) {
  return (
    <div>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700 }}>{icon} {title}</h3>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{hint}</span>
      </div>
      {rows.length === 0 ? (
        <div className="empty" style={{ padding: 30 }}>{empty}</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Service</th>
                <th style={{ textAlign: 'right' }}>Calls</th>
                <th style={{ textAlign: 'right' }}>Err %</th>
                <th style={{ textAlign: 'right' }}>Avg</th>
                <th style={{ textAlign: 'right' }}>P99</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(r => (
                <tr key={r.service}>
                  <td>
                    <Link href={`/service?name=${encodeURIComponent(r.service)}`} style={{ fontWeight: 600 }}>
                      {r.service}
                    </Link>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(r.calls)}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <span className={`badge ${r.errorRate > 5 ? 'b-err' : r.errorRate > 0 ? 'b-warn' : 'b-ok'}`}>
                      {r.errorRate.toFixed(1)}%
                    </span>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{r.avgMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{r.p99Ms.toFixed(1)}ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

export default function ServiceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ServiceDetailInner />
    </Suspense>
  );
}
