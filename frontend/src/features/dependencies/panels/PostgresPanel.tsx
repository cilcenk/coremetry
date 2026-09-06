import { useEffect, useState } from 'react';
import { rowActivation } from '@/lib/a11y'; // v0.10.455 (dış denetim D3 dilim 3)
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { TimeRange, PostgresMetrics } from '@/lib/types';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// v0.9.873 (tutarlılık denetimi BT15) — sabit `sizeBytes desc` sıralaması
// primitife devredildi; artık aynı sıra VARSAYILAN, ama Backends veya
// Rollbacks/s'e göre sıralamak da mümkün (bir PG kümesinde "hangi veritabanı
// rollback üretiyor" sorusu boyuta göre sıralı listede kayboluyordu).
type PGDatabase = PostgresMetrics['databases'][number];

// mT8 (v0.9.907) — motor MARKA rengi, tema token'ı değil: bir PostgreSQL
// panelinin mavisi temaya göre değişmemeli (Dalga 4 kapsam sınırı bunu
// bilinçli istisna sayıyor). Sızıntı rengin kendisi DEĞİLDİ; zemin ve
// kenarlık tint'lerinin ondan TÜREMEYİP ayrı ayrı yazılmış olmasıydı —
// üç literal, biri değişince diğer ikisi sessizce ayrışıyordu.
const PG_BRAND = '#5b8fb9';

const PG_DB_COLS: DataTableColumn<PGDatabase>[] = [
  { id: 'name',      label: 'Name',         sortValue: d => d.name,            naturalDir: 'asc', flex: true },
  { id: 'size',      label: 'Size',         sortValue: d => d.sizeBytes,       numeric: true, width: 100 },
  { id: 'backends',  label: 'Backends',     sortValue: d => d.backendCount,    numeric: true, width: 110 },
  { id: 'commits',   label: 'Commits/s',    sortValue: d => d.commitsPerSec,   numeric: true, width: 115 },
  { id: 'rollbacks', label: 'Rollbacks/s',  sortValue: d => d.rollbacksPerSec, numeric: true, width: 125 },
];
import {
  Stat, GaugeStat, OracleMetricDrillModal, TopSQLSection,
  PanelHeader, PanelErr, SubHeader, fmtBytes,
  type OracleDrill, DegradedBand } from './shared';

