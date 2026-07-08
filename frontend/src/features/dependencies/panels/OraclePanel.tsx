import { useEffect, useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { TimeRange, OracleMetrics } from '@/lib/types';
import {
  Stat, GaugeStat, OracleMetricDrillModal, TopSQLTable, HostLink, fmtBytes,
  WaitClassesBar,
  type OracleDrill,
} from './shared';

// OraclePanel renders the OracleDB-receiver drill-down. Fetches
// `/api/databases/oracle?instance=…` and shows a KPI grid +
// tablespace usage bars. When the backend has no real
// oracledb.* points it returns synthetic=true and a "demo data"
// chip is rendered so the operator knows the integration
// isn't actually online yet.
export function OraclePanel({ instance, range }: { instance: string; range: TimeRange }) {
  const [data, setData] = useState<OracleMetrics | null | undefined>(undefined);
  // Drill-down modal state. null when closed; an OracleDrill when
  // the operator clicked a tile / wait class / tablespace row.
  // The modal queries /api/metrics/query for the metric over the
  // same window the panel is showing.
  const [drill, setDrill] = useState<OracleDrill | null>(null);
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.oracleMetrics(instance, from, to)
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [instance, range]);

  const tsFilters = (name: string) =>
    [{ key: 'tablespace_name' as const, op: '=' as const, value: name }];

  return (
    <div style={{
      marginTop: 6, marginBottom: 14, padding: 12, borderRadius: 6,
      background: 'rgba(216,72,57,0.05)',
      border: '1px solid rgba(216,72,57,0.25)',
    }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10,
        fontSize: 12, fontWeight: 700, color: '#d84839',
      }}>
        <span style={{ fontSize: 13 }}>⛁</span>
        OracleDB receiver
        {data && (
          <span title={data.status === 'up'
            ? 'oracledb.* metric_points present in window'
            : 'No oracledb.* metric_points seen — receiver may be down or not yet wired'}
                style={{
                  fontSize: 9, padding: '1px 6px', borderRadius: 3,
                  background: data.status === 'up' ? 'rgba(63,185,80,0.15)' : 'rgba(248,81,73,0.15)',
                  color: data.status === 'up' ? 'var(--ok)' : 'var(--err)',
                  fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                  textTransform: 'uppercase', letterSpacing: '.5px',
                }}>{data.status}</span>
        )}
        <span style={{
          marginLeft: 'auto', fontSize: 10, color: 'var(--text3)',
          fontWeight: 400, fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>
          instance: {instance || '(unknown)'}
        </span>
        {instance && <HostLink instance={instance} />}
      </div>

      {data === undefined && <Spinner />}
      {data === null && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>Oracle metrics query failed.</div>
      )}
      {data && (
        <>
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 8, marginBottom: 12,
          }}>
            <GaugeStat label="Sessions"
              usage={data.sessions.usage} limit={data.sessions.limit}
              sub={data.sessions.active > 0 || data.sessions.inactive > 0
                ? `${fmtNum(data.sessions.active)} active · ${fmtNum(data.sessions.inactive)} idle`
                : undefined}
              onClick={() => setDrill({ metric: 'oracledb.sessions.usage', label: 'Sessions' })} />
            <GaugeStat label="Processes"
              usage={data.processes.usage} limit={data.processes.limit}
              onClick={() => setDrill({ metric: 'oracledb.processes.usage', label: 'Processes' })} />
            <Stat label="Logical reads/s"  value={fmtNum(data.logicalReadsPerSec)}
              onClick={() => setDrill({ metric: 'oracledb.logical_reads', label: 'Logical reads', unit: '/s' })} />
            <Stat label="Physical reads/s" value={fmtNum(data.physicalReadsPerSec)}
                  tone={data.physicalReadsPerSec > data.logicalReadsPerSec * 0.05 ? 'warn' : undefined}
                  onClick={() => setDrill({ metric: 'oracledb.physical_reads', label: 'Physical reads', unit: '/s' })} />
            <Stat label="Cache hit"        value={`${data.cacheHitPct.toFixed(1)}%`}
                  tone={data.cacheHitPct < 95 ? 'warn' : 'ok'}
                  onClick={() => setDrill({ metric: 'oracledb.physical_reads', label: 'Physical vs logical reads (cache hit)', unit: '/s' })} />
            <Stat label="Row-lock waits/s" value={data.rowLockWaitsPerSec.toFixed(2)}
                  tone={data.rowLockWaitsPerSec > 1 ? 'err'
                       : data.rowLockWaitsPerSec > 0.2 ? 'warn' : 'ok'}
                  onClick={() => setDrill({ metric: 'oracledb.row_lock_waits', label: 'Row-lock waits', unit: '/s' })} />
            <Stat label="Executions/s"     value={fmtNum(data.executionsPerSec)}
                  onClick={() => setDrill({ metric: 'oracledb.executions', label: 'Executions', unit: '/s' })} />
            <Stat label="Commits/s"        value={fmtNum(data.userCommitsPerSec)}
                  onClick={() => setDrill({ metric: 'oracledb.user_commits', label: 'User commits', unit: '/s' })} />
            <Stat label="Rollbacks/s"      value={fmtNum(data.userRollbacksPerSec)}
                  tone={data.userRollbacksPerSec > data.userCommitsPerSec * 0.05 ? 'warn' : undefined}
                  onClick={() => setDrill({ metric: 'oracledb.user_rollbacks', label: 'User rollbacks', unit: '/s' })} />
            <Stat label="Hard parses/s"    value={fmtNum(data.hardParsesPerSec)}
                  onClick={() => setDrill({ metric: 'oracledb.hard_parses', label: 'Hard parses', unit: '/s' })} />
            <Stat label="Parse calls/s"    value={fmtNum(data.parseCallsPerSec)}
                  onClick={() => setDrill({ metric: 'oracledb.parse_calls', label: 'Parse calls', unit: '/s' })} />
            <Stat label="CPU time"         value={`${data.cpuTimeSec.toFixed(0)}s`}
                  onClick={() => setDrill({ metric: 'oracledb.cpu_time', label: 'CPU time', unit: 's' })} />
            <Stat label="SGA"              value={fmtBytes(data.sgaMemoryBytes)}
                  onClick={() => setDrill({ metric: 'oracledb.sga_max_size', label: 'SGA size', unit: 'B' })} />
            <Stat label="PGA memory"       value={fmtBytes(data.pgaMemoryBytes)}
                  onClick={() => setDrill({ metric: 'oracledb.pga_memory', label: 'PGA memory', unit: 'B' })} />
          </div>

          {data.waitClasses.length > 0 && (
            <WaitClassesBar waits={data.waitClasses}
              onClickClass={cls => setDrill({
                metric: `oracledb.wait_time.${cls}`,
                label: `Wait time · ${cls}`,
                unit: 's',
              })} />
          )}

          {data.topSQL.length > 0 && (
            <TopSQLTable rows={data.topSQL} instance={instance} />
          )}

          {data.tablespaces.length > 0 && (
            <div>
              <div style={{
                fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
                textTransform: 'uppercase', letterSpacing: 0.4,
              }}>
                Tablespaces ({data.tablespaces.length})
              </div>
              <div style={{ display: 'grid', gap: 4 }}>
                {[...data.tablespaces]
                  .sort((a, b) => b.usedPct - a.usedPct)
                  .map(t => (
                    <TablespaceBar key={t.name} ts={t}
                      onClick={() => setDrill({
                        metric: 'oracledb.tablespace_size.usage',
                        label: `Tablespace · ${t.name}`,
                        unit: 'B',
                        filters: tsFilters(t.name),
                      })} />
                  ))}
              </div>
            </div>
          )}
        </>
      )}

      {drill && (
        <OracleMetricDrillModal
          drill={drill}
          range={range}
          onClose={() => setDrill(null)} />
      )}
    </div>
  );
}

