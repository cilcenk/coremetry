import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useSystemStats, keys } from '@/lib/queries';
import { api } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  TimeRange,
  SystemStatus, ComponentHealth, StatusComponent,
  RedisStats, CacheStats, SystemStats,
} from '@/lib/types';

// Row types for the shared sortable + resizable DataTable adoption.
type TableStatRow = SystemStats['tables'][number];
type HistoryRow = SystemStats['history'][number];
type TopKeyRow = CacheStats['topKeys'][number];

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

// Hottest API cache keys. Default = hits desc (server already returns
// them hottest-first).
const TOPKEY_COLS: DataTableColumn<TopKeyRow>[] = [
  { id: 'key',  label: 'Key',  sortValue: k => k.key,  naturalDir: 'asc',  width: 420 },
  { id: 'hits', label: 'Hits', sortValue: k => k.hits, numeric: true, naturalDir: 'desc', width: 100 },
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
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
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
              <KPI label="Metrics · 24h" value={fmtNum(data.snapshot.metrics24h)}
                   sub={`${fmtRate(data.ingest.metricsPerSec)} now`} />
              <KPI label="Metrics total" value={fmtNum(data.snapshot.metricsAllTime)} />
              <KPI label="Profiles · 24h" value={fmtNum(data.snapshot.profiles24h)} />
              <KPI label="Services · 24h" value={fmtNum(data.snapshot.services24h)} />
              <KPI label="Operations · 24h" value={fmtNum(data.snapshot.operations24h)} />
              <KPI label="Disk total" value={fmtBytes(data.snapshot.totalDiskBytes)} />
            </div>

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

function SectionHeader({ title, sub }: { title: string; sub?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 10 }}>
      <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text)' }}>{title}</h2>
      {sub && <span style={{ fontSize: 11, color: 'var(--text3)' }}>{sub}</span>}
    </div>
  );
}

// ── Live status section helpers (kept verbatim from the old
// /status page so the visual rhythm — banner colour, dot, chip
// styling — stays consistent with what operators are used to). ──

function statusHeadline(s: ComponentHealth): string {
  switch (s) {
    case 'operational': return 'All systems operational';
    case 'degraded':    return 'Some systems experiencing issues';
    case 'outage':      return 'Major outage in one or more systems';
  }
}

function Banner({ status, headline }: { status: ComponentHealth; headline: string }) {
  return (
    <div className={`status-banner status-banner-${status}`}>
      <span className={`status-pill status-pill-${status}`}>
        <StatusIcon status={status} />
      </span>
      <span style={{ fontWeight: 700, fontSize: 18 }}>{headline}</span>
    </div>
  );
}

function ComponentRow({ c }: { c: StatusComponent }) {
  const infoEntries = Object.entries(c.info ?? {});
  return (
    <div className={`status-row status-row-${c.status}`}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, flexWrap: 'wrap' }}>
        <StatusDot status={c.status} />
        <span style={{ fontWeight: 600 }}>{c.name}</span>
        {c.message && (
          <span style={{ color: 'var(--text3)', fontSize: 12, maxWidth: 360,
                         overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                title={c.message}>
            · {c.message}
          </span>
        )}
        {c.ratePerSec !== undefined && (
          <InfoChip k="rate" v={`${c.ratePerSec.toFixed(1)}/s`} highlight />
        )}
        {infoEntries.map(([k, v]) => <InfoChip key={k} k={k} v={v} />)}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0 }}>
        {c.latencyMs !== undefined && c.latencyMs > 0 && (
          <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'monospace' }}
                title="Probe latency">
            {c.latencyMs}ms
          </span>
        )}
        <span className={`status-pill status-pill-${c.status}`}>{labelOf(c.status)}</span>
      </div>
    </div>
  );
}

function InfoChip({ k, v, highlight }: { k: string; v: string; highlight?: boolean }) {
  return (
    <span style={{
      fontSize: 11, fontFamily: 'monospace', padding: '1px 6px', borderRadius: 4,
      background: highlight ? 'rgba(56,139,253,.14)' : 'var(--bg3)',
      color: highlight ? 'var(--accent)' : 'var(--text2)',
      border: highlight ? '1px solid rgba(56,139,253,.30)' : '1px solid var(--border)',
      whiteSpace: 'nowrap',
    }}>
      <span style={{ opacity: .65, marginRight: 4 }}>{k}:</span>{v}
    </span>
  );
}

function labelOf(s: ComponentHealth): string {
  switch (s) {
    case 'operational': return 'Operational';
    case 'degraded':    return 'Degraded';
    case 'outage':      return 'Outage';
  }
}

function StatusDot({ status }: { status: ComponentHealth }) {
  return <span className={`status-dot status-dot-${status}`} aria-hidden />;
}

