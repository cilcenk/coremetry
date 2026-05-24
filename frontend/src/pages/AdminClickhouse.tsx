import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';

// AdminClickhouse — v0.5.329. Datadog-style CH self-stats:
// slow queries, in-flight merges, part hotspots, replication lag.
// Reads from /api/admin/clickhouse (server caches 5s, polls every
// 10s here so the operator can watch merge pressure ease/spike in
// near-real-time). Pauses on document.hidden per CLAUDE.md.

type Slow = {
  query: string; elapsedMs: number; memoryMb: number;
  readRows: number; resultRows: number; eventTimeNs: number; user: string;
};
type Merge = {
  database: string; table: string;
  elapsedSec: number; progressPct: number;
  rowsRead: number; mergedSizeBytes: number;
};
type PartHot = {
  database: string; table: string;
  parts: number; rowsTotal: number; bytesTotal: number;
};
type RepLag = {
  database: string; table: string;
  queueSize: number; absoluteDelaySec: number;
};
type AsyncIns = {
  database: string; table: string;
  totalBytes: number; entriesCount: number;
  firstUpdateMsAgo: number;
};
type ClusterNode = {
  cluster: string; shardNum: number; replicaNum: number;
  hostName: string; hostAddress?: string; port: number; isLocal: boolean;
};
type Topology = {
  mode: 'cluster' | 'standalone';
  configuredCluster?: string;
  database: string;
  connectedHosts?: string[];
  nodes?: ClusterNode[];
  distributedTables: number;
  localReplicated: number;
  plainMergeTree: number;
  zookeeperConnected: boolean;
  // v0.5.419 — resolved per-table shard policy. Operator audits
  // which expression each Distributed wrapper actually got.
  shardPolicy?: Record<string, string>;
  // v0.5.428 — populated when the system.clusters probe itself
  // failed (timeout / disconnect). Empty when the probe completed
  // (whether or not it returned rows). Drives the soft "probe
  // failed" banner so the hard "misconfigured" banner only fires
  // on a genuine empty-result.
  clusterProbeError?: string;
};
type CHHealth = {
  topology: Topology;
  slowQueries: Slow[] | null;
  merges: Merge[] | null;
  partHotspots: PartHot[] | null;
  replicationLag?: RepLag[] | null;
  asyncInserts?: AsyncIns[] | null;
  generatedAt: number;
};

