import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useSystemStats, useTraceContext, keys } from '@/lib/queries';
import { useUrlRange } from '@/lib/useUrlRange';
import { api } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  SystemStatus,
  RedisStats, CacheStats, SystemStats,
} from '@/lib/types';
import { SectionHeader, KPI, fmtBytes, fmtRate } from './adminstats/shared';
import { statusHeadline, Banner, ComponentRow, Legend } from './adminstats/StatusSection';
import { DropsPanel, RedisPanel, ApiCachePanel } from './adminstats/panels';

// Row types for the shared sortable + resizable DataTable adoption.
type TableStatRow = SystemStats['tables'][number];
type HistoryRow = SystemStats['history'][number];

// ClickHouse per-table storage. Compression sorts on the raw ratio
// (compressed/uncompressed) so the most-/least-compressible tables
// sort sensibly even though the cell renders a "% (raw)" string.
const STORAGE_COLS: DataTableColumn<TableStatRow>[] = [
  { id: 'table',       label: 'Table',       sortValue: t => t.table,            naturalDir: 'asc',  width: 220 },
  { id: 'rows',        label: 'Rows',        sortValue: t => t.rows,             numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'disk',        label: 'On disk',     sortValue: t => t.bytesOnDisk,      numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'compression', label: 'Compression', sortValue: t => t.uncompressedBytes > 0 ? t.compressedBytes / t.uncompressedBytes : 0,
                                                                                  numeric: true, naturalDir: 'desc', width: 200 },
  { id: 'parts',       label: 'Parts',       sortValue: t => t.parts,            numeric: true, naturalDir: 'desc', width: 90 },
  { id: 'oldest',      label: 'Oldest',      sortValue: t => t.oldestNs,         naturalDir: 'asc',  width: 180 },
  { id: 'newest',      label: 'Newest',      sortValue: t => t.newestNs,         naturalDir: 'desc', width: 180 },
];

// Daily history. Default sort = day desc (newest first), preserving
// the prior `[...history].reverse()` ordering.
const HISTORY_COLS: DataTableColumn<HistoryRow>[] = [
  { id: 'day',      label: 'Day',      sortValue: d => d.day,                                naturalDir: 'desc', width: 140 },
  { id: 'traces',   label: 'Traces',   sortValue: d => d.traces,            numeric: true,   naturalDir: 'desc', width: 110 },
  { id: 'spans',    label: 'Spans',    sortValue: d => d.spans,             numeric: true,   naturalDir: 'desc', width: 110 },
  { id: 'errors',   label: 'Errors',   sortValue: d => d.errors,            numeric: true,   naturalDir: 'desc', width: 110 },
  { id: 'errPct',   label: 'Err %',    sortValue: d => d.spans > 0 ? (d.errors / d.spans) * 100 : 0,
                                                                            numeric: true,   naturalDir: 'desc', width: 90 },
  { id: 'services', label: 'Services', sortValue: d => d.services,          numeric: true,   naturalDir: 'desc', width: 100 },
];


