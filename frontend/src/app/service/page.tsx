'use client';
import { Suspense, useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceStructure } from '@/components/ServiceStructure';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { encodeFilters, encodeRange, buildQuery } from '@/lib/urlState';
import type { Service, ServiceEdgeStats, Problem, TimeRange, OperationSummary } from '@/lib/types';

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

function ServiceDetailInner() {
  const searchParams = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  const [info, setInfo] = useState<Service | null>(null);
  const [callers, setCallers] = useState<ServiceEdgeStats[]>([]);
  const [callees, setCallees] = useState<ServiceEdgeStats[]>([]);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [operations, setOperations] = useState<OperationSummary[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!svc) return;
    setLoading(true);
    const since = SINCE_MAP[range.preset] ?? '24h';
    const r = timeRangeToNs(range);
    Promise.all([
      api.services(r),
      api.serviceCallers(svc, since),
      api.serviceCallees(svc, since),
      api.problems({ service: svc, limit: 50 }),
      api.serviceOperations(svc, r),
    ]).then(([all, up, down, probs, ops]) => {
      setInfo((all ?? []).find(s => s.name === svc) ?? null);
      setCallers(up ?? []);
      setCallees(down ?? []);
      setProblems(probs ?? []);
      setOperations(ops ?? []);
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
          <>
            <ServiceStructure service={svc} callers={callers} callees={callees} />

            <OperationsTable service={svc} rows={operations} range={range} />

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
          </>
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

// OperationsTable — per-operation aggregate (count / err / avg / p50 /
// p95 / p99 / apdex). Click an operation to drill into Explore with
// `name = <op>` pre-filtered alongside the service. Sortable; aggregate
// "All" row at the top mirrors the services page so totals are visible
// without scrolling.
type OpSortKey = 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99' | 'apdex';
const OP_NATURAL_DIR: Record<OpSortKey, 'asc' | 'desc'> = {
  name: 'asc', spanCount: 'desc', errorRate: 'desc',
  avg: 'desc', p99: 'desc', apdex: 'asc',
};

function OperationsTable({ service, rows, range }: { service: string; rows: OperationSummary[]; range: TimeRange }) {
  const [sortBy, setSortBy] = useState<OpSortKey>('spanCount');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  // Drill-down to Explore with both service.name + name pre-filtered.
  // Same pattern as the services-page sparkline click-throughs so the
  // user lands in a familiar view.
  const opHref = (op: string) => {
    const q = buildQuery([
      ['range', encodeRange(range)],
      ['filters', encodeFilters([
        { k: 'service.name', op: '=', v: [service] },
        { k: 'name',         op: '=', v: [op] },
      ])],
      ['result', 'traces'],
    ]);
    return `/explore?${q}`;
  };

  const sorted = useMemo(() => {
    const cmp = (a: OperationSummary, b: OperationSummary): number => {
      switch (sortBy) {
        case 'name':      return a.name.localeCompare(b.name);
        case 'spanCount': return a.spanCount - b.spanCount;
        case 'errorRate': return a.errorRate - b.errorRate;
        case 'avg':       return a.avgDurationMs - b.avgDurationMs;
        case 'p99':       return a.p99DurationMs - b.p99DurationMs;
        case 'apdex':     return (a.apdex ?? 0) - (b.apdex ?? 0);
      }
    };
    const arr = [...rows].sort(cmp);
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [rows, sortBy, sortDir]);

  // Same weighted-aggregate scheme as the services page totals row:
  // sum spans/errs, weight avg/apdex by span count, take max for p99.
  const agg = useMemo(() => {
    if (rows.length === 0) return null;
    let totalSpans = 0, totalErrs = 0, wAvg = 0, wApdex = 0, maxP99 = 0;
    for (const r of rows) {
      totalSpans += r.spanCount;
      totalErrs += r.errorCount;
      wAvg += r.avgDurationMs * r.spanCount;
      wApdex += (r.apdex ?? 0) * r.spanCount;
      if (r.p99DurationMs > maxP99) maxP99 = r.p99DurationMs;
    }
    return {
      spans: totalSpans, errs: totalErrs,
      errorRate: totalSpans > 0 ? (totalErrs / totalSpans) * 100 : 0,
      avgMs: totalSpans > 0 ? wAvg / totalSpans : 0,
      p99Ms: maxP99,
      apdex: totalSpans > 0 ? wApdex / totalSpans : 0,
    };
  }, [rows]);

  const toggleSort = (col: OpSortKey) => {
    if (sortBy === col) setSortDir(d => d === 'desc' ? 'asc' : 'desc');
    else { setSortBy(col); setSortDir(OP_NATURAL_DIR[col]); }
  };

  if (rows.length === 0) {
    return (
      <div style={{ marginTop: 18 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>⊙ Operations</h3>
        <div className="empty" style={{ padding: 30 }}>No operations seen in this window</div>
      </div>
    );
  }

  return (
    <div style={{ marginTop: 18 }}>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {rows.length} distinct span name{rows.length === 1 ? '' : 's'} in {service}
        </span>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <OpSortTh col="name"      label="Operation"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
              <OpSortTh col="spanCount" label="Calls"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="errorRate" label="Err %"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="avg"       label="Avg"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <th style={{ textAlign: 'right' }}>P50</th>
              <th style={{ textAlign: 'right' }}>P95</th>
              <OpSortTh col="p99"       label="P99"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <OpSortTh col="apdex"     label="Apdex"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
            </tr>
          </thead>
          <tbody>
            {agg && (
              <tr className="agg-row">
                <td><span style={{ fontWeight: 700 }}>All ({rows.length})</span></td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(agg.spans)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  <span className={`badge b-${agg.errorRate > 5 ? 'err' : agg.errorRate > 0 ? 'warn' : 'ok'}`}>
                    {agg.errorRate.toFixed(2)}%
                  </span>
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{agg.avgMs.toFixed(1)}ms</td>
                <td className="mono" style={{ textAlign: 'right', color: 'var(--text3)' }}>—</td>
                <td className="mono" style={{ textAlign: 'right', color: 'var(--text3)' }}>—</td>
                <td className="mono" style={{ textAlign: 'right' }}>{agg.p99Ms.toFixed(1)}ms</td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  {isFinite(agg.apdex) ? agg.apdex.toFixed(2) : '—'}
                </td>
              </tr>
            )}
            {sorted.map(op => {
              const errCls = op.errorRate > 5 ? 'err' : op.errorRate > 0 ? 'warn' : 'ok';
              return (
                <tr key={op.name}>
                  <td>
                    <Link
                      href={opHref(op.name)}
                      style={{ fontWeight: 500 }}
                      title="Open this operation in Explore — service + name pre-filtered"
                    >{op.name}</Link>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(op.spanCount)}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <span className={`badge b-${errCls === 'err' ? 'err' : errCls === 'warn' ? 'warn' : 'ok'}`}>
                      {op.errorRate.toFixed(2)}%
                    </span>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.avgDurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p50DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p95DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p99DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    {isFinite(op.apdex) ? op.apdex.toFixed(2) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function OpSortTh({ col, label, sort, dir, onSort, align }: {
  col: OpSortKey; label: string;
  sort: OpSortKey; dir: 'asc' | 'desc';
  onSort: (c: OpSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
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
