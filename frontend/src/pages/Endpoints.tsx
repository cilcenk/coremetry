import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { Sparkline } from '@/components/Sparkline';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Modal } from '@/components/ui';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { encodeRange } from '@/lib/urlState';
import type { EndpointRow, TimeRange, SpanMetricSeries } from '@/lib/types';

// /endpoints — operator-asked v0.5.365. Cross-service inbound
// RED rollup keyed on http.route (templated) with url.path /
// http.target fallbacks. Backend resolves the priority chain
// per row so this page only deals with the resolved string.
// Mirrors the /services list ergonomics: search + service
// filter + sortable columns + drill-throughs into /traces and
// /service detail.

type SortKey = 'service' | 'path' | 'calls' | 'errors' | 'errorRate' | 'avgMs' | 'p99Ms';

export default function EndpointsPage() {
  const [params, setParams] = useSearchParams();
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [rows, setRows] = useState<EndpointRow[] | null | undefined>(undefined);
  const [search, setSearch] = useState(() => params.get('search') ?? '');
  const [clusterOptions, setClusterOptions] = useState<string[]>([]);
  const cluster = params.get('cluster') ?? '';
  const setCluster = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('cluster', v); else next.delete('cluster');
    return next;
  }, { replace: true });
  const service = params.get('service') ?? '';
  const setService = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('service', v); else next.delete('service');
    return next;
  }, { replace: true });
  const setSearchParam = (v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('search', v); else next.delete('search');
    return next;
  }, { replace: true });

  const [sortKey, setSortKey] = useState<SortKey>('calls');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');
  // v0.5.389 — limit lifted to 2000 default, with an explicit
  // "load top 5000" toggle so the operator can pull the long
  // tail when they need it. Backend caps at 5000; values above
  // that get clamped server-side.
  const [limit, setLimit] = useState<number>(2000);
  // v0.5.387 — sparkline-click drill-in. Holds the row whose
  // trend was clicked; modal renders the three RED sparklines
  // (calls / errors / p99) side-by-side with their summary
  // stats so the operator confirms "is this endpoint spiky" at
  // a glance, then drills further via the same "view traces"
  // link the table row already exposes.
  const [detail, setDetail] = useState<EndpointRow | null>(null);

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    setRows(undefined);
    api.endpoints({
      from, to,
      service: service || undefined,
      search: search.trim() || undefined,
      cluster: cluster || undefined,
      limit,
    })
      .then(r => setRows(r ?? []))
      .catch(() => setRows(null));
  }, [from, to, service, search, cluster, limit]);

  // Cluster picker options — mirror Services page so symmetry is
  // intuitive for operators landing here after filtering there.
  useEffect(() => {
    api.clusters(from, to)
      .then(r => setClusterOptions(r?.clusters ?? []))
      .catch(() => setClusterOptions([]));
  }, [from, to]);

  const sorted = useMemo(() => {
    const list = rows ?? [];
    const arr = [...list].sort((a, b) => {
      const dir = sortDir === 'asc' ? 1 : -1;
      switch (sortKey) {
        case 'service':   return dir * a.service.localeCompare(b.service);
        case 'path':      return dir * a.path.localeCompare(b.path);
        case 'calls':     return dir * (a.calls - b.calls);
        case 'errors':    return dir * (a.errors - b.errors);
        case 'errorRate': return dir * (a.errorRate - b.errorRate);
        case 'avgMs':     return dir * (a.avgMs - b.avgMs);
        case 'p99Ms':     return dir * (a.p99Ms - b.p99Ms);
      }
    });
    return arr;
  }, [rows, sortKey, sortDir]);

  const setSort = (k: SortKey) => {
    if (k === sortKey) setSortDir(d => d === 'asc' ? 'desc' : 'asc');
    else { setSortKey(k); setSortDir(k === 'service' || k === 'path' ? 'asc' : 'desc'); }
  };

  const totalCalls = (rows ?? []).reduce((s, r) => s + r.calls, 0);
  const totalErrors = (rows ?? []).reduce((s, r) => s + r.errors, 0);
  const totalErrorRate = totalCalls > 0 ? (totalErrors / totalCalls) * 100 : 0;

  return (
    <>
      <Topbar title="Endpoints" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
          <ServicePicker value={service} onChange={setService}
            placeholder="All services…" width={200} />
          <input value={search}
            onChange={e => { setSearch(e.target.value); setSearchParam(e.target.value); }}
            placeholder="Filter by path (substring)…"
            style={{ width: 280, padding: '5px 10px', fontSize: 12,
                     background: 'var(--bg)', color: 'var(--text)',
                     border: '1px solid var(--border)', borderRadius: 4 }} />
          {/* Cluster filter — same source as the Services page so
              an operator who picked a cluster there sees a
              symmetric set here. Hidden when no cluster signal
              is present in the window (resource attr keys
              k8s.cluster.name / openshift.cluster.name / cluster
              unset across all spans). */}
          {clusterOptions.length > 0 && (
            <select value={cluster}
              onChange={e => setCluster(e.target.value)}
              style={{ minWidth: 160 }}
              title={`${clusterOptions.length} cluster${clusterOptions.length === 1 ? '' : 's'} detected`}>
              <option value="">All clusters ({clusterOptions.length})</option>
              {clusterOptions.map(c => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {rows && (
              <>
                Top {fmtNum(rows.length)} by calls
                {rows.length >= limit && (
                  <span style={{ color: 'var(--warn)', marginLeft: 6 }}
                        title="Result hit the limit — long-tail endpoints may be hidden">
                    (capped)
                  </span>
                )}
              </>
            )}
          </span>
          {/* Limit dropdown — operator pulls the long tail when
              the table is at-cap and they want to see the lower-
              traffic endpoints. Capped at 5000 server-side. */}
          <select value={limit}
            onChange={e => setLimit(Number(e.target.value))}
            style={{ fontSize: 12 }}
            title="Maximum rows returned by the backend">
            <option value={500}>top 500</option>
            <option value={1000}>top 1000</option>
            <option value={2000}>top 2000</option>
            <option value={5000}>top 5000</option>
          </select>
        </div>

        {rows === undefined && <Spinner />}
        {rows === null && (
          <Empty icon="⚠" title="Failed to load endpoints">
            The backend /api/endpoints request errored.
          </Empty>
        )}
        {rows && rows.length === 0 && (
          <Empty icon="∅" title="No endpoints in window">
            <div style={{ fontSize: 12, color: 'var(--text2)', maxWidth: 520, marginTop: 8, lineHeight: 1.5 }}>
              No spans with <code>http.route</code> / <code>url.path</code> / <code>http.target</code> attrs
              landed in this window. Try widening the time range, or check
              that your services emit one of those attributes on server-kind spans.
            </div>
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
              gap: 12, marginBottom: 14,
            }}>
              <KPI label="Endpoints" value={fmtNum(rows.length)} />
              <KPI label="Total calls" value={fmtNum(totalCalls)} />
              <KPI label="Errors" value={fmtNum(totalErrors)}
                   sub={`${totalErrorRate.toFixed(2)}%`}
                   cls={totalErrorRate >= 5 ? 'err' : totalErrorRate >= 1 ? 'warn' : ''} />
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <SortHeader k="service"   label="Service"    cur={sortKey} dir={sortDir} onSort={setSort} />
                    <SortHeader k="path"      label="Path"       cur={sortKey} dir={sortDir} onSort={setSort} />
                    <th>Method</th>
                    <SortHeader k="calls"     label="Calls"      cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="errors"    label="Errors"     cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="errorRate" label="Error rate" cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="avgMs"     label="Avg"        cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="p99Ms"     label="P99"        cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <th>Trend</th>
                    <th>Traces</th>
                  </tr>
                </thead>
                <tbody>
                  {sorted.map((r, i) => {
                    const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                    return (
                      <tr key={`${r.service}|${r.path}|${i}`}
                          style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                        <td>
                          <Link to={`/services?name=${encodeURIComponent(r.service)}`}
                                style={{ fontFamily: 'monospace', fontSize: 12 }}>
                            {r.service}
                          </Link>
                        </td>
                        <td className="mono" style={{ fontSize: 12, wordBreak: 'break-all' }} title={r.path}>
                          {r.path}
                        </td>
                        <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                          {r.method || '—'}
                        </td>
                        <td className="num mono">{fmtNum(r.calls)}</td>
                        <td className="num mono">{fmtNum(r.errors)}</td>
                        <td className="num mono">
                          <span className={`badge ${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                        </td>
                        <td className="num mono">{r.avgMs.toFixed(1)} ms</td>
                        <td className="num mono">{r.p99Ms.toFixed(0)} ms</td>
                        <td>
                          <button
                            type="button"
                            onClick={() => setDetail(r)}
                            title="Click for calls / errors / p99 detail"
                            style={{
                              background: 'transparent', border: 0, padding: 0,
                              cursor: 'pointer', display: 'inline-block',
                            }}
                          >
                            <Sparkline values={r.sparkline ?? []}
                              width={100} height={22}
                              color={r.errorRate >= 5 ? 'var(--err)' : r.errorRate >= 1 ? 'var(--warn)' : undefined}
                              title={`${r.calls.toLocaleString()} calls — click for detail`} />
                          </button>
                        </td>
                        <td>
                          {/* /traces filter on (service, search=path).
                              The search field matches span.name OR
                              attrs; combined with rootOnly=false and
                              the service filter, this returns every
                              trace that includes a call on this
                              endpoint. */}
                          <Link to={tracesLink(r, range)}
                                style={{ fontSize: 11, color: 'var(--accent2)' }}>
                            view →
                          </Link>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
              Path source priority: <code>http.route</code> (templated) →
              {' '}<code>url.path</code> → <code>http.target</code>.
              Server / consumer spans only — outbound client spans count under
              the callee's row.
            </div>
          </>
        )}
        <EndpointMetricModal row={detail} onClose={() => setDetail(null)} range={range} />
      </div>
    </>
  );
}

// EndpointMetricModal — opens on sparkline click. Renders the
// three RED dimensions (calls, errors, p99) as full uPlot
// time-axis charts so the operator can read tick marks, hover
// for exact values, and visually correlate spikes across all
// three at the same instant via syncKey-linked crosshairs.
// Deep links into /traces and the service detail page close
// the metric → trace loop on the (service, path) tuple.
//
// v0.5.391 — upgraded from three bare Sparklines to MultiLineChart
// instances so the metric view answers "what was happening at
// 14:23" not just "is this endpoint spiky-shaped". Time axis,
// crosshair sync, and tooltip per series — the same uPlot
// affordances the Metrics / Explore pages use.
function EndpointMetricModal({
  row, onClose, range,
}: { row: EndpointRow | null; onClose: () => void; range: TimeRange }) {
  // Hooks must run unconditionally — call useMemo before any
  // early return (React rules-of-hooks).
  const series = useMemo(() => {
    if (!row) return { calls: [], errors: [], p99: [] };
    return bucketsToSeries(row, range);
  }, [row, range]);

  if (!row) return <Modal open={false} onClose={onClose} />;
  const peakCalls = (row.sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const totalErrs = (row.errorsSparkline ?? []).reduce((s, v) => s + v, 0);
  const maxP99 = (row.p99Sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const errCls = row.errorRate >= 5 ? 'err' : row.errorRate >= 1 ? 'warn' : '';
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span className="mono" style={{ fontSize: 13 }}>
          {row.method ? row.method + ' ' : ''}{row.path}
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            ({row.service})
          </span>
        </span>
      }
    >
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 12, marginBottom: 14,
      }}>
        <MetricTile
          label="Calls"
          big={fmtNum(row.calls)}
          sub={`peak ${fmtNum(peakCalls)} / bucket`}
          series={series.calls}
        />
        <MetricTile
          label="Errors"
          big={fmtNum(row.errors)}
          sub={`${row.errorRate.toFixed(2)}% rate`}
          subCls={errCls}
          series={series.errors}
        />
        <MetricTile
          label="P99 latency"
          big={`${row.p99Ms.toFixed(0)} ms`}
          sub={`peak ${maxP99.toFixed(0)} ms · avg ${row.avgMs.toFixed(0)} ms`}
          series={series.p99}
          unit="ms"
        />
      </div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
        Hover any chart to read the bucket value; the crosshair
        syncs across all three so you can correlate calls /
        errors / p99 at the same instant. Total errors in window:
        {' '}<strong>{fmtNum(totalErrs)}</strong>.
      </div>
      <div style={{ display: 'flex', gap: 10 }}>
        <Link
          to={tracesLink(row, range)}
          style={{ fontSize: 12, color: 'var(--accent2)' }}
        >
          View traces →
        </Link>
        <Link
          to={`/services?name=${encodeURIComponent(row.service)}`}
          style={{ fontSize: 12, color: 'var(--accent2)' }}
        >
          Service detail →
        </Link>
      </div>
    </Modal>
  );
}

function MetricTile({
  label, big, sub, subCls, series, unit,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
}) {
  return (
    <div style={{
      padding: '10px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 600, marginBottom: 2 }}>{big}</div>
      <div style={{
        fontSize: 11, marginBottom: 8,
        color: subCls === 'err' ? 'var(--err)' : subCls === 'warn' ? 'var(--warn)' : 'var(--text3)',
      }}>{sub}</div>
      {series.length > 0 && series[0].points.length > 0 ? (
        <MultiLineChart
          series={series}
          unit={unit}
          height={140}
          syncKey="endpoints-detail"
        />
      ) : (
        <div style={{
          height: 140, display: 'flex', alignItems: 'center',
          justifyContent: 'center', color: 'var(--text3)', fontSize: 11,
        }}>no data in window</div>
      )}
    </div>
  );
}

// bucketsToSeries converts the row's three 30-bucket sparkline
// arrays into SpanMetricSeries shape so MultiLineChart can plot
// them on a time axis. The backend doesn't ship per-bucket
// timestamps (the payload size is bounded that way) so we
// reconstruct them client-side from the page's selected range
// — bucket i sits at the midpoint of its slice of the window.
// Matches the bucketing the backend's per_bucket CTE uses
// (intDiv(time - from, bucketNs)).
function bucketsToSeries(row: EndpointRow, range: TimeRange): {
  calls: SpanMetricSeries[]; errors: SpanMetricSeries[]; p99: SpanMetricSeries[];
} {
  const { from, to } = timeRangeToNs(range);
  const calls = row.sparkline ?? [];
  const errs = row.errorsSparkline ?? [];
  const p99s = row.p99Sparkline ?? [];
  const n = Math.max(calls.length, errs.length, p99s.length);
  if (n === 0 || to <= from) {
    return { calls: [], errors: [], p99: [] };
  }
  const bucketNs = (to - from) / n;
  const timeAtBucket = (i: number) => from + bucketNs * i + bucketNs / 2;
  const buildPoints = (arr: number[]) => arr.map((v, i) => ({
    time: timeAtBucket(i),
    value: v,
  }));
  return {
    calls: calls.length ? [{ groupKey: ['calls'], points: buildPoints(calls) }] : [],
    errors: errs.length ? [{ groupKey: ['errors'], points: buildPoints(errs) }] : [],
    p99: p99s.length ? [{ groupKey: ['p99 ms'], points: buildPoints(p99s) }] : [],
  };
}

function tracesLink(r: EndpointRow, range: TimeRange): string {
  return `/traces?service=${encodeURIComponent(r.service)}` +
    `&search=${encodeURIComponent(r.path)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    `&view=list&rootOnly=false`;
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '8px 14px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 2 }}>{label}</div>
      <div style={{
        fontSize: 22, fontWeight: 600,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function SortHeader({ k, label, cur, dir, onSort, num }: {
  k: SortKey; label: string; cur: SortKey; dir: 'asc' | 'desc';
  onSort: (k: SortKey) => void; num?: boolean;
}) {
  const active = cur === k;
  return (
    <th onClick={() => onSort(k)} className={num ? 'num' : ''}
        style={{ cursor: 'pointer', userSelect: 'none' }}>
      <span style={{ color: active ? 'var(--text)' : 'var(--text2)' }}>
        {label}{active ? (dir === 'asc' ? ' ▲' : ' ▼') : ''}
      </span>
    </th>
  );
}
