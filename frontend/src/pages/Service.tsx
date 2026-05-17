import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceStructure } from '@/components/ServiceStructure';
import { ServiceCharts } from '@/components/ServiceCharts';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { ServiceCatalogPill } from '@/components/ServiceCatalogPill';
import { SamplingChip } from '@/components/SamplingChip';
import { TopologyMuteChip } from '@/components/TopologyMuteChip';
import { DBQueriesPanel } from '@/components/DBQueriesPanel';
import { DeployHistoryPanel } from '@/components/DeployHistoryPanel';
import { ServiceNeighbors } from '@/components/ServiceNeighbors';
import { ServiceInfra } from '@/components/ServiceInfra';
import { Sparkline } from '@/components/Sparkline';
import { SpanBreakdownChart } from '@/components/SpanBreakdownChart';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { encodeFilters, encodeRange, buildQuery } from '@/lib/urlState';
import { useQueryClient } from '@tanstack/react-query';
import { keys } from '@/lib/queries/keys';
import type { Service, Problem, TimeRange, OperationSummary } from '@/lib/types';

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '10m': '10m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

function ServiceDetailInner() {
  const [searchParams] = useSearchParams();
  const svc = searchParams.get('name') ?? '';

  const queryClient = useQueryClient();
  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [info, setInfo] = useState<Service | null>(null);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [operations, setOperations] = useState<OperationSummary[]>([]);
  const [loading, setLoading] = useState(true);

  // Memoize the absolute window so JSX-level reads (passed as
  // fromNs/toNs props to child fetchers) don't change identity
  // on every render — without this, a relative range like
  // { preset: '15m' } evaluates a fresh now() each paint and
  // the children's useEffect([fromNs, toNs, …]) deps thrash
  // into an infinite refetch.
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    if (!svc) return;
    setLoading(true);
    const r = timeRangeToNs(range);
    // Single bundled fetch — the backend fans out KPI lookup,
    // problems list, operations table, and deploy markers to
    // CH in parallel goroutines and ships one JSON response,
    // cached 60s with SWR. At billion-span scale: 4 network
    // round trips + 4 serial CH cold-cache costs collapse
    // into 1 round trip and 1 parallel CH window. Heatmap /
    // RED charts / cluster breakdown stay separate; they
    // own their own time-window semantics (compare period,
    // lazy mount) the bundle can't bake in without coupling.
    //
    // Bundle's deploys slot is hand-seeded into the React
    // Query cache under the deploys-forService key, so the
    // ServiceCharts component's useServiceDeploys hook
    // resolves instantly (cache HIT) instead of firing its
    // own /api/services/.../deploys round trip below the
    // fold. Same window the bundle is for.
    api.serviceBundle(svc, r)
      .then(b => {
        setInfo(b?.service ?? null);
        setProblems(b?.problems ?? []);
        setOperations(b?.operations ?? []);
        if (b?.deploys) {
          queryClient.setQueryData(
            keys.deploys.forService(svc, r.from ?? 0, r.to ?? 0),
            b.deploys,
          );
        }
      })
      .catch(() => {
        setInfo(null); setProblems([]); setOperations([]);
      })
      .finally(() => setLoading(false));
  }, [svc, range, queryClient]);

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
              <SamplingChip service={svc} spanCount={info.spanCount} range={range} />
              <TopologyMuteChip service={svc} />
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
          {/* Profiling deep-link (v0.5.161). Opens /profiling
              pre-filtered to this service so the operator goes
              from "latency looks weird" → flamegraph in one
              hop. Always shown — if profiling isn't wired up
              yet, the page surfaces a setup-recipes CTA. */}
          <Link to={`/profiling?service=${encodeURIComponent(svc)}`} style={{
            fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }} title="Continuous profiling — CPU + heap flamegraphs for this service">
            🔥 Profiles
          </Link>
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
            {/* Deploy history (v0.5.189) — continuous benchmarking.
                Renders only when the service emits service.version;
                self-hides if there are no deploys in the lookback
                window so it doesn't add visual weight for
                un-versioned services. */}
            <DeployHistoryPanel service={svc} />
            <ServiceInfra     service={svc} since={SINCE_MAP[range.preset] ?? '15m'} />
            <ServiceNeighbors service={svc} since={SINCE_MAP[range.preset] ?? '1h'} />
            <ServiceStructure service={svc} since={SINCE_MAP[range.preset] ?? '1h'} />
            {/* Per-cluster RED breakdown. Renders only when the
                service has traffic from 2+ k8s/openshift clusters.
                Lets an operator spot "same service, different
                performance per cluster" without a per-cluster
                pivot in Explore. */}
            <ServiceClusterBreakdown service={svc} range={range} />
            {/* Span breakdown — Elastic-APM "where does this
                service spend its time?" stacked-area view sits
                above the DB / operations tables so the operator
                gets the categorical answer first, then drills
                down into the rows. */}
            <SpanBreakdownChart service={svc}
                                fromNs={rangeNs.from}
                                toNs={rangeNs.to} />
            <DBQueriesPanel   service={svc}
                              from={rangeNs.from}
                              to={rangeNs.to} />
            <OperationsTable service={svc} rows={operations} range={range} />
            {/* Latency heatmap (Honeycomb-style 2D density).
                Reveals multi-modal distributions the P50/P99
                line charts hide — "P99 is fine but 5% of
                users hit the slow band" reads instantly off
                this grid. Toggleable so operators who don't
                find the view useful can collapse it without
                paying the load. */}
            <ServiceLatencyHeatmap service={svc} range={range} />
            {/* Compact RED time-series at the bottom — RPS,
                error rate, P99 latency. SLOs paint threshold
                lines automatically; deploys paint vertical
                markers; sync cursors keep the three panels
                in lockstep. */}
            <ServiceCharts service={svc} range={range}
              onZoom={(fromUnixSec, toUnixSec) => {
                // Dynatrace-style drag-to-zoom on any RED
                // panel — selecting a range narrows the
                // page's TimeRange so every chart + the
                // operations table re-fetch for that window.
                // Same pattern v0.5.23 wired on dashboards.
                setRange({
                  preset: 'custom',
                  fromMs: Math.round(fromUnixSec * 1000),
                  toMs: Math.round(toUnixSec * 1000),
                });
              }} />
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

