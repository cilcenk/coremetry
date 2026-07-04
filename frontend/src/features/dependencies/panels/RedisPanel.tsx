import { useEffect, useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { TimeRange, RedisMetrics } from '@/lib/types';
import {
  Stat, OracleMetricDrillModal,
  PanelHeader, PanelErr, SubHeader, fmtBytes, fmtDuration,
  type OracleDrill,
} from './shared';

// RedisPanel — drill-down for one Redis instance.
// Split out of the DependenciesTable monolith (v0.8.252 refactor)
// verbatim.
export function RedisPanel({ instance, range }: { instance: string; range: TimeRange }) {
  const [data, setData] = useState<RedisMetrics | null | undefined>(undefined);
  const [drill, setDrill] = useState<OracleDrill | null>(null);
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.redisMetrics(instance, from, to)
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [instance, range]);
  return (
    <div style={{
      marginTop: 6, marginBottom: 14, padding: 12, borderRadius: 6,
      background: 'rgba(220,38,38,0.05)',
      border: '1px solid rgba(220,38,38,0.25)',
    }}>
      <PanelHeader engineLabel="Redis receiver" instance={instance}
        status={data?.status} color="#dc2626"
        extraBadge={data?.role && data.role !== 'unknown' ? data.role : undefined} />
      {data === undefined && <Spinner />}
      {data === null && <PanelErr />}
      {data && (
        <>
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 8, marginBottom: 12,
          }}>
            <Stat label="Clients" value={fmtNum(data.clients.connected)}
              sub={data.clients.blocked > 0 ? `${fmtNum(data.clients.blocked)} blocked` : undefined}
              onClick={() => setDrill({ metric: 'redis.clients.connected', label: 'Clients connected' })} />
            <Stat label="Memory used" value={fmtBytes(data.memory.usedBytes)}
              sub={data.memory.maxBytes > 0 ? `${data.memory.usagePct.toFixed(1)}% of max` : undefined}
              tone={data.memory.usagePct >= 90 ? 'err' : data.memory.usagePct >= 75 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'redis.memory.used', label: 'Memory used', unit: 'B' })} />
            <Stat label="Fragmentation" value={data.memory.fragmentationRatio.toFixed(2)}
              tone={data.memory.fragmentationRatio > 1.5 ? 'warn'
                  : data.memory.fragmentationRatio > 5 ? 'err' : 'ok'}
              onClick={() => setDrill({ metric: 'redis.memory.fragmentation_ratio', label: 'Memory fragmentation ratio' })} />
            <Stat label="Commands/s" value={fmtNum(data.commandsPerSec)}
              onClick={() => setDrill({ metric: 'redis.commands', label: 'Commands', unit: '/s' })} />
            <Stat label="Hit rate" value={`${data.hitRatePct.toFixed(1)}%`}
              tone={data.hitRatePct < 90 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'redis.keyspace.hits', label: 'Keyspace hits' })} />
            <Stat label="Keys evicted/s" value={data.keysEvictedPerSec.toFixed(2)}
              tone={data.keysEvictedPerSec > 0.5 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'redis.keys.evicted', label: 'Keys evicted', unit: '/s' })} />
            <Stat label="Keys expired/s" value={fmtNum(data.keysExpiredPerSec)}
              onClick={() => setDrill({ metric: 'redis.keys.expired', label: 'Keys expired', unit: '/s' })} />
            <Stat label="Net in/s" value={fmtBytes(data.netInputBytesPerSec)}
              onClick={() => setDrill({ metric: 'redis.net.input', label: 'Net input', unit: 'B' })} />
            <Stat label="Net out/s" value={fmtBytes(data.netOutputBytesPerSec)}
              onClick={() => setDrill({ metric: 'redis.net.output', label: 'Net output', unit: 'B' })} />
            <Stat label="Repl lag" value={fmtBytes(data.replicationLagBytes)}
              tone={data.replicationLagBytes > 64 * 1024 * 1024 ? 'warn' : 'ok'}
              onClick={() => setDrill({ metric: 'redis.replication.replica_offset', label: 'Replication lag', unit: 'B' })} />
            <Stat label="Slowlog" value={fmtNum(data.slowlogEntries)}
              tone={data.slowlogEntries > 100 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'redis.slowlog.length', label: 'Slowlog entries' })} />
            <Stat label="Conn rejected/s" value={data.connectionsRejectedPerSec.toFixed(2)}
              tone={data.connectionsRejectedPerSec > 0 ? 'err' : 'ok'}
              onClick={() => setDrill({ metric: 'redis.connections.rejected', label: 'Connections rejected', unit: '/s' })} />
            <Stat label="Memory RSS" value={fmtBytes(data.memory.rssBytes)}
              onClick={() => setDrill({ metric: 'redis.memory.rss', label: 'Memory RSS', unit: 'B' })} />
            <Stat label="Peak memory" value={fmtBytes(data.memory.peakBytes)}
              onClick={() => setDrill({ metric: 'redis.memory.peak', label: 'Peak memory', unit: 'B' })} />
            <Stat label="Lua memory" value={fmtBytes(data.memory.luaBytes)}
              onClick={() => setDrill({ metric: 'redis.memory.lua', label: 'Lua memory', unit: 'B' })} />
            <Stat label="Unsaved changes" value={fmtNum(data.changesSinceLastSave)}
              sub="since last save"
              tone={data.changesSinceLastSave > 10000 ? 'warn' : undefined}
              onClick={() => setDrill({ metric: 'redis.rdb.changes_since_last_save', label: 'Changes since last save' })} />
            <Stat label="Uptime" value={fmtDuration(data.uptimeSec)}
              onClick={() => setDrill({ metric: 'redis.uptime', label: 'Uptime', unit: 's' })} />
            <Stat label="Max client in-buf" value={fmtBytes(data.clients.maxInputBufferBytes)}
              onClick={() => setDrill({ metric: 'redis.clients.max_input_buffer', label: 'Max client input buffer', unit: 'B' })} />
            <Stat label="Max client out-buf" value={fmtBytes(data.clients.maxOutputBufferBytes)}
              onClick={() => setDrill({ metric: 'redis.clients.max_output_buffer', label: 'Max client output buffer', unit: 'B' })} />
          </div>
          {data.keyspaces.length > 0 && (
            <div>
              <SubHeader label={`Keyspaces (${data.keyspaces.length})`} />
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {[...data.keyspaces].sort((a, b) => b.keys - a.keys).map(k => (
                  <button key={k.name} type="button"
                    onClick={() => setDrill({
                      metric: 'redis.db.keys',
                      label: `Keyspace ${k.name}`,
                      filters: [{ key: 'db', op: '=', value: k.name }],
                    })}
                    style={{
                      all: 'unset', cursor: 'pointer',
                      fontSize: 11, padding: '4px 10px', borderRadius: 3,
                      background: 'var(--bg3)', color: 'var(--text2)',
                      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      transition: 'background 0.12s',
                    }}
                    onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg2)')}
                    onMouseLeave={e => (e.currentTarget.style.background = 'var(--bg3)')}>
                    <span style={{ fontWeight: 600, color: 'var(--text)' }}>{k.name}</span>
                    {' '}<span>{fmtNum(k.keys)} keys</span>
                    {k.expires > 0 && (
                      <span style={{ color: 'var(--text3)' }}> · {fmtNum(k.expires)} expires</span>
                    )}
                  </button>
                ))}
              </div>
            </div>
          )}
        </>
      )}
      {drill && (
        <OracleMetricDrillModal drill={drill} range={range} onClose={() => setDrill(null)} />
      )}
    </div>
  );
}