// PostgresPanel — drill-down for one Postgres instance, mirrors
// OraclePanel's shape (status badge + KPI tiles + per-DB table).
// Tile clicks open the same metric-chart modal used by Oracle.
export function PostgresPanel({ instance, range }: { instance: string; range: TimeRange }) {
  const [data, setData] = useState<PostgresMetrics | null | undefined>(undefined);
  const [drill, setDrill] = useState<OracleDrill | null>(null);
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.postgresMetrics(instance, from, to)
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [instance, range]);
  const dbDt = useDataTable<PGDatabase>({
    storageKey: 'deps-postgres-databases', columns: PG_DB_COLS,
    rows: data?.databases ?? [],
    initialSort: { id: 'size', dir: 'desc' },
  });
  return (
    <div style={{
      marginTop: 6, marginBottom: 14, padding: 12, borderRadius: 6,
      background: `color-mix(in srgb, ${PG_BRAND} 5%, transparent)`,
      border: `1px solid color-mix(in srgb, ${PG_BRAND} 25%, transparent)`,
    }}>
      <PanelHeader engineLabel="PostgreSQL receiver" instance={instance}
        status={data?.status} color={PG_BRAND} range={range} />
      {data === undefined && <Spinner />}
      {data === null && <PanelErr />}
      {data && (
        <>
          <DegradedBand reason={data.degradedReason} />
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 8, marginBottom: 12,
          }}>
            <GaugeStat label="Backends"
              usage={data.backends.usage} limit={data.backends.limit}
              onClick={() => setDrill({ metric: 'postgresql.backends', label: 'Backends' })} />
            <Stat label="Commits/s" value={fmtNum(data.commitsPerSec)}
              onClick={() => setDrill({ metric: 'postgresql.commits', label: 'Commits', unit: '/s' })} />
            <Stat label="Rollbacks/s" value={fmtNum(data.rollbacksPerSec)}
              tone={data.rollbacksPerSec > data.commitsPerSec * 0.05 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'postgresql.rollbacks', label: 'Rollbacks', unit: '/s' })} />
            <Stat label="Deadlocks/s" value={data.deadlocksPerSec.toFixed(3)}
              tone={data.deadlocksPerSec > 0.1 ? 'err' : data.deadlocksPerSec > 0 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'postgresql.deadlocks', label: 'Deadlocks', unit: '/s' })} />
            <Stat label="Cache hit" value={`${data.cacheHitPct.toFixed(1)}%`}
              tone={data.cacheHitPct < 95 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'postgresql.blocks_hit', label: 'Cache hits vs reads' })} />
            <Stat label="Blocks read/s" value={fmtNum(data.blocksReadPerSec)}
              onClick={() => setDrill({ metric: 'postgresql.blocks_read', label: 'Blocks read', unit: '/s' })} />
            <Stat label="Temp files/s" value={data.tempFilesPerSec.toFixed(2)}
              tone={data.tempFilesPerSec > 1 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'postgresql.temp_files', label: 'Temp files', unit: '/s' })} />
            <Stat label="WAL lag" value={fmtBytes(data.walLagBytes)}
              tone={data.walLagBytes > 64 * 1024 * 1024 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'postgresql.wal.lag', label: 'WAL lag', unit: 'B' })} />
            <Stat label="Replication delay" value={`${data.replicationDelaySec.toFixed(1)}s`}
              tone={data.replicationDelaySec > 10 ? 'err'
                  : data.replicationDelaySec > 2 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'postgresql.replication.data_delay', label: 'Replication delay', unit: 's' })} />
            <Stat label="Bgwriter alloc/s" value={fmtNum(data.bgwriter.buffersAllocatedPerSec)}
              onClick={() => setDrill({ metric: 'postgresql.bgwriter.buffers.allocated', label: 'Bgwriter buffers allocated', unit: '/s' })} />
            <Stat label="Buffers by backend/s" value={fmtNum(data.bgwriter.buffersBackendPerSec)}
              tone={data.bgwriter.buffersBackendPerSec > data.bgwriter.buffersCheckpointPerSec ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'postgresql.bgwriter.buffers.writes', label: 'Buffers written by backend', unit: '/s' })} />
            <Stat label="Temp bytes/s" value={fmtBytes(data.tempBytesPerSec)}
              tone={data.tempBytesPerSec > 0 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'postgresql.temp.io', label: 'Temp bytes', unit: 'B' })} />
            <Stat label="WAL age" value={`${data.walAgeSec.toFixed(0)}s`}
              tone={data.walAgeSec > 300 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'postgresql.wal.age', label: 'WAL age', unit: 's' })} />
          </div>

          {data.databases.length > 0 && (
            <div style={{ marginBottom: 12 }}>
              <SubHeader label={`Databases (${data.databases.length})`} />
              <div className="table-wrap is-scroll" style={{ maxHeight: 240, overflowY: 'auto' }}>
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={dbDt} />
                  <DataTableHead dt={dbDt} />
                  <tbody>
                    {dbDt.sortedRows.map(d => (
                      <tr key={d.name}
                        {...rowActivation(() => setDrill({
                          metric: 'postgresql.database.size',
                          label: `Database · ${d.name}`,
                          unit: 'B',
                          filters: [{ k: 'database', op: '=', v: [d.name] }],
                        }))}
                        style={{ cursor: 'pointer' }}>
                        <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11, fontWeight: 600 }}>{d.name}</td>
                        <td className="num mono">{fmtBytes(d.sizeBytes)}</td>
                        <td className="num mono">{fmtNum(d.backendCount)}</td>
                        <td className="num mono">{fmtNum(d.commitsPerSec)}</td>
                        <td className="num mono">{fmtNum(d.rollbacksPerSec)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {data.locks.length > 0 && (
            <div style={{ marginBottom: 12 }}>
              <SubHeader label="Locks by mode" />
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {data.locks.map(l => (
                  <span key={l.mode} style={{
                    fontSize: 11, padding: '3px 8px', borderRadius: 3,
                    background: 'var(--bg3)', color: 'var(--text2)',
                    fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                  }}>
                    {l.mode} <span style={{ color: 'var(--text3)' }}>{fmtNum(l.count)}</span>
                  </span>
                ))}
              </div>
            </div>
          )}

          <TopSQLSection rows={data.topSQL} instance={instance} range={range}
            hint="Enable the pg_stat_statements extension + the receiver's statement scrape to populate." />
        </>
      )}
      {drill && (
        <OracleMetricDrillModal drill={drill} range={range} instance={instance} engine="postgresql" onClose={() => setDrill(null)} />
      )}
    </div>
  );
}
