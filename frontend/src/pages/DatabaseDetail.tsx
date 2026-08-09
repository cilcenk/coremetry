import { lazy, Suspense, useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Turtle } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { LazyMount } from '@/components/LazyMount';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { api } from '@/lib/api';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { dbTracesHref } from '@/lib/pivotHref';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { StmtDetailDrawer } from '@/pages/slowqueries/StmtDetailDrawer';
import { decodeStmtParam, encodeStmtParam } from '@/pages/slowqueries/stmtParam';
import { WaitLockStrip, isWaitLockEngine } from '@/features/dependencies/panels/WaitLockStrip';
import { OraclePanel } from '@/features/dependencies/panels/OraclePanel';
import { PostgresPanel } from '@/features/dependencies/panels/PostgresPanel';
import { MySQLPanel } from '@/features/dependencies/panels/MySQLPanel';
import { RedisPanel } from '@/features/dependencies/panels/RedisPanel';
import { parseDatabasePageRef } from '@/pages/databases/databaseParam';
import type {
  DataTableColumn,
} from '@/lib/dataTable';
import type {
  DBCallerBreakdown, DBDetail, DBTrend, SlowQueryRow, SpanMetricSeries,
} from '@/lib/types';

// /database — the full-page database detail (v0.9.840).
//
// WHY A PAGE. The /databases row used to expand into an inline drawer
// squeezed under the table, which is where the caller table, the
// wait/lock strip, the engine panel AND the statement list all had to
// share one 12px-padded cell. Operator's call, same as /endpoint the
// version before: the row click navigates.
//
// NO NEW BACKEND. Every read here already existed behind the drawer:
// /api/databases/detail (the v0.9.821 identity TRIPLE), /trends,
// /waitlock, the four engine panels and /slow-queries. The trends read
// is the page-wide one the table also issues — same params, so React
// Query serves this page from the same cache entry rather than adding
// a per-row parameter to a shared endpoint.
//
// IDENTITY (v0.9.821, the trap): a row is (system, instance, dbName),
// never the printed label. When dbName is empty the page says "every
// database on this instance" out loud, because the drawer's hardest-won
// lesson was that a silent scope reads as a narrow one.
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