function StatusIcon({ status }: { status: ComponentHealth }) {
  switch (status) {
    case 'operational':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M6.7 11.3 3.4 8l1.4-1.4 1.9 1.9 4.5-4.5 1.4 1.4z"/></svg>;
    case 'degraded':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M8 1l7 13H1zm-1 5v4h2V6zm0 5v2h2v-2z"/></svg>;
    case 'outage':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1zm-1 3h2v5H7zm0 6h2v2H7z"/></svg>;
  }
}

function Legend() {
  return (
    <div style={{ marginTop: 14, display: 'flex', gap: 16, fontSize: 11, color: 'var(--text3)' }}>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="operational" /> Operational
      </span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="degraded" /> Degraded
      </span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="outage" /> Outage
      </span>
    </div>
  );
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: 12, border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg2)',
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
      }}>{label}</div>
      <div className={cls} style={{ fontSize: 20, fontWeight: 700, marginTop: 4 }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
          {sub}
        </div>
      )}
    </div>
  );
}

// RedisPanel renders Redis INFO + DBSIZE — keys, memory, hit-rate,
// ops/sec — alongside the ClickHouse storage table. Falls back to
// "Redis not configured" when version is empty (server returned a
// zero-valued struct because no Redis URL is wired). Polled every
// 10s so the ops/sec gauge feels live during incident response.
function RedisPanel({ data }: { data: RedisStats | null | undefined }) {
  if (data === undefined) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          Redis cache <span style={{ color: 'var(--text3)', fontWeight: 400 }}>· loading…</span>
        </div>
        <Spinner />
      </div>
    );
  }
  if (data === null) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          Redis cache <span style={{ color: 'var(--err)', fontWeight: 400 }}>· probe failed</span>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text2)' }}>
          INFO command returned an error. Check the Redis URL in config or container logs.
        </div>
      </div>
    );
  }
  if (!data.version) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 8 }}>
          Redis cache <span style={{ color: 'var(--text3)', fontWeight: 400 }}>· not configured</span>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text2)', lineHeight: 1.6 }}>
          Coremetry is running with the in-memory Noop cache. For multi-replica HA
          (alert deduplication, response cache shared across pods, anomaly
          evaluator leader election) wire <code>cache.redis_url</code> in the config or
          set <code>COREMETRY_REDIS_URL=redis://&lt;host&gt;:6379/0</code> in the
          environment.
        </div>
      </div>
    );
  }
  const memPct = data.maxMemoryBytes > 0
    ? (data.usedMemoryBytes / data.maxMemoryBytes) * 100
    : 0;
  const evicting = data.evictedKeys > 0;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12,
      }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Redis cache</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          v{data.version} · {data.mode || 'standalone'} · uptime {fmtUptime(data.uptimeSec)}
        </span>
      </div>
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))', gap: 10,
      }}>
        <KPI label="Keys"          value={fmtNum(data.keys)} />
        <KPI label="Hit rate"      value={`${(data.hitRate * 100).toFixed(1)}%`}
             cls={data.hitRate >= 0.8 ? 'ok' : data.hitRate >= 0.5 ? 'warn' : 'err'} />
        <KPI label="Ops / sec"     value={fmtNum(Math.round(data.opsPerSec))} />
        <KPI label="Clients"       value={String(data.connectedClients)} />
        <KPI label="Memory"
             value={fmtBytes(data.usedMemoryBytes)}
             sub={data.maxMemoryBytes > 0
               ? `${memPct.toFixed(0)}% of ${fmtBytes(data.maxMemoryBytes)}`
               : `peak ${fmtBytes(data.usedMemoryPeakBytes)}`}
             cls={memPct >= 90 ? 'err' : memPct >= 75 ? 'warn' : undefined} />
        <KPI label="Net in"        value={`${data.netInputKbps.toFixed(1)} KB/s`} />
        <KPI label="Net out"       value={`${data.netOutputKbps.toFixed(1)} KB/s`} />
        <KPI label="Evicted"
             value={fmtNum(data.evictedKeys)}
             cls={evicting ? 'warn' : undefined}
             sub={evicting ? 'maxmemory pressure' : undefined} />
        <KPI label="Expired"       value={fmtNum(data.expiredKeys)} />
      </div>
    </div>
  );
}