// ServiceClusterBreakdown — sortable RED stats per cluster the
// service emitted spans from. Renders silently when there's
// only one cluster (or none), so single-cluster operators don't
// see noise. Click a row to pivot to /services?cluster=<name>
// scoped to that one cluster. The sort defaults to spanCount
// desc so the heaviest cluster lands at top — usually what an
// operator triaging "is this service slow?" wants first.
type ClusterSortKey = 'cluster' | 'calls' | 'errRate' | 'avg' | 'p99';
function ServiceClusterBreakdown({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const [data, setData] = useState<import('@/lib/types').ServiceClusterStat[] | null | undefined>(undefined);
  const [sortBy, setSortBy] = useState<ClusterSortKey>('calls');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.serviceClusters(service, from, to)
      .then(r => setData(r?.clusters ?? []))
      .catch(() => setData(null));
  }, [service, range]);

  const sorted = useMemo(() => {
    if (!data) return data;
    const arr = [...data];
    arr.sort((a, b) => {
      switch (sortBy) {
        case 'cluster': return a.cluster.localeCompare(b.cluster);
        case 'calls':   return a.spanCount - b.spanCount;
        case 'errRate': return a.errorRate - b.errorRate;
        case 'avg':     return a.avgDurationMs - b.avgDurationMs;
        case 'p99':     return a.p99DurationMs - b.p99DurationMs;
      }
    });
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [data, sortBy, sortDir]);

  // Silent when fewer than 2 clusters — single-cluster (or
  // zero-cluster, e.g. SDK without resource attrs) deployments
  // don't need the panel. Loading / error states stay quiet
  // for the same reason.
  if (!sorted || sorted.length < 2) return null;

  const toggleSort = (col: ClusterSortKey) => {
    if (sortBy === col) {
      setSortDir(d => (d === 'desc' ? 'asc' : 'desc'));
    } else {
      setSortBy(col);
      setSortDir(col === 'cluster' ? 'asc' : 'desc');
    }
  };

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Per-cluster breakdown <span style={{
          fontWeight: 400, color: 'var(--text3)', textTransform: 'none',
        }}>· {sorted.length} clusters</span>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <ClusterTh col="cluster" label="Cluster" sort={sortBy} dir={sortDir} onSort={toggleSort} />
              <ClusterTh col="calls"   label="Calls"   sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <ClusterTh col="errRate" label="Err %"   sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <ClusterTh col="avg"     label="Avg"     sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              <ClusterTh col="p99"     label="P99"     sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
            </tr>
          </thead>
          <tbody>
            {sorted.map(c => {
              const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
              return (
                <tr key={c.cluster}>
                  <td>
                    <Link to={`/services?cluster=${encodeURIComponent(c.cluster)}`}
                          style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}
                          title={`Filter /services to cluster ${c.cluster}`}>
                      {c.cluster}
                    </Link>
                  </td>
                  <td className="num mono">{fmtNum(c.spanCount)}</td>
                  <td className="num mono">
                    <span className={`badge b-${errCls}`}>{c.errorRate.toFixed(2)}%</span>
                  </td>
                  <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                  <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ServiceLatencyHeatmap fetches the heatmap for the current
// service + window and renders it under a collapsible
// section. Uses the existing /api/spans/heatmap endpoint
// with a single service_name filter — that endpoint already
// uses the primary-key partition prune so this is cheap
// even on a 24h window.
function ServiceLatencyHeatmap({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const [data, setData] = useState<import('@/lib/types').LatencyHeatmap | null | undefined>(undefined);
  // Same service usually runs across multiple clusters
  // simultaneously (eu-west + eu-central + us-east as one
  // logical fleet). We load the cluster set from the
  // per-service breakdown endpoint that already powers the
  // table above and let the operator pivot the heatmap to
  // any single cluster — or "All clusters" (the merged
  // distribution, default) which is the union view a
  // global-latency owner cares about.
  const [clusters, setClusters] = useState<string[]>([]);
  const [picked, setPicked] = useState<string>(''); // '' = all
  // Collapse state — defaults open. Persisted to localStorage
  // so an operator who'd rather hide the panel doesn't fight
  // it on every reload. Keyed globally (not per-service) so
  // the preference is a one-time setting.
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try { return localStorage.getItem('svc.heatmap.collapsed') === '1'; }
    catch { return false; }
  });

  // Lazy-mount gate — the heatmap is below the RED chart row
  // and the operations table, so on a wide enough viewport
  // it's often only seen after a scroll. Until the section
  // has been visible at least once (or sits within 200px of
  // the viewport), the spanHeatmap fetch is deferred. Keeps
  // the cold initial render from competing for CH bandwidth
  // with the above-the-fold queries.
  //
  // Sticky: once the operator has seen the heatmap, we leave
  // hasBeenVisible=true so subsequent scrolls / re-mounts
  // don't toggle it back to "deferred" and re-skip fetches.
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [hasBeenVisible, setHasBeenVisible] = useState(false);
  useEffect(() => {
    if (hasBeenVisible) return;
    if (!containerRef.current) return;
    if (typeof IntersectionObserver === 'undefined') {
      // Old browser fallback — eagerly mount, same behaviour
      // as before lazy-mount existed.
      setHasBeenVisible(true);
      return;
    }
    const io = new IntersectionObserver(entries => {
      for (const e of entries) {
        if (e.isIntersecting) {
          setHasBeenVisible(true);
          io.disconnect();
          break;
        }
      }
    }, {
      // Start fetching 200px before the panel enters the
      // viewport so the data is usually ready by the time
      // the operator reads its top edge.
      rootMargin: '200px',
    });
    io.observe(containerRef.current);
    return () => io.disconnect();
  }, [hasBeenVisible]);

  // Pull the cluster set whenever the service or window
  // changes. Cheap (cached server-side 30s) and keeps the
  // dropdown in sync with whatever traffic is in the window.
  // Gated on visibility too — no point loading the cluster
  // list for a panel the operator hasn't scrolled to.
  useEffect(() => {
    if (!hasBeenVisible || collapsed) return;
    const { from, to } = timeRangeToNs(range);
    api.serviceClusters(service, from, to)
      .then(r => {
        const names = (r?.clusters ?? []).map(c => c.cluster);
        setClusters(names);
        // If the previously-picked cluster vanished from the
        // window (e.g. window moved past its traffic), drop
        // back to "All" instead of querying for nothing.
        if (picked && !names.includes(picked)) setPicked('');
      })
      .catch(() => setClusters([]));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [service, range, hasBeenVisible, collapsed]);

  useEffect(() => {
    if (collapsed || !hasBeenVisible) return;
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    const f: { key: string; op: string; value: string }[] = [
      { key: 'service.name', op: '=', value: service },
    ];
    if (picked) {
      // Hit the resource-attr key directly. The OTLP ingest
      // path materialises k8s.cluster.name as a span attr,
      // so a single predicate is enough (no coalesce across
      // resource + span attrs needed at query time).
      f.push({ key: 'k8s.cluster.name', op: '=', value: picked });
    }
    api.spanHeatmap({ from, to, filters: JSON.stringify(f), buckets: 60 })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [service, range, collapsed, picked, hasBeenVisible]);

  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    try { localStorage.setItem('svc.heatmap.collapsed', next ? '1' : '0'); }
    catch { /* private browsing — best-effort only */ }
  };

  return (
    <div ref={containerRef} style={{ marginTop: 24, marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6,
      }}>
        <button type="button" onClick={toggle}
          style={{
            all: 'unset', cursor: 'pointer',
            fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.4,
            display: 'inline-flex', alignItems: 'center', gap: 6,
          }}
          title={collapsed ? 'Expand' : 'Collapse'}>
          <span style={{ color: 'var(--text3)' }}>{collapsed ? '▸' : '▾'}</span>
          Latency distribution
        </button>
        {!collapsed && clusters.length >= 2 && (
          <select value={picked}
            onChange={e => setPicked(e.target.value)}
            title="Same service runs across multiple clusters — pivot the heatmap to any single cluster, or stay on the union view."
            style={{ fontSize: 11, padding: '2px 6px', marginLeft: 4 }}>
            <option value="">All clusters ({clusters.length})</option>
            {clusters.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
        )}
        {!collapsed && data && data.maxCount > 0 && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            peak {data.maxCount.toLocaleString()} spans/cell · log-scale y-axis
          </span>
        )}
      </div>
      {!collapsed && (
        <div style={{
          padding: 10, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
        }}>
          {data === undefined && <Spinner />}
          {data === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>
              Failed to load latency distribution.
            </div>
          )}
          {data && (data.maxCount === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              No spans in this window.
            </div>
          ) : (
            <LatencyHeatmap data={data} height={240} />
          ))}
        </div>
      )}
    </div>
  );
}

function ClusterTh({ col, label, sort, dir, onSort, align }: {
  col: ClusterSortKey; label: string;
  sort: ClusterSortKey; dir: 'asc' | 'desc';
  onSort: (c: ClusterSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        style={{ textAlign: align ?? 'left' }}
        aria-sort={active ? (dir === 'desc' ? 'descending' : 'ascending') : 'none'}>
      <button type="button" onClick={() => onSort(col)}
        style={{
          all: 'unset', display: 'inline-flex', alignItems: 'baseline',
          gap: 4, width: '100%', cursor: 'pointer',
          justifyContent: align === 'right' ? 'flex-end' : 'flex-start',
        }}>
        {label}
        <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
      </button>
    </th>
  );
}