export default function AdminClickhousePage() {
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [data, setData] = useState<CHHealth | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    const fetchOnce = () => {
      api.clickhouseHealth()
        .then(d => { if (!cancelled) setData(d as CHHealth ?? null); })
        .catch(() => { if (!cancelled) setData(null); });
    };
    fetchOnce();
    const id = setInterval(() => { if (!document.hidden) fetchOnce(); }, 10_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  // Highest-volume merge — surfaced in the page header so the
  // operator sees pressure without reading the table.
  const peakMergeSec = data?.merges?.reduce((m, x) => Math.max(m, x.elapsedSec), 0) ?? 0;
  const peakParts = data?.partHotspots?.reduce((m, x) => Math.max(m, x.parts), 0) ?? 0;

  return (
    <>
      <Topbar title="ClickHouse" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
          gap: 12, marginBottom: 18,
        }}>
          <KPI label="Slow queries · 1h" value={fmtNum(data?.slowQueries?.length ?? 0)}
               sub=">500ms" />
          <KPI label="Active merges" value={fmtNum(data?.merges?.length ?? 0)}
               sub={peakMergeSec > 0 ? `peak ${peakMergeSec.toFixed(0)}s` : ''} />
          <KPI label="Part hotspots" value={fmtNum(data?.partHotspots?.length ?? 0)}
               sub={peakParts > 0 ? `max ${peakParts} parts` : ''}
               cls={peakParts > 300 ? 'warn' : peakParts > 600 ? 'err' : ''} />
          <KPI label="Replication lag rows"
               value={fmtNum(data?.replicationLag?.length ?? 0)}
               sub="cluster only" />
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load ClickHouse health" />}

        {data && (
          <>
            <TopologyPanel topology={data.topology} />

            <Section title="Slow queries (>500ms, last 1h)">
              {(!data.slowQueries || data.slowQueries.length === 0)
                ? <EmptyNote text="No slow queries in the last hour" />
                : (
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Time</th>
                          <th>User</th>
                          <th className="num">Elapsed</th>
                          <th className="num">Memory</th>
                          <th className="num">Read rows</th>
                          <th>Query</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.slowQueries.map((q, i) => (
                          <tr key={i}>
                            <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                              {new Date(q.eventTimeNs / 1e6).toLocaleTimeString()}
                            </td>
                            <td className="mono" style={{ fontSize: 11 }}>{q.user || '—'}</td>
                            <td className="num mono">{q.elapsedMs.toFixed(0)} ms</td>
                            <td className="num mono">{q.memoryMb.toFixed(0)} MB</td>
                            <td className="num mono">{fmtNum(q.readRows)}</td>
                            <td className="mono" style={{
                              fontSize: 11, maxWidth: 540,
                              overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                            }} title={q.query}>
                              {q.query.replace(/\s+/g, ' ').slice(0, 200)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            <Section title="In-flight merges">
              {(!data.merges || data.merges.length === 0)
                ? <EmptyNote text="No merges in flight — CH idle or up-to-date" />
                : (
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Database</th><th>Table</th>
                          <th className="num">Elapsed</th>
                          <th className="num">Progress</th>
                          <th className="num">Rows read</th>
                          <th className="num">Merged size</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.merges.map((m, i) => (
                          <tr key={i}>
                            <td className="mono">{m.database}</td>
                            <td className="mono">{m.table}</td>
                            <td className="num mono">{m.elapsedSec.toFixed(1)}s</td>
                            <td className="num mono">{m.progressPct.toFixed(0)}%</td>
                            <td className="num mono">{fmtNum(m.rowsRead)}</td>
                            <td className="num mono">{fmtBytes(m.mergedSizeBytes)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            <Section title="Part hotspots (active parts per table, top 15)">
              {(!data.partHotspots || data.partHotspots.length === 0)
                ? <EmptyNote text="No part data available" />
                : (
                  <div className="table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Database</th><th>Table</th>
                          <th className="num">Parts</th>
                          <th className="num">Rows</th>
                          <th className="num">Bytes</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.partHotspots.map((p, i) => (
                          <tr key={i}>
                            <td className="mono">{p.database}</td>
                            <td className="mono">{p.table}</td>
                            <td className="num mono" style={{
                              color: p.parts > 300 ? 'var(--err)' : p.parts > 150 ? 'var(--warn)' : 'var(--text)',
                              fontWeight: p.parts > 150 ? 600 : 400,
                            }}>{fmtNum(p.parts)}</td>
                            <td className="num mono">{fmtNum(p.rowsTotal)}</td>
                            <td className="num mono">{fmtBytes(p.bytesTotal)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            {data.asyncInserts && data.asyncInserts.length > 0 && (
              <Section title="Async insert buffer">
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Database</th><th>Table</th>
                        <th className="num">Bytes buffered</th>
                        <th className="num">Entries</th>
                        <th className="num">Oldest</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.asyncInserts.map((a, i) => (
                        <tr key={i}>
                          <td className="mono">{a.database}</td>
                          <td className="mono">{a.table}</td>
                          <td className="num mono">{fmtNum(a.totalBytes)}</td>
                          <td className="num mono">{fmtNum(a.entriesCount)}</td>
                          <td className="num mono">{a.firstUpdateMsAgo}ms</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Section>
            )}

            {data.replicationLag && data.replicationLag.length > 0 && (
              <Section title="Replication lag (cluster only)">
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Database</th><th>Table</th>
                        <th className="num">Queue</th>
                        <th className="num">Absolute delay</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.replicationLag.map((r, i) => (
                        <tr key={i}>
                          <td className="mono">{r.database}</td>
                          <td className="mono">{r.table}</td>
                          <td className="num mono">{fmtNum(r.queueSize)}</td>
                          <td className="num mono">{r.absoluteDelaySec}s</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Section>
            )}
          </>
        )}
      </div>
    </>
  );
}

// TopologyPanel — first thing on /admin/clickhouse. Operator
// answers "are we talking to a cluster?" in under a second. The
// banner colour reflects the live agreement between the
// configured cluster name and what system.clusters reports:
//   • green  — configuredCluster set, nodes detected
//   • blue   — standalone install (no cluster configured)
//   • amber  — configuredCluster set but system.clusters is
//              empty → misconfig (env var on the app side,
//              <remote_servers> missing on CH side)
function TopologyPanel({ topology: t }: { topology: Topology }) {
  // v0.5.428 — only flag misconfig when the probe actually
  // returned an empty set. If the probe itself failed
  // (timeout / disconnect), we can't make a misconfig claim —
  // render a softer "probe failed" banner instead.
  const probeFailed = !!t.clusterProbeError;
  const misconfigured = !!t.configuredCluster && (!t.nodes || t.nodes.length === 0) && !probeFailed;
  const bannerCls = misconfigured || probeFailed ? 'warn' : (t.mode === 'cluster' ? 'ok' : 'info');
  const bannerColor =
    bannerCls === 'ok' ? 'var(--ok)' :
    bannerCls === 'warn' ? 'var(--warn)' : 'var(--accent2)';
  const bannerBg =
    bannerCls === 'ok' ? 'rgba(34, 197, 94, 0.08)' :
    bannerCls === 'warn' ? 'rgba(250, 204, 21, 0.10)' : 'rgba(96, 165, 250, 0.08)';

  return (
    <div style={{ marginBottom: 24 }}>
      <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8 }}>Topology</h3>
      <div style={{
        padding: '12px 14px', borderRadius: 6,
        border: `1px solid ${bannerColor}`, background: bannerBg,
        marginBottom: 10,
      }}>
        <div style={{ fontSize: 13, color: 'var(--text)', marginBottom: 4 }}>
          {misconfigured && (
            <>
              <strong style={{ color: 'var(--warn)' }}>⚠ Cluster misconfigured</strong> —
              cluster <code className="mono">{t.configuredCluster}</code> is configured
              but not present in <code>system.clusters</code>. Check the CH server's
              <code> &lt;remote_servers&gt;</code> block.
            </>
          )}
          {probeFailed && (
            <>
              <strong style={{ color: 'var(--warn)' }}>⚠ Cluster probe failed</strong> —
              couldn't confirm cluster <code className="mono">{t.configuredCluster}</code>{' '}
              via <code>system.clusters</code>: <span className="mono" style={{ fontSize: 11 }}>{t.clusterProbeError}</span>.
              {' '}Usually transient (CH busy). Retry on next refresh.
            </>
          )}
          {!misconfigured && t.mode === 'cluster' && (
            <>
              <strong style={{ color: 'var(--ok)' }}>● Cluster mode</strong> —
              connected to cluster <code className="mono">{t.configuredCluster}</code>
              {' '}with <strong>{t.nodes?.length ?? 0}</strong> registered node{(t.nodes?.length ?? 0) === 1 ? '' : 's'}.
            </>
          )}
          {!misconfigured && t.mode === 'standalone' && (
            <>
              <strong style={{ color: 'var(--accent2)' }}>● Standalone mode</strong> —
              no <code>ON CLUSTER</code> name configured; Coremetry is
              writing to a single ClickHouse server.
            </>
          )}
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Database: <code className="mono">{t.database}</code>
          {' · '}
          Driver hosts: <code className="mono">{(t.connectedHosts ?? []).join(', ') || '—'}</code>
        </div>
      </div>

      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
        gap: 10, marginBottom: 10,
      }}>
        <MiniStat label="Distributed tables" value={fmtNum(t.distributedTables)}
                  hint={t.distributedTables > 0 ? 'cluster wrapper in use' : 'no Distributed wrapper'} />
        <MiniStat label="Replicated local tables" value={fmtNum(t.localReplicated)}
                  hint={t.localReplicated > 0 ? 'ReplicatedMergeTree' : 'no replicas'} />
        <MiniStat label="Plain MergeTree tables" value={fmtNum(t.plainMergeTree)} />
        <MiniStat label="ZooKeeper / Keeper" value={t.zookeeperConnected ? 'connected' : 'not detected'}
                  cls={t.mode === 'cluster' && !t.zookeeperConnected ? 'warn' : ''}
                  hint={t.mode === 'cluster' && !t.zookeeperConnected
                    ? 'cluster requires ZK/Keeper'
                    : t.mode === 'standalone' ? 'not required' : ''} />
      </div>

      {t.nodes && t.nodes.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th className="num">Shard</th>
                <th className="num">Replica</th>
                <th>Host</th>
                <th>Address</th>
                <th className="num">Port</th>
                <th>Local</th>
              </tr>
            </thead>
            <tbody>
              {t.nodes.map((n, i) => (
                <tr key={i}>
                  <td className="num mono">{n.shardNum}</td>
                  <td className="num mono">{n.replicaNum}</td>
                  <td className="mono">{n.hostName}</td>
                  <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                    {n.hostAddress || '—'}
                  </td>
                  <td className="num mono">{n.port}</td>
                  <td>
                    {n.isLocal
                      ? <span style={{ color: 'var(--ok)' }}>● self</span>
                      : <span style={{ color: 'var(--text3)' }}>—</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* v0.5.419 — resolved per-table shard policy. Operator
          confirms which expression each Distributed wrapper got
          without `SHOW CREATE TABLE` round-trips. */}
      {t.shardPolicy && Object.keys(t.shardPolicy).length > 0 && (
        <div style={{ marginTop: 12 }}>
          <div style={{
            fontSize: 10, fontWeight: 700,
            textTransform: 'uppercase', letterSpacing: 0.4,
            color: 'var(--text2)', marginBottom: 6,
          }}>
            Shard policy (resolved)
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Table</th>
                  <th>Shard expression</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(t.shardPolicy)
                  .sort(([a], [b]) => a.localeCompare(b))
                  .map(([table, expr]) => (
                    <tr key={table}>
                      <td className="mono">{table}</td>
                      <td className="mono" style={{
                        fontSize: 11,
                        color: expr === 'rand()' ? 'var(--text3)' : 'var(--text)',
                      }}>{expr}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
          <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 6 }}>
            Resolution: <code>COREMETRY_CH_SHARD_KEY</code> env (uniform override) →
            built-in Datadog-style per-table defaults → <code>rand()</code>.
            Change requires a one-time <code>COREMETRY_CH_RESET_SCHEMA=1</code> boot
            since <code>ENGINE = Distributed(…, shard_key)</code> freezes the
            expression at table creation.
          </div>
        </div>
      )}
    </div>
  );
}

function MiniStat({ label, value, hint, cls }: {
  label: string; value: string; hint?: string; cls?: string;
}) {
  return (
    <div style={{
      padding: '8px 12px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: 16, fontWeight: 600, marginTop: 2,
        color: cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {hint && (
        <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 2 }}>{hint}</div>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 24 }}>
      <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8 }}>{title}</h3>
      {children}
    </div>
  );
}

function EmptyNote({ text }: { text: string }) {
  return (
    <div style={{
      padding: '14px 16px', borderRadius: 6,
      background: 'var(--bg2)', border: '1px dashed var(--border)',
      fontSize: 12, color: 'var(--text3)',
    }}>{text}</div>
  );
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '10px 14px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: 22, fontWeight: 700, marginTop: 4,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>
      )}
    </div>
  );
}

function fmtBytes(n: number): string {
  if (!n) return '0';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}
