// Self-observability panels for /admin/stats (split out of
// AdminStats.tsx — refactor batch item 2, v0.8.269): ingest
// data-loss counters, Redis INFO, and the multi-tier API cache
// effectiveness. Presentation-only — the page owns the queries and
// hands each panel its (undefined | null | data) tri-state.

import { Spinner } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { fmtNum } from '@/lib/utils';
import { KPI, fmtUptime, fmtBytes } from './shared';
import type { DataTableColumn } from '@/lib/dataTable';
import type { RedisStats, CacheStats, SystemStats } from '@/lib/types';

type TopKeyRow = CacheStats['topKeys'][number];

// Hottest API cache keys. Default = hits desc (server already returns
// them hottest-first).
const TOPKEY_COLS: DataTableColumn<TopKeyRow>[] = [
  { id: 'key',  label: 'Key',  sortValue: k => k.key,  naturalDir: 'asc',  width: 420 },
  { id: 'hits', label: 'Hits', sortValue: k => k.hits, numeric: true, naturalDir: 'desc', width: 100 },
];

// DropsPanel renders the cumulative ingest data-loss counters (since process
// start). Compact "✓ no loss" when clean; a red per-signal breakdown when any
// counter is non-zero — queue-full (receiver buffer overflow) vs write-failed
// (ClickHouse insert dropped, not retried). Self-observability: an explicit
// "no loss" indicator is as valuable as the alarm.
export function DropsPanel({ drops }: { drops: SystemStats['drops'] }) {
  const d = drops ?? {
    spansQueueFull: 0, logsQueueFull: 0, metricsQueueFull: 0,
    spansWriteFailed: 0, logsWriteFailed: 0, metricsWriteFailed: 0,
    spansPipeline: 0, logsPipeline: 0, metricsPipeline: 0,
  };
  const total =
    d.spansQueueFull + d.logsQueueFull + d.metricsQueueFull +
    d.spansWriteFailed + d.logsWriteFailed + d.metricsWriteFailed;
  // Pipeline drops are INTENTIONAL (operator drop/sample rules) — kept out
  // of the loss `total`/alarm and shown as a neutral informational line so
  // an active drop rule never flips the "✓ no loss" indicator (v0.8.282).
  const pipelineTotal = d.spansPipeline + d.logsPipeline + d.metricsPipeline;
  const signals = [
    { label: 'Spans',   queueFull: d.spansQueueFull,   writeFailed: d.spansWriteFailed },
    { label: 'Logs',    queueFull: d.logsQueueFull,    writeFailed: d.logsWriteFailed },
    { label: 'Metrics', queueFull: d.metricsQueueFull, writeFailed: d.metricsWriteFailed },
  ];
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: total > 0 ? 12 : 0 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Ingest data loss</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          cumulative since process start · queue-full = buffer overflow · write-failed = CH insert dropped
        </span>
        <span style={{ flex: 1 }} />
        {total === 0
          ? <span className="ok" style={{ fontSize: 12, fontWeight: 600 }}>✓ no loss</span>
          : <span className="err" style={{ fontSize: 12, fontWeight: 700 }}>⚠ {fmtNum(total)} dropped</span>}
      </div>
      {total > 0 && (
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 12,
        }}>
          {signals.map(s => {
            const lost = s.queueFull + s.writeFailed;
            return (
              <div key={s.label} style={{
                padding: 12, border: '1px solid var(--border)',
                borderRadius: 6, background: 'var(--bg2)',
              }}>
                <div style={{
                  fontSize: 10, color: 'var(--text3)',
                  textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
                }}>{s.label}</div>
                <div className={lost > 0 ? 'err' : 'ok'} style={{ fontSize: 20, fontWeight: 700, marginTop: 4 }}>
                  {fmtNum(lost)}
                </div>
                <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                  queue-full {fmtNum(s.queueFull)} · write-failed {fmtNum(s.writeFailed)}
                </div>
              </div>
            );
          })}
        </div>
      )}
      {pipelineTotal > 0 && (
        <div style={{
          marginTop: total > 0 ? 12 : 8, paddingTop: 8,
          borderTop: total > 0 ? '1px solid var(--border)' : 'none',
          fontSize: 11, color: 'var(--text3)',
        }}>
          Dropped by pipeline rules (intentional): spans {fmtNum(d.spansPipeline)}
          {' · '}logs {fmtNum(d.logsPipeline)} · metrics {fmtNum(d.metricsPipeline)}
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
export function RedisPanel({ data }: { data: RedisStats | null | undefined }) {
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
export function ApiCachePanel({ data }: { data: CacheStats | null | undefined }) {
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
