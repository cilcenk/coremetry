import { useEffect, useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { TimeRange, MySQLMetrics } from '@/lib/types';
import {
  Stat, GaugeStat, OracleMetricDrillModal, TopSQLSection,
  PanelHeader, PanelErr, SubHeader,
  type OracleDrill,
} from './shared';

// MySQLPanel — drill-down for one MySQL/MariaDB instance.
// Split out of the DependenciesTable monolith (v0.8.252 refactor)
// verbatim.
export function MySQLPanel({ instance, range }: { instance: string; range: TimeRange }) {
  const [data, setData] = useState<MySQLMetrics | null | undefined>(undefined);
  const [drill, setDrill] = useState<OracleDrill | null>(null);
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.mysqlMetrics(instance, from, to)
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [instance, range]);
  return (
    <div style={{
      marginTop: 6, marginBottom: 14, padding: 12, borderRadius: 6,
      background: 'rgba(0,117,143,0.05)',
      border: '1px solid rgba(33,160,160,0.25)',
    }}>
      <PanelHeader engineLabel="MySQL receiver" instance={instance}
        status={data?.status} color="#21a0a0" />
      {data === undefined && <Spinner />}
      {data === null && <PanelErr />}
      {data && (
        <>
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 8, marginBottom: 12,
          }}>
            <GaugeStat label="Connections"
              usage={data.connections.usage} limit={data.connections.limit}
              onClick={() => setDrill({ metric: 'mysql.connection.count', label: 'Connections' })} />
            <Stat label="Threads connected" value={fmtNum(data.threads.connected)}
              sub={`${fmtNum(data.threads.running)} running`}
              onClick={() => setDrill({ metric: 'mysql.threads', label: 'Threads' })} />
            <Stat label="Questions/s" value={fmtNum(data.questionsPerSec)}
              onClick={() => setDrill({ metric: 'mysql.questions', label: 'Questions', unit: '/s' })} />
            <Stat label="Slow queries/s" value={data.slowQueriesPerSec.toFixed(2)}
              tone={data.slowQueriesPerSec > 1 ? 'err' : data.slowQueriesPerSec > 0.1 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'mysql.slow_queries', label: 'Slow queries', unit: '/s' })} />
            <Stat label="Row-lock waits/s" value={data.rowLockWaitsPerSec.toFixed(2)}
              tone={data.rowLockWaitsPerSec > 1 ? 'err' : data.rowLockWaitsPerSec > 0.2 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'mysql.row_locks', label: 'Row-lock waits', unit: '/s' })} />
            <Stat label="Row-lock time" value={`${data.rowLockTimeSec.toFixed(1)}s`}
              tone={data.rowLockTimeSec > 1 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'mysql.locks.time', label: 'Row-lock time', unit: 's' })} />
            <Stat label="Opened tables/s" value={data.openedTablesPerSec.toFixed(2)}
              tone={data.openedTablesPerSec > 1 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'mysql.opened_resources.table', label: 'Opened tables', unit: '/s' })} />
            <Stat label="Buffer pool usage" value={`${data.bufferPool.usagePct.toFixed(1)}%`}
              sub={`${data.bufferPool.dirtyPct.toFixed(1)}% dirty`}
              tone={data.bufferPool.usagePct >= 95 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'mysql.buffer_pool.pages.data', label: 'Buffer pool pages' })} />
            <Stat label="Tmp disk tables/s" value={data.tmpDiskTablesPerSec.toFixed(2)}
              tone={data.tmpDiskTablesPerSec > 1 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'mysql.tmp_resources.disk', label: 'Tmp disk tables', unit: '/s' })} />
            <Stat label="Inserts/s" value={fmtNum(data.rowOps.insertPerSec)}
              onClick={() => setDrill({ metric: 'mysql.row_operations.insert', label: 'Inserts', unit: '/s' })} />
            <Stat label="Updates/s" value={fmtNum(data.rowOps.updatePerSec)}
              onClick={() => setDrill({ metric: 'mysql.row_operations.update', label: 'Updates', unit: '/s' })} />
            <Stat label="Deletes/s" value={fmtNum(data.rowOps.deletePerSec)}
              onClick={() => setDrill({ metric: 'mysql.row_operations.delete', label: 'Deletes', unit: '/s' })} />
            <Stat label="Selects/s" value={fmtNum(data.rowOps.selectPerSec)}
              onClick={() => setDrill({ metric: 'mysql.row_operations.select', label: 'Selects', unit: '/s' })} />
            <Stat label="Replica delay" value={`${data.replicaDelaySec.toFixed(1)}s`}
              tone={data.replicaDelaySec > 10 ? 'err'
                  : data.replicaDelaySec > 2 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'mysql.replica.time_behind_source', label: 'Replica delay', unit: 's' })} />
            <Stat label="Threads created/s" value={fmtNum(data.threads.createdPerSec)}
              tone={data.threads.createdPerSec > 1 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'mysql.threads.created', label: 'Threads created', unit: '/s' })} />
          </div>
          <div>
            <SubHeader label="Handlers (index efficiency proxy)" />
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              <HandlerChip label="read_first" v={data.handlers.readFirstPerSec} />
              <HandlerChip label="read_key" v={data.handlers.readKeyPerSec} />
              <HandlerChip label="read_next" v={data.handlers.readNextPerSec} />
              <HandlerChip label="read_rnd_next" v={data.handlers.readRndNextPerSec}
                warn={data.handlers.readRndNextPerSec > data.handlers.readKeyPerSec} />
              <HandlerChip label="write" v={data.handlers.writePerSec} />
            </div>
            {data.handlers.readRndNextPerSec > data.handlers.readKeyPerSec && (
              <div style={{ fontSize: 11, color: 'var(--warn)', marginTop: 6 }}>
                read_rnd_next exceeds read_key — full-table scans dominating index reads.
                Check for missing indexes or stale query plans.
              </div>
            )}
          </div>

          <div style={{ marginTop: 12 }}>
            <TopSQLSection rows={data.topSQL} instance={instance}
              hint="Enable performance_schema statement instrumentation + the receiver's statement scrape to populate." />
          </div>
        </>
      )}
      {drill && (
        <OracleMetricDrillModal drill={drill} range={range} onClose={() => setDrill(null)} />
      )}
    </div>
  );
}

function HandlerChip({ label, v, warn }: { label: string; v: number; warn?: boolean }) {
  return (
    <span style={{
      fontSize: 11, padding: '3px 8px', borderRadius: 3,
      background: warn ? 'color-mix(in srgb, var(--warn) 15%, transparent)' : 'var(--bg3)',
      color: warn ? 'var(--warn)' : 'var(--text2)',
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
    }}>
      {label} <span style={{ opacity: 0.7 }}>{fmtNum(v)}/s</span>
    </span>
  );
}