// Coremetry "what's inside" page. Three sections stacked top-to-
// bottom:
//   1. Live system status — banner + per-component probe results,
//      auto-refreshing every 30s. Folded in from the old /status
//      page so the operator has one place to start.
//   2. Volume KPIs / 30-day history / live ingest rate.
//   3. Per-table ClickHouse storage with compression ratio.
export default function AdminStatsPage() {
  // Topbar wants a TimeRange even though this page doesn't use it.
  const [range, setRange] = useUrlRange('30m');
  const qc = useQueryClient();

  // Health probe — its own poll cycle (30s) so a slow systemStats
  // refetch doesn't block the freshness of the live banner.
  const statusQ = useQuery<SystemStatus | null>({
    queryKey: ['admin', 'status'],
    queryFn: () => api.status(),
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
  const status = statusQ.isLoading ? undefined : statusQ.isError ? null : statusQ.data;

  // System stats — 60s cached on the server, 60s polled on the
  // client. Refresh button invalidates the cache via the
  // useQueryClient handle.
  const dataQ = useSystemStats();
  const data = dataQ.isLoading ? undefined : dataQ.isError ? null : dataQ.data;

  // Redis live snapshot — 5s server cache, 10s client poll. Separate
  // query so the rest of the page (60s polled) doesn't have to wait
  // on the Redis round-trip and vice-versa.
  const redisQ = useQuery<RedisStats>({
    queryKey: ['admin', 'redis-stats'],
    queryFn: () => api.redisStats(),
    refetchInterval: 10_000,
    staleTime: 7_000,
  });
  const redis = redisQ.isLoading ? undefined : redisQ.isError ? null : redisQ.data;

  // API multi-tier cache stats — 10s poll so the operator can
  // watch hit-rate move under load. Server doesn't cache this
  // endpoint (would pollute its own counters).
  const cacheStatsQ = useQuery<CacheStats>({
    queryKey: ['admin', 'cache-stats'],
    queryFn: () => api.cacheStats(),
    refetchInterval: 10_000,
    staleTime: 7_000,
  });
  const cacheStats = cacheStatsQ.isLoading ? undefined
    : cacheStatsQ.isError ? null : cacheStatsQ.data;

  // Trace-context coverage snapshot (v0.8.348, pivot Phase 1c). One-shot,
  // 5m-stale (matches the server cache); the KPI simply doesn't render
  // until the report is available — best-effort, never blocks the page.
  const traceCtxQ = useTraceContext();
  const traceCtx = traceCtxQ.data?.report;
  const setRefreshTick = (_n: number | ((p: number) => number)) => {
    qc.invalidateQueries({ queryKey: keys.admin.systemStats });
  };

  const histMax = useMemo(() => {
    if (!data?.history?.length) return 0;
    return Math.max(...data.history.map(d => d.spans));
  }, [data]);

  // Shared sortable + resizable tables. Hooks are unconditional —
  // they sit above the `data` conditional render below.
  const storageDt = useDataTable<TableStatRow>({
    storageKey: 'adminstats-storage', columns: STORAGE_COLS,
    rows: data?.tables ?? [], initialSort: { id: 'disk', dir: 'desc' },
  });
  const historyDt = useDataTable<HistoryRow>({
    storageKey: 'adminstats-history', columns: HISTORY_COLS,
    rows: data?.history ?? [], initialSort: { id: 'day', dir: 'desc' },
  });

  return (
    <>
      <Topbar title="System" range={range} onRangeChange={setRange} />
      <div id="content">
        {/* ── Live status banner + components ────────────────────── */}
        <SectionHeader title="Live status"
          sub={status?.checkedAt
            ? `last checked ${new Date(status.checkedAt).toLocaleTimeString()} · auto-refreshes every 30s`
            : 'probing…'} />
        {status === undefined && <Spinner />}
        {status === null && (
          <Banner status="outage" headline="Could not reach Coremetry status endpoint" />
        )}
        {status && (
          <>
            <Banner status={status.status} headline={statusHeadline(status.status)} />
            <div className="status-grid" style={{ marginTop: 10 }}>
              {status.components.map(c => <ComponentRow key={c.name} c={c} />)}
            </div>
            <Legend />
          </>
        )}

        <div style={{ height: 24 }} />

        {/* ── Volume / storage / history ─────────────────────────── */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
          <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text)' }}>
            What's inside
          </h2>
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            cached 60s · system.parts + service_summary_5m MV
          </span>
          <span style={{ flex: 1 }} />
          <button className="sec" onClick={() => setRefreshTick(t => t + 1)}
            title="Force a fresh recompute">↻ Refresh</button>
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load system stats" />}
        {data && (
          <>
            {/* ── Empty-MV health warning (v0.8.211) ────────────────── */}
            {data.health?.externalDistributedSpansUnset && (
              <div style={{
                border: '1px solid var(--err)', background: 'rgba(220,80,80,0.08)',
                borderRadius: 6, padding: '10px 14px', marginBottom: 16, fontSize: 13, color: 'var(--text)',
              }}>
                <strong>⚠ Materialized views are not populating.</strong>{' '}
                <code>spans</code> is an external Distributed table but{' '}
                <code>COREMETRY_CH_CLUSTER_NAME</code> is unset — MV insert-triggers never fire, so the
                summary MVs (service_summary_5m, trace_service_index_5m, …) stay empty and reads return
                no / partial results.{' '}
                {data.health.suggestedClusterName
                  ? <>Fix: set <code>COREMETRY_CH_CLUSTER_NAME={data.health.suggestedClusterName}</code> and restart.</>
                  : <>Fix: set <code>COREMETRY_CH_CLUSTER_NAME</code> to the cluster the external spans fans to, and restart.</>}
              </div>
            )}

            {/* ── Duplicate-worker HA warning (v0.8.212) ────────────── */}
            {data.health?.lockDegraded && (
              <div style={{
                border: '1px solid var(--err)', background: 'rgba(220,80,80,0.08)',
                borderRadius: 6, padding: '10px 14px', marginBottom: 16, fontSize: 13, color: 'var(--text)',
              }}>
                <strong>⚠ Distributed leader lock is degraded.</strong>{' '}
                <code>COREMETRY_REDIS_URL</code> is set but Redis is unreachable, so this pod runs the
                always-leader fallback. If you run more than one replica, background jobs (alerts,
                notifications, topology aggregation, retention) are <strong>duplicated</strong> across
                pods. Fix: restore Redis so exactly one pod holds leadership.
              </div>
            )}

            {/* ── Volume KPIs ──────────────────────────────────────── */}
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))',
              gap: 12, marginBottom: 18,
            }}>
              <KPI label="Spans · 24h" value={fmtNum(data.snapshot.spans24h)}
                   sub={`${fmtRate(data.ingest.spansPerSec)} now`} />
              <KPI label="Spans · 7d"  value={fmtNum(data.snapshot.spans7d)} />
              <KPI label="Spans total" value={fmtNum(data.snapshot.spansAllTime)} />
              <KPI label="Errors · 24h"
                   value={fmtNum(data.snapshot.errors24h)}
                   cls={data.snapshot.errors24h > 0 ? 'warn' : 'ok'} />
              <KPI label="Logs · 24h" value={fmtNum(data.snapshot.logs24h)}
                   sub={`${fmtRate(data.ingest.logsPerSec)} now`} />
              <KPI label="Logs total" value={fmtNum(data.snapshot.logsAllTime)} />
              {traceCtx?.available && traceCtx.total > 0 && (
                <KPI label="Logs w/ trace ctx · 24h"
                     value={`${((traceCtx.withTrace / traceCtx.total) * 100).toFixed(1)}%`}
                     cls={traceCtx.pivotReady ? undefined : 'warn'}
                     sub={`${fmtNum(traceCtx.withTrace)} of ${fmtNum(traceCtx.total)} · ${traceCtxQ.data?.backend ?? ''}`} />
              )}
              <KPI label="Metrics · 24h" value={fmtNum(data.snapshot.metrics24h)}
                   sub={`${fmtRate(data.ingest.metricsPerSec)} now`} />
              <KPI label="Metrics total" value={fmtNum(data.snapshot.metricsAllTime)} />
              <KPI label="Profiles · 24h" value={fmtNum(data.snapshot.profiles24h)} />
              <KPI label="Services · 24h" value={fmtNum(data.snapshot.services24h)} />
              <KPI label="Operations · 24h" value={fmtNum(data.snapshot.operations24h)} />
              <KPI label="Disk total" value={fmtBytes(data.snapshot.totalDiskBytes)} />
            </div>

            {/* ── Ingest data loss ────────────────────────────────── */}
            <DropsPanel drops={data.drops} />

            {/* ── 30-day history ──────────────────────────────────── */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 18,
            }}>
              <div style={{
                display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 10,
              }}>
                <span style={{ fontSize: 12, fontWeight: 600 }}>
                  Spans / day · last 30 days
                </span>
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  bars scaled to peak day · errors overlay in red
                </span>
              </div>
              {data.history.length === 0 ? (
                <div style={{ color: 'var(--text3)', fontSize: 12, fontStyle: 'italic' }}>
                  No history yet. The 5-minute aggregate MV needs at least one bucket to populate.
                </div>
              ) : (
                <div style={{
                  display: 'flex', alignItems: 'flex-end', gap: 2,
                  height: 140, paddingTop: 8,
                }}>
                  {data.history.map(d => {
                    const h = histMax > 0 ? Math.max(2, (d.spans / histMax) * 130) : 2;
                    const errH = d.spans > 0
                      ? Math.max(0, (d.errors / d.spans) * h)
                      : 0;
                    return (
                      <div key={d.day} style={{
                        flex: 1, minWidth: 6, display: 'flex',
                        flexDirection: 'column', alignItems: 'center',
                        position: 'relative',
                      }}
                        title={
                          `${d.day}\n` +
                          `${fmtNum(d.spans)} spans\n` +
                          `${fmtNum(d.errors)} errors\n` +
                          `${d.services} service${d.services === 1 ? '' : 's'}`
                        }>
                        <div style={{ width: '100%', height: h, position: 'relative',
                                      background: 'var(--accent2)', borderRadius: '2px 2px 0 0' }}>
                          {errH > 0 && (
                            <div style={{
                              position: 'absolute', bottom: 0, left: 0, right: 0,
                              height: errH, background: 'var(--err)',
                              borderRadius: '0 0 0 0',
                            }} />
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
              {/* X-axis label endpoints */}
              {data.history.length >= 2 && (
                <div style={{
                  display: 'flex', justifyContent: 'space-between',
                  fontSize: 10, color: 'var(--text3)', marginTop: 6,
                  fontFamily: 'ui-monospace, monospace',
                }}>
                  <span>{data.history[0].day}</span>
                  <span>{data.history[data.history.length - 1].day}</span>
                </div>
              )}
            </div>

            {/* ── Redis cache live status ─────────────────────────── */}
            <RedisPanel data={redis} />

            {/* ── API multi-tier cache effectiveness ──────────────── */}
            <ApiCachePanel data={cacheStats} />

            {/* ── Per-table storage ───────────────────────────────── */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 18,
            }}>
              <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
                ClickHouse storage · {data.tables.length} table{data.tables.length === 1 ? '' : 's'}
              </div>
              <div className="table-wrap">
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={storageDt} />
                  <DataTableHead dt={storageDt} />
                  <tbody>
                    {storageDt.sortedRows.map(t => {
                      const ratio = t.uncompressedBytes > 0
                        ? t.compressedBytes / t.uncompressedBytes
                        : 0;
                      return (
                        <tr key={t.table}>
                          <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>{t.table}</td>
                          <td className="num">{fmtNum(t.rows)}</td>
                          <td className="num">{fmtBytes(t.bytesOnDisk)}</td>
                          <td className="num" style={{ color: 'var(--text3)' }}>
                            {ratio > 0
                              ? `${(ratio * 100).toFixed(1)}% (${fmtBytes(t.uncompressedBytes)} raw)`
                              : '—'}
                          </td>
                          <td className="num">{t.parts}</td>
                          <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                            {t.oldestNs ? tsLong(t.oldestNs) : '—'}
                          </td>
                          <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                            {t.newestNs ? tsLong(t.newestNs) : '—'}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>

            {/* ── 30-day history table ────────────────────────────── */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 18,
            }}>
              <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
                Daily history
              </div>
              <div className="table-wrap" style={{ maxHeight: 360, overflowY: 'auto' }}>
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={historyDt} />
                  <DataTableHead dt={historyDt} />
                  <tbody>
                    {historyDt.sortedRows.map(d => {
                      const errPct = d.spans > 0 ? (d.errors / d.spans) * 100 : 0;
                      return (
                        <tr key={d.day}>
                          <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>{d.day}</td>
                          <td className="num">{fmtNum(d.traces)}</td>
                          <td className="num">{fmtNum(d.spans)}</td>
                          <td className="num">{fmtNum(d.errors)}</td>
                          <td className={`num ${errPct >= 5 ? 'err' : errPct > 0 ? 'warn' : ''}`}>
                            {errPct.toFixed(2)}%
                          </td>
                          <td className="num">{d.services}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>

            <div style={{ fontSize: 11, color: 'var(--text3)' }}>
              Tip: <Link to="/services" style={{ color: 'var(--accent2)' }}>/services</Link>
              {' '}lists all live services; <Link to="/alerts" style={{ color: 'var(--accent2)' }}>/alerts</Link>
              {' '}shows the rules driving Problems / Incidents.
            </div>
          </>
        )}
      </div>
    </>
  );
}