export default function DatabaseDetailPage() {
  const [params, setParams] = useSearchParams();
  const search = params.toString();
  const refObj = useMemo(() => parseDatabasePageRef(search), [search]);
  const [env] = useUrlEnv();
  const { range, setRange } = usePageZoomRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);

  const system = refObj?.system ?? '';
  const instance = refObj?.instance ?? '';
  const dbName = refObj?.dbName ?? '';

  const detailQ = useQuery({
    queryKey: ['database-detail', system, instance, dbName, from, to],
    queryFn: () => api.databaseDetail(system, instance, dbName, from, to),
    enabled: !!refObj,
    staleTime: 30_000,
  });

  // The SAME key /databases uses, on purpose: no new parameter on a
  // shared endpoint, and the page inherits the table's warm cache.
  const trendsQ = useQuery({
    queryKey: ['db-trends', from, to],
    queryFn: () => api.dbTrends(from, to),
    enabled: !!refObj,
    staleTime: 30_000,
  });
  const trend: DBTrend | undefined = useMemo(() => {
    if (!refObj) return undefined;
    return (trendsQ.data ?? []).find(t =>
      t.dbSystem === refObj.system
      && t.instance === refObj.instance
      && (t.dbName ?? '') === refObj.dbName);
  }, [trendsQ.data, refObj]);

  const stmtsQ = useQuery({
    queryKey: ['database-statements', system, dbName, from, to],
    queryFn: () => api.slowQueries({
      from, to,
      db_system: system || undefined,
      db_name: dbName || undefined,
      limit: 10,
    }),
    enabled: !!refObj,
    staleTime: 30_000,
  });
  const stmtRef = useMemo(() => decodeStmtParam(params.get('stmt')), [params]);
  const stmtRow = useMemo(
    () => (stmtsQ.data ?? []).find(r => r.stmtHash === stmtRef?.hash),
    [stmtsQ.data, stmtRef]);
  const openStmt = (r: SlowQueryRow) => {
    if (!r.stmtHash) return;
    setParams(prev => {
      const next = new URLSearchParams(prev);
      next.set('stmt', encodeStmtParam({ hash: r.stmtHash!, system: r.dbSystem }));
      return next;
    }, { replace: true });
  };
  const closeStmt = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('stmt');
    next.delete('stmtcmp');
    return next;
  }, { replace: true });

  const series = useMemo(() => trendSeries(trend), [trend]);

  if (!refObj) {
    return (
      <>
        <Topbar title="Database" />
        <div id="content">
          <Empty icon="⚠" title="No database in this link">
            The URL is missing <code>system</code> or <code>instance</code>.
            {' '}<Link to="/databases">Back to Databases →</Link>
          </Empty>
        </div>
      </>
    );
  }

  const d: DBDetail | null | undefined =
    detailQ.isPending ? undefined : detailQ.isError ? null : detailQ.data;
  const engine = refObj.system.toLowerCase();

  return (
    <>
      <Topbar title="Database" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ fontSize: 11.5, color: 'var(--text3)', marginBottom: 8 }}>
          <Link to="/databases">Databases</Link> › database detail
        </div>

        <div style={{
          display: 'flex', alignItems: 'center', gap: 10,
          flexWrap: 'wrap', marginBottom: 4,
        }}>
          <span className="badge b-info" style={{ fontSize: 10, textTransform: 'uppercase' }}>
            {refObj.system}
          </span>
          <span className="mono" style={{ fontSize: 15, fontWeight: 600 }}>
            {refObj.dbName || refObj.instance}
          </span>
          <span className="badge b-gray mono" style={{ fontSize: 10 }}
            title="db_summary_5m resolves the instance through six rungs (peer.service → server.address → net.peer.name → db.host → db.name → service.name); this is whichever one it found.">
            {refObj.instance}
          </span>
          {refObj.source === 'receiver' && (
            <span className="badge b-info" style={{ fontSize: 10 }}
              title="Discovered via an OpenTelemetry database receiver — the engine panel below reads its metrics directly.">
              via receiver
            </span>
          )}
          {env && (
            <span className="badge b-info" style={{ fontSize: 10 }}
              title="The overview this row came from was narrowed to this environment.">
              env={env}
            </span>
          )}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: 12, fontSize: 12 }}>
            <Link to={dbTracesHref({
              window: range, system: refObj.system,
              instance: refObj.instance, dbName: refObj.dbName || undefined,
            })}>Traces →</Link>
            <Link to={`/databases/slow-queries?dbsys=${encodeURIComponent(refObj.system)}`
              + (refObj.dbName ? `&dbname=${encodeURIComponent(refObj.dbName)}` : '')}
              style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
              <Turtle size={13} strokeWidth={1.75} /> Top statements →
            </Link>
          </span>
        </div>

        {/* SCOPE LINE — the drawer's v0.9.821 honesty, carried over. An
            empty db.name is a real state, not a missing value, and it
            means something much wider than the row label suggests. */}
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 12 }}>
          Scope:{' '}
          <span className="mono" style={{ color: 'var(--text2)' }}>
            {refObj.system} / {refObj.instance}
          </span>
          {refObj.dbName
            ? <> · database <span className="mono" style={{ color: 'var(--text2)' }}>{refObj.dbName}</span></>
            : <> · <b>every database</b> on this instance (all db.name values)</>}
        </div>

        {d === undefined && <Spinner />}
        {d === null && (
          <Empty icon="⚠" title="Detail query failed">
            The /api/databases/detail request errored.
            {' '}<Link to="/databases">Back to the overview →</Link>
          </Empty>
        )}

        {d && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(104px, 1fr))',
              gap: 8, marginBottom: 14,
            }}>
              <Stat label="Calls" value={fmtNum(d.spanCount)} />
              <Stat label="Errors" value={fmtNum(d.errorCount)} />
              <Stat label="Err rate" value={`${d.errorRate.toFixed(2)}%`}
                tone={d.errorRate > 5 ? 'err' : d.errorRate > 0 ? 'warn' : undefined} />
              <Stat label="Avg" value={`${d.avgDurationMs.toFixed(1)} ms`} />
              <Stat label="P50" value={msOrDash(d.p50DurationMs)} />
              <Stat label="P95" value={msOrDash(d.p95DurationMs)} />
              <Stat label="P99" value={`${d.p99DurationMs.toFixed(1)} ms`} />
              {/* Total time — the mockup's extra tile, and the number
                  that actually ranks a database against its siblings:
                  calls × avg is the wall-clock this DB cost the fleet
                  inside the window. */}
              <Stat label="Total time"
                value={fmtTotal(d.spanCount * d.avgDurationMs)} />
            </div>

            {/* Three series, from the SAME /api/databases/trends payload
                the overview grid uses — filtered client-side by the
                identity triple. No per-row parameter added to a shared,
                cached endpoint. */}
            <div className="ov-grid ov-charts-3 ov-mb">
              {(['calls', 'errors', 'p99'] as const).map(k => (
                <LazyMount key={k} minHeight={170}>
                  <Suspense fallback={<div style={{ height: 170, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
                    <CorePanelMultiLazy
                      title={SERIES_TITLE[k]}
                      storageKey={`database-detail-${k}`}
                      height={150}
                      unit={SERIES_UNIT[k]}
                      xRange={xRange}
                      // '-ms' = engine namespace (v0.9.789); one group so
                      // the crosshair correlates the three.
                      syncKey="database-detail-ms"
                      emptyReason={series[k].length ? undefined
                        : (trendsQ.isPending ? 'Yükleniyor…'
                          : 'Bu pencerede bu veritabanı için kova yok')}
                      items={[{
                        name: SERIES_TITLE[k],
                        role: k === 'errors' ? 'error' : 'data',
                        series: series[k],
                      }]} />
                  </Suspense>
                </LazyMount>
              ))}
            </div>

            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(340px, 1fr))',
              gap: 12,
            }}>
              <CallersCard callers={d.callers ?? []} />
              {isWaitLockEngine(refObj.system) ? (
                <Card header={<PanelTitle sub="wait classes">Waits &amp; locks</PanelTitle>}>
                  <WaitLockStrip system={refObj.system} instance={refObj.instance} range={range} />
                </Card>
              ) : (
                <Card header={<PanelTitle>Waits &amp; locks</PanelTitle>}>
                  <div style={{ fontSize: 11.5, color: 'var(--text3)', lineHeight: 1.6 }}>
                    <code>{refObj.system}</code> has no wait / lock metric family
                    in its OpenTelemetry receiver, so there is nothing to show —
                    this panel is empty by the engine's design, not by a gap in
                    the window.
                  </div>
                </Card>
              )}
            </div>

            <Card header={
              <PanelTitle sub={refObj.dbName
                ? `narrowed to ${refObj.dbName} · click a row for statement detail`
                : `every database on this instance · click a row for statement detail`}>
                Top statements
              </PanelTitle>
            }>
              {stmtsQ.isPending && <TableSkeleton rows={5} cols={5} wideFirst />}
              {stmtsQ.data && stmtsQ.data.length === 0 && (
                <Empty icon="◷" title="No statement in this window">
                  Nothing matched <code>{refObj.system}</code>
                  {refObj.dbName ? <> / <code>{refObj.dbName}</code></> : null} in the
                  selected range.
                </Empty>
              )}
              {(stmtsQ.data ?? []).length > 0 && (
                <div className="table-wrap">
                  <table style={{ width: '100%', fontSize: 12 }}>
                    <thead><tr>
                      <th style={{ textAlign: 'left' }}>Statement</th>
                      <th style={{ textAlign: 'left', width: 190 }}>Service</th>
                      <th className="num" style={{ width: 80 }}>Calls</th>
                      <th className="num" style={{ width: 88 }}>P95</th>
                      <th className="num" style={{ width: 88 }}>Total</th>
                    </tr></thead>
                    <tbody>
                      {(stmtsQ.data ?? []).map((r, i) => (
                        <tr key={r.stmtHash ?? i}
                          onClick={() => openStmt(r)}
                          title={r.sampleStatement || r.statement}
                          style={{ cursor: r.stmtHash ? 'pointer' : 'default' }}>
                          <td className="mono" style={{
                            maxWidth: 0, overflow: 'hidden',
                            textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                          }}>{r.statement}</td>
                          <td className="mono" style={{
                            maxWidth: 0, overflow: 'hidden',
                            textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                          }} title={r.service}>{r.service}</td>
                          <td className="num mono">{fmtNum(r.count)}</td>
                          <td className="num mono">{r.p95Ms.toFixed(1)} ms</td>
                          <td className="num mono"><b>{(r.totalMs / 1000).toFixed(1)} s</b></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>

            {/* Engine panel — receiver rows ONLY. A span-derived row has
                no receiver behind it, and rendering the panel anyway
                would bleed another instance's engine metrics into a row
                that never had any (the drawer's `source` gate, kept). */}
            {refObj.source === 'receiver' && (
              <>
                {engine === 'oracle' && <OraclePanel instance={refObj.instance} range={range} />}
                {(engine === 'postgresql' || engine === 'postgres') && (
                  <PostgresPanel instance={refObj.instance} range={range} />
                )}
                {(engine === 'mysql' || engine === 'mariadb') && (
                  <MySQLPanel instance={refObj.instance} range={range} />
                )}
                {engine === 'redis' && <RedisPanel instance={refObj.instance} range={range} />}
              </>
            )}
          </>
        )}

        {stmtRef && (
          <StmtDetailDrawer
            refObj={stmtRef}
            row={stmtRow}
            range={range}
            onClose={closeStmt}
          />
        )}
      </div>
    </>
  );
}

const SERIES_TITLE = { calls: 'Calls / s', errors: 'Error %', p99: 'P99 latency' } as const;
const SERIES_UNIT = { calls: 'reqps', errors: 'percent', p99: 'ms' } as const;

/** trendSeries — one DBTrend's buckets into the three CorePanel series.
 *  DBTrendPoint.t is already unix NANOSECONDS (the SpanMetricSeries
 *  contract), so there is no unit conversion here to get wrong. */
function trendSeries(t: DBTrend | undefined): {
  calls: SpanMetricSeries[]; errors: SpanMetricSeries[]; p99: SpanMetricSeries[];
} {
  const pts = t?.points ?? [];
  if (pts.length === 0) return { calls: [], errors: [], p99: [] };
  const build = (pick: (p: typeof pts[number]) => number, label: string): SpanMetricSeries[] =>
    [{ groupKey: [label], points: pts.map(p => ({ time: p.t, value: pick(p) })) }];
  return {
    calls: build(p => p.rps, 'Calls / s'),
    errors: build(p => p.errorRate, 'Error %'),
    p99: build(p => p.p99Ms, 'P99 ms'),
  };
}

// msOrDash — v0.9.263, kept verbatim: a duration the backend did not
// send is '—', never "0.0 ms". 0.0 would read as "instantaneous"
// rather than "not reported".
function msOrDash(v?: number): string {
  return v === undefined ? '—' : `${v.toFixed(1)} ms`;
}

/** fmtTotal — window-wide wall clock, in the largest unit that keeps
 *  the number readable. */
function fmtTotal(ms: number): string {
  if (ms < 1000) return `${ms.toFixed(0)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} min`;
  return `${(ms / 3_600_000).toFixed(1)} h`;
}

function PanelTitle({ children, sub }: { children: React.ReactNode; sub?: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
      <span style={{
        fontSize: 11, fontWeight: 700, letterSpacing: 0.4,
        textTransform: 'uppercase', color: 'var(--text2)',
      }}>{children}</span>
      {sub && (
        <span style={{ fontSize: 10.5, fontWeight: 400, color: 'var(--text3)' }}>{sub}</span>
      )}
    </div>
  );
}

function Stat({ label, value, tone }: {
  label: string; value: string; tone?: 'warn' | 'err';
}) {
  return (
    <div style={{
      padding: '8px 10px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', minWidth: 0,
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', marginBottom: 2,
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>{label}</div>
      <div className="mono" style={{
        fontSize: 15, fontWeight: 600,
        color: tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
    </div>
  );
}

const CALLER_COLS: DataTableColumn<DBCallerBreakdown>[] = [
  { id: 'service', label: 'Service',    sortValue: r => r.service,        naturalDir: 'asc', width: 190 },
  { id: 'pod',     label: 'Pod / host', sortValue: r => r.pod,            naturalDir: 'asc', width: 170 },
  { id: 'calls',   label: 'Calls',      sortValue: r => r.spanCount,      numeric: true, width: 80 },
  { id: 'errRate', label: 'Err %',      sortValue: r => r.errorRate,      numeric: true, width: 76 },
  { id: 'p95',     label: 'P95',        sortValue: r => r.p95DurationMs ?? 0, numeric: true, width: 82 },
  { id: 'impact',  label: 'Impact',     sortValue: r => r.spanCount * r.avgDurationMs, numeric: true, width: 80 },
];

// CallersCard — "who calls this database", the panel /endpoint copied
// in v0.9.839. Impact = calls × avg, the Elastic-APM reading the drawer
// sorted by: a 200ms call made 10k times outweighs a 5s call made
// twice for cumulative load on the backend.
function CallersCard({ callers }: { callers: DBCallerBreakdown[] }) {
  const total = useMemo(
    () => callers.reduce((s, c) => s + c.spanCount * c.avgDurationMs, 0),
    [callers]);
  const dt = useDataTable<DBCallerBreakdown>({
    storageKey: 'database-detail-callers',
    columns: CALLER_COLS,
    rows: callers,
    initialSort: { id: 'impact', dir: 'desc' },
  });
  return (
    <Card header={<PanelTitle sub="service · pod, by impact">Who calls this</PanelTitle>}>
      {callers.length === 0 ? (
        <Empty icon="↘" title="No caller in this window">
          No application span reached this database in the selected range —
          either nothing called it, or the callers are not instrumented.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((c, i) => {
                const errCls = c.errorRate > 5 ? 'b-err' : c.errorRate > 0 ? 'b-warn' : 'b-ok';
                const impact = c.spanCount * c.avgDurationMs;
                return (
                  <tr key={`${c.service}|${c.pod}|${i}`}
                    style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                    <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                      title={c.service}>
                      <Link to={`/service?name=${encodeURIComponent(c.service)}`}
                        className="mono" style={{ fontSize: 11.5 }}>
                        {c.service}
                      </Link>
                    </td>
                    <td className="mono" style={{
                      fontSize: 11, color: 'var(--text2)',
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }} title={c.pod}>{c.pod}</td>
                    <td className="num mono">{fmtNum(c.spanCount)}</td>
                    <td className="num mono">
                      <span className={`badge ${errCls}`} style={{ fontSize: 9 }}>
                        {c.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">
                      {c.p95DurationMs === undefined
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : `${c.p95DurationMs.toFixed(1)} ms`}
                    </td>
                    <td className="num mono">
                      {total > 0 ? `${((impact / total) * 100).toFixed(1)}%` : '—'}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