// WaitClassesBar moved to shared.tsx (v0.8.391, Stage-2 D3) — the
// cross-engine WaitLockStrip reuses the same stacked bar.

function TablespaceBar({ ts, onClick }: {
  ts: { name: string; usedBytes: number; maxBytes: number; usedPct: number };
  onClick?: () => void;
}) {
  const tone: 'ok' | 'warn' | 'err' =
    ts.usedPct >= 90 ? 'err' : ts.usedPct >= 75 ? 'warn' : 'ok';
  const fill = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--ok)';
  const inner = (
    <div style={{
      display: 'grid', gridTemplateColumns: '120px 1fr 90px 60px 18px', gap: 10,
      alignItems: 'center', fontSize: 11,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
    }}>
      <span style={{ color: 'var(--text)', fontWeight: 600 }}>{ts.name}</span>
      <div style={{
        height: 6, background: 'var(--bg3)', borderRadius: 3, overflow: 'hidden',
      }}>
        <div style={{
          width: `${Math.min(100, ts.usedPct)}%`, height: '100%', background: fill,
        }} />
      </div>
      <span style={{ color: 'var(--text2)', textAlign: 'right' }}>
        {fmtBytes(ts.usedBytes)} / {fmtBytes(ts.maxBytes)}
      </span>
      <span style={{
        color: tone === 'ok' ? 'var(--text2)' : fill,
        textAlign: 'right', fontWeight: 600,
      }}>{ts.usedPct.toFixed(1)}%</span>
      <span aria-hidden style={{
        color: 'var(--text3)', textAlign: 'right',
        opacity: onClick ? 0.7 : 0,
      }}>↗</span>
    </div>
  );
  if (onClick) {
    return (
      <button type="button" onClick={onClick}
        title={`Open ${ts.name} usage chart`}
        style={{
          all: 'unset', display: 'block', cursor: 'pointer',
          padding: '3px 6px', borderRadius: 3,
          transition: 'background 0.12s',
        }}
        onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
        onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
        {inner}
      </button>
    );
  }
  return inner;
}