// ApiCachePanel renders the multi-tier API cache effectiveness:
// per-tier hit distribution as a stacked bar, KPI tiles for the
// computed hit rate / total requests / L1 fill, and the top
// 20 hottest keys. Polled every 10s so the operator can see
// the cache warming up after a deploy. Self-hides with a
// "cache idle" tile when no requests have been served yet
// (fresh process, no traffic).
const TIER_ORDER = ['HIT-L1', 'HIT', 'STALE', 'HIT-LEGACY', 'MISS', 'BYPASS'] as const;
const TIER_COLOR: Record<string, string> = {
  'HIT-L1':     '#4ade80', // green — best, no network
  'HIT':        '#22d3ee', // teal — Redis fresh
  'STALE':      '#facc15', // amber — served stale, refresh fired
  'HIT-LEGACY': '#94a3b8', // grey — pre-envelope entry
  'MISS':       '#f87171', // red — upstream hit
  'BYPASS':     '#a78bfa', // purple — operator forced refresh
};
function ApiCachePanel({ data }: { data: CacheStats | null | undefined }) {
  // Shared sortable + resizable hot-keys table — hook BEFORE the
  // early returns below (react-hooks rules-of-hooks).
  const topKeysDt = useDataTable<TopKeyRow>({
    storageKey: 'adminstats-topkeys', columns: TOPKEY_COLS,
    rows: data?.topKeys ?? [], initialSort: { id: 'hits', dir: 'desc' },
  });
  if (data === undefined) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          API cache <span style={{ color: 'var(--text3)', fontWeight: 400 }}>· loading…</span>
        </div>
        <Spinner />
      </div>
    );
  }
  if (data === null) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          API cache <span style={{ color: 'var(--err)', fontWeight: 400 }}>· probe failed</span>
        </div>
      </div>
    );
  }
  const counts = data.counts || {};
  const total = TIER_ORDER.reduce((acc, t) => acc + (counts[t] ?? 0), 0);
  const hits = ['HIT-L1', 'HIT', 'STALE', 'HIT-LEGACY']
    .reduce((acc, t) => acc + (counts[t] ?? 0), 0);
  const hitRate = total > 0 ? (hits / total) * 100 : 0;
  const sinceMs = (Date.now() * 1_000_000 - data.sinceUnixNano) / 1_000_000;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>API cache</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          L1 · Redis · singleflight · SWR — since {fmtUptime(Math.floor(sinceMs / 1000))} ago
        </span>
      </div>

      {total === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text2)' }}>
          No cached endpoints served yet since process start. Hit any
          dashboard / services page to populate.
        </div>
      ) : (
        <>
          {/* KPI tiles */}
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 10, marginBottom: 14,
          }}>
            <KPI label="Hit rate" value={`${hitRate.toFixed(1)}%`}
              cls={hitRate < 50 ? 'warn' : undefined}
              sub={hitRate < 50 ? 'cold cache — most requests miss' : undefined} />
            <KPI label="Total requests" value={fmtNum(total)} />
            <KPI label="L1 (in-process)" value={`${data.l1Size} / ${data.l1Cap}`}
              sub={data.l1Size >= data.l1Cap ? 'at cap — eviction active' : undefined} />
            <KPI label="Stale refreshes" value={fmtNum(counts['STALE'] ?? 0)}
              sub="served immediately + background refresh fired" />
          </div>

          {/* Stacked tier-distribution bar */}
          <div style={{ marginBottom: 14 }}>
            <div style={{
              display: 'flex', height: 14, borderRadius: 4, overflow: 'hidden',
              border: '1px solid var(--border)',
            }}>
              {TIER_ORDER.map(tier => {
                const n = counts[tier] ?? 0;
                const pct = total > 0 ? (n / total) * 100 : 0;
                if (pct === 0) return null;
                return (
                  <div key={tier} title={`${tier}: ${fmtNum(n)} (${pct.toFixed(1)}%)`}
                    style={{ width: `${pct}%`, background: TIER_COLOR[tier] }} />
                );
              })}
            </div>
            <div style={{
              display: 'flex', flexWrap: 'wrap', gap: 12,
              fontSize: 11, color: 'var(--text2)', marginTop: 8,
            }}>
              {TIER_ORDER.map(tier => {
                const n = counts[tier] ?? 0;
                if (n === 0) return null;
                const pct = total > 0 ? (n / total) * 100 : 0;
                return (
                  <span key={tier} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                    <span style={{
                      display: 'inline-block', width: 10, height: 10,
                      borderRadius: 2, background: TIER_COLOR[tier],
                    }} />
                    <span style={{ fontFamily: 'ui-monospace, monospace' }}>{tier}</span>
                    <span style={{ color: 'var(--text3)' }}>
                      {fmtNum(n)} · {pct.toFixed(1)}%
                    </span>
                  </span>
                );
              })}
            </div>
          </div>

          {/* Top hot keys */}
          {data.topKeys && data.topKeys.length > 0 && (
            <div>
              <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)', marginBottom: 6 }}>
                Hottest cache keys
              </div>
              <div className="table-wrap">
                <table style={{ fontSize: 12, tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={topKeysDt} />
                  <DataTableHead dt={topKeysDt} />
                  <tbody>
                    {topKeysDt.sortedRows.map(k => (
                      <tr key={k.key}>
                        <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                          {k.key}
                        </td>
                        <td className="num">{fmtNum(k.hits)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function fmtUptime(sec: number): string {
  if (!sec || sec < 0) return '—';
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}

function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)} ${units[i]}`;
}

function fmtRate(perSec: number): string {
  if (!perSec || perSec < 0) return '0 /s';
  if (perSec >= 1000) return `${(perSec / 1000).toFixed(1)}k /s`;
  if (perSec >= 1) return `${perSec.toFixed(0)} /s`;
  return `${perSec.toFixed(2)} /s`;
}
