import { Suspense, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceStructure } from '@/components/ServiceStructure';
import { ServiceCharts } from '@/components/ServiceCharts';
import { ServiceCatalogPill } from '@/components/ServiceCatalogPill';
import { DBQueriesPanel } from '@/components/DBQueriesPanel';
import { ServiceNeighbors } from '@/components/ServiceNeighbors';
import { ServiceInfra } from '@/components/ServiceInfra';
import { Sparkline } from '@/components/Sparkline';
import { SpanBreakdownChart } from '@/components/SpanBreakdownChart';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { encodeFilters, encodeRange, buildQuery } from '@/lib/urlState';
import type { Service, Problem, TimeRange, OperationSummary } from '@/lib/types';

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '10m': '10m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

function ServiceDetailInner() {
  const [searchParams] = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [info, setInfo] = useState<Service | null>(null);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [operations, setOperations] = useState<OperationSummary[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!svc) return;
    setLoading(true);
    const r = timeRangeToNs(range);
    // Upstream / downstream callers + callees were removed per
    // operator request — the ServiceStructure waterfall below shows
    // who-calls-whom in the actual span tree, which is more
    // informative and doesn't depend on peer.service being
    // populated. KPI strip + operations + problems remain.
    Promise.all([
      api.services(r, undefined, svc),
      api.problems({ service: svc, limit: 50 }),
      api.serviceOperations(svc, r),
    ]).then(([all, probs, ops]) => {
      setInfo((all ?? []).find(s => s.name === svc) ?? null);
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
          <Link to="/services" className="sec" style={{
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
          <Link to={`/service/backtrace?name=${encodeURIComponent(svc)}`} style={{
            marginLeft: 'auto', fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }} title="Inbound callers — service / pod / IP backtrace">↩ Backtrace</Link>
          <Link to={`/traces?service=${encodeURIComponent(svc)}`} style={{
            fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }}>⋮ View traces</Link>
        </div>
        {/* Service catalog metadata — owner team / oncall /
            runbook / repo. Operator-curated; falls back to a
            single "+ Add catalog metadata" CTA when empty.
            Editor+ role can edit inline. */}
        <div style={{ marginBottom: 12 }}>
          <ServiceCatalogPill service={svc} />
        </div>

        {openProbs.length > 0 && (
          <div style={{
            border: '1px solid rgba(255,82,82,.4)',
            background: 'rgba(255,82,82,.06)',
            borderRadius: 6, padding: 12, marginBottom: 14,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
              <span style={{ color: 'var(--err)', fontWeight: 600 }}>
                ! {openProbs.length} open problem{openProbs.length === 1 ? '' : 's'} on {svc}
              </span>
              <span style={{ flex: 1 }} />
              <Link to={`/problems?service=${encodeURIComponent(svc)}`} style={{ fontSize: 11 }}>
                View all for this service →
              </Link>
            </div>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 8,
            }}>
              {openProbs.map(p => {
                const sevCls = p.severity === 'critical' ? 'b-err' : 'b-warn';
                return (
                  <div key={p.id} style={{
                    padding: 8, borderRadius: 4,
                    background: 'var(--bg2)', border: '1px solid var(--border)',
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
                      <span className={`badge ${sevCls}`} style={{ fontSize: 10 }}>
                        {p.severity.toUpperCase()}
                      </span>
                      <span style={{ fontSize: 12, fontWeight: 600 }}>{p.ruleName}</span>
                    </div>
                    <div style={{ fontSize: 11, color: 'var(--text2)' }}>
                      <span style={{ fontFamily: 'monospace' }}>{p.metric}</span>
                      {' = '}
                      <b style={{ color: 'var(--err)' }}>{Number(p.value).toFixed(2)}</b>
                      {' '}(threshold {Number(p.threshold).toFixed(2)})
                    </div>
                    {p.description && (
                      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                        {p.description}
                      </div>
                    )}
                    <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 4, fontFamily: 'monospace' }}>
                      since {new Date(p.startedAt / 1e6).toLocaleString()}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {loading && <Spinner />}
        {!loading && (
          <>
            <ServiceInfra     service={svc} since={SINCE_MAP[range.preset] ?? '15m'} />
            <ServiceNeighbors service={svc} since={SINCE_MAP[range.preset] ?? '1h'} />
            <ServiceStructure service={svc} since={SINCE_MAP[range.preset] ?? '1h'} />
            {/* Span breakdown — Elastic-APM "where does this
                service spend its time?" stacked-area view sits
                above the DB / operations tables so the operator
                gets the categorical answer first, then drills
                down into the rows. */}
            <SpanBreakdownChart service={svc}
                                fromNs={timeRangeToNs(range).from}
                                toNs={timeRangeToNs(range).to} />
            <DBQueriesPanel   service={svc}
                              from={timeRangeToNs(range).from}
                              to={timeRangeToNs(range).to} />
            <OperationsTable service={svc} rows={operations} range={range} />
            {/* Compact RED time-series at the bottom — RPS,
                error rate, P99 latency. SLOs paint threshold
                lines automatically; deploys paint vertical
                markers; sync cursors keep the three panels
                in lockstep. */}
            <ServiceCharts service={svc} range={range} />
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
type OpSortKey = 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99' | 'apdex' | 'impact';
const OP_NATURAL_DIR: Record<OpSortKey, 'asc' | 'desc'> = {
  name: 'asc', spanCount: 'desc', errorRate: 'desc',
  avg: 'desc', p99: 'desc', apdex: 'asc', impact: 'desc',
};

// Elastic-APM's "Impact" = avg_duration × count. Surfaces the
// heaviest cumulative consumers — the operation that's slow OR
// runs a lot. A 5ms operation called 100k times shows up; a
// once-an-hour 30s job doesn't. Default sort so the top of the
// table answers "what should I optimise first" without the
// operator combining columns by eye.
function impactOf(r: OperationSummary): number {
  return r.avgDurationMs * r.spanCount;
}

function OperationsTable({ service, rows, range }: { service: string; rows: OperationSummary[]; range: TimeRange }) {
  const [sortBy, setSortBy] = useState<OpSortKey>('impact');
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
        case 'impact':    return impactOf(a) - impactOf(b);
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

  // Element-wise sum of every operation's sparkline → service-wide
  // call-rate trend rendered on the "All" aggregate row. Uses the
  // longest sparkline as the canvas length; shorter ones (only one
  // bucket of activity, e.g. a single-call operation) just contribute
  // zeros to the trailing slots.
  const aggSparkline = useMemo(() => {
    let len = 0;
    for (const r of rows) {
      if (r.sparkline && r.sparkline.length > len) len = r.sparkline.length;
    }
    if (len === 0) return [];
    const out = new Array(len).fill(0);
    for (const r of rows) {
      if (!r.sparkline) continue;
      for (let i = 0; i < r.sparkline.length; i++) {
        out[i] += r.sparkline[i];
      }
    }
    return out;
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
      {/* Cap the operations table at 540 px tall and let it
          scroll inside that container — at 500+ operations on
          one service the previous full-height render put the
          KPIs above and the rest of the page sections below
          way out of reach. Sticky thead keeps the column
          labels visible while scrolling. */}
      <div className="table-wrap" style={{ maxHeight: 540, overflowY: 'auto' }}>
        <table>
          <thead style={{ position: 'sticky', top: 0, zIndex: 1, background: 'var(--bg1)' }}>
            <tr>
              <OpSortTh col="name"      label="Operation"  sort={sortBy} dir={sortDir} onSort={toggleSort} />
              <th style={{ textAlign: 'left', width: 92 }}>Trend</th>
              <OpSortTh col="impact"    label="Impact"     sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
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
                <td>
                  {/* Aggregate trend = element-wise sum across all
                      per-operation sparklines so the "All" row
                      shows the service-wide call rate at a glance,
                      using the same window + bucket boundaries as
                      every row beneath it. */}
                  <Sparkline values={aggSparkline} title={`total calls/bucket × ${rows.length} ops`} />
                </td>
                <td className="mono" style={{ textAlign: 'right', fontWeight: 700 }}>
                  {fmtImpact(rows.reduce((n, r) => n + impactOf(r), 0))}
                </td>
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
              // Tone the per-row sparkline with the same severity
              // colour as the err-rate badge so the eye reads "this
              // op is hot" from one glance at the trend column,
              // before reading the numbers.
              const sparkColor = errCls === 'err' ? 'var(--err)'
                              : errCls === 'warn' ? 'var(--warn)'
                              : undefined;
              return (
                <tr key={op.name}>
                  <td>
                    <Link
                      to={opHref(op.name)}
                      style={{ fontWeight: 500 }}
                      title="Open this operation in Explore — service + name pre-filtered"
                    >{op.name}</Link>
                  </td>
                  <td>
                    <Sparkline values={op.sparkline ?? []}
                      color={sparkColor}
                      title={`${fmtNum(op.spanCount)} calls · click row to drill in`} />
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <ImpactBar value={impactOf(op)}
                               max={Math.max(...rows.map(impactOf))} />
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

// ImpactBar renders a horizontal proportion bar + numeric label —
// Elastic APM's signature pattern in the transaction list. Bar
// width = row impact / max impact across the visible rows so the
// busiest operation always fills the cell. Keeps the heaviest
// cumulative consumer visually obvious without forcing the
// operator to read tabular numbers.
function ImpactBar({ value, max }: { value: number; max: number }) {
  const pct = max > 0 ? (value / max) * 100 : 0;
  return (
    <div style={{ position: 'relative', minWidth: 90, display: 'inline-block' }}>
      <div style={{
        position: 'absolute', inset: 0, width: `${pct}%`,
        background: 'rgba(56,139,253,0.12)',
        borderRadius: 3,
      }} />
      <span style={{ position: 'relative', paddingRight: 4 }}>
        {fmtImpact(value)}
      </span>
    </div>
  );
}

// fmtImpact renders impact in time units. Below a second we keep
// ms with at most one decimal; past a second we promote to s/min/h
// so a 30M-ms ops job reads as "8.3h" rather than "30000000".
function fmtImpact(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)}ms`;
  if (ms < 1000) return `${ms.toFixed(1)}ms`;
  const sec = ms / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  const min = sec / 60;
  if (min < 60) return `${min.toFixed(1)}min`;
  const hr = min / 60;
  return `${hr.toFixed(1)}h`;
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


export default function ServiceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ServiceDetailInner />
    </Suspense>
  );
}
