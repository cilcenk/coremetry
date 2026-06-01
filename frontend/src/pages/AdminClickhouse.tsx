import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
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
  // v0.5.439 — set when the live probe failed but a recent
  // cached snapshot filled `nodes` in. Renders an inline "last
  // refreshed N min ago" pill instead of the warn banner so a
  // transient timeout doesn't flash a red box at the operator.
  // Age is the cache-snapshot age in milliseconds.
  clusterNodesStale?: boolean;
  clusterNodesAgeMs?: number;
};
// v0.6.22 — in-flight mutations panel. Healthy queue is empty;
// growing queue → time to swap the offending ALTER UPDATE/
// DELETE pattern for a tombstone or ReplacingMergeTree shape.
type Mutation = {
  database: string;
  table: string;
  command: string;
  parts: number;
  elapsedMs: number;
  latestFail?: string;
};

type CHHealth = {
  topology: Topology;
  slowQueries: Slow[] | null;
  merges: Merge[] | null;
  partHotspots: PartHot[] | null;
  replicationLag?: RepLag[] | null;
  asyncInserts?: AsyncIns[] | null;
  mutations?: Mutation[] | null;
  generatedAt: number;
};

// Shard-policy table iterates a Record<table, expr>; flatten to a
// row type so it can ride the shared sortable + resizable primitive.
type ShardPolicyRow = { table: string; expr: string };

// Column defs for the shared DataTable primitive. Body cell ORDER on
// each table must match its COLS order. Free-text columns (Query /
// Command / Failure / shard expr) rely on the global td ellipsis under
// table-layout:fixed — no per-cell maxWidth/nowrap needed.
const SLOW_COLS: DataTableColumn<Slow>[] = [
  { id: 'time',     label: 'Time',      sortValue: q => q.eventTimeNs, naturalDir: 'desc', width: 110 },
  { id: 'user',     label: 'User',      sortValue: q => q.user ?? '',  naturalDir: 'asc',  width: 120 },
  { id: 'elapsed',  label: 'Elapsed',   sortValue: q => q.elapsedMs, numeric: true, naturalDir: 'desc', width: 100 },
  { id: 'memory',   label: 'Memory',    sortValue: q => q.memoryMb,  numeric: true, naturalDir: 'desc', width: 100 },
  { id: 'readRows', label: 'Read rows', sortValue: q => q.readRows,  numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'query',    label: 'Query',     sortValue: q => q.query,     naturalDir: 'asc',  width: 540 },
];

const MERGE_COLS: DataTableColumn<Merge>[] = [
  { id: 'database', label: 'Database',    sortValue: m => m.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',       sortValue: m => m.table,    naturalDir: 'asc',  width: 200 },
  { id: 'elapsed',  label: 'Elapsed',     sortValue: m => m.elapsedSec,      numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'progress', label: 'Progress',    sortValue: m => m.progressPct,     numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'rowsRead', label: 'Rows read',   sortValue: m => m.rowsRead,        numeric: true, naturalDir: 'desc', width: 130 },
  { id: 'merged',   label: 'Merged size', sortValue: m => m.mergedSizeBytes, numeric: true, naturalDir: 'desc', width: 130 },
];

const PARTHOT_COLS: DataTableColumn<PartHot>[] = [
  { id: 'database', label: 'Database', sortValue: p => p.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',    sortValue: p => p.table,    naturalDir: 'asc',  width: 240 },
  { id: 'parts',    label: 'Parts',    sortValue: p => p.parts,      numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'rows',     label: 'Rows',     sortValue: p => p.rowsTotal,  numeric: true, naturalDir: 'desc', width: 140 },
  { id: 'bytes',    label: 'Bytes',    sortValue: p => p.bytesTotal, numeric: true, naturalDir: 'desc', width: 140 },
];

const ASYNC_COLS: DataTableColumn<AsyncIns>[] = [
  { id: 'database', label: 'Database',       sortValue: a => a.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',          sortValue: a => a.table,    naturalDir: 'asc',  width: 240 },
  { id: 'bytes',    label: 'Bytes buffered', sortValue: a => a.totalBytes,      numeric: true, naturalDir: 'desc', width: 150 },
  { id: 'entries',  label: 'Entries',        sortValue: a => a.entriesCount,    numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'oldest',   label: 'Oldest',         sortValue: a => a.firstUpdateMsAgo, numeric: true, naturalDir: 'desc', width: 110 },
];

const MUTATION_COLS: DataTableColumn<Mutation>[] = [
  { id: 'table',   label: 'Table',          sortValue: m => `${m.database}.${m.table}`, naturalDir: 'asc',  width: 240 },
  { id: 'parts',   label: 'Parts left',     sortValue: m => m.parts,     numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'elapsed', label: 'Elapsed',        sortValue: m => m.elapsedMs, numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'command', label: 'Command',        sortValue: m => m.command,   naturalDir: 'asc', width: 360 },
  { id: 'failure', label: 'Latest failure', sortValue: m => m.latestFail ?? '', naturalDir: 'asc', width: 240 },
];

const REPLAG_COLS: DataTableColumn<RepLag>[] = [
  { id: 'database', label: 'Database',       sortValue: r => r.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',          sortValue: r => r.table,    naturalDir: 'asc',  width: 240 },
  { id: 'queue',    label: 'Queue',          sortValue: r => r.queueSize,        numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'delay',    label: 'Absolute delay', sortValue: r => r.absoluteDelaySec, numeric: true, naturalDir: 'desc', width: 140 },
];

const NODE_COLS: DataTableColumn<ClusterNode>[] = [
  { id: 'shard',   label: 'Shard',   sortValue: n => n.shardNum,   numeric: true, naturalDir: 'asc', width: 90 },
  { id: 'replica', label: 'Replica', sortValue: n => n.replicaNum, numeric: true, naturalDir: 'asc', width: 90 },
  { id: 'host',    label: 'Host',    sortValue: n => n.hostName,        naturalDir: 'asc', width: 220 },
  { id: 'address', label: 'Address', sortValue: n => n.hostAddress ?? '', naturalDir: 'asc', width: 200 },
  { id: 'port',    label: 'Port',    sortValue: n => n.port, numeric: true, naturalDir: 'asc', width: 90 },
  { id: 'local',   label: 'Local',   sortValue: n => (n.isLocal ? 1 : 0), numeric: false, naturalDir: 'desc', width: 90 },
];

const SHARD_POLICY_COLS: DataTableColumn<ShardPolicyRow>[] = [
  { id: 'table', label: 'Table',            sortValue: r => r.table, naturalDir: 'asc', width: 240 },
  { id: 'expr',  label: 'Shard expression', sortValue: r => r.expr,  naturalDir: 'asc', width: 400 },
];

export default function AdminClickhousePage() {
  const [range, setRange] = useUrlRange('30m');
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

  // Shared sortable + resizable tables. Hooks are UNCONDITIONAL
  // (rules-of-hooks) — they live above the data === undefined/null
  // render branches and feed each table its already-fetched array.
  const slowDt = useDataTable<Slow>({
    storageKey: 'ch-slowqueries', columns: SLOW_COLS,
    rows: data?.slowQueries ?? [], initialSort: { id: 'elapsed', dir: 'desc' },
  });
  const mergeDt = useDataTable<Merge>({
    storageKey: 'ch-merges', columns: MERGE_COLS,
    rows: data?.merges ?? [], initialSort: { id: 'elapsed', dir: 'desc' },
  });
  const partDt = useDataTable<PartHot>({
    storageKey: 'ch-parthotspots', columns: PARTHOT_COLS,
    rows: data?.partHotspots ?? [], initialSort: { id: 'parts', dir: 'desc' },
  });
  const asyncDt = useDataTable<AsyncIns>({
    storageKey: 'ch-asyncinserts', columns: ASYNC_COLS,
    rows: data?.asyncInserts ?? [], initialSort: { id: 'bytes', dir: 'desc' },
  });
  const mutationDt = useDataTable<Mutation>({
    storageKey: 'ch-mutations', columns: MUTATION_COLS,
    rows: data?.mutations ?? [], initialSort: { id: 'parts', dir: 'desc' },
  });
  const repLagDt = useDataTable<RepLag>({
    storageKey: 'ch-replicationlag', columns: REPLAG_COLS,
    rows: data?.replicationLag ?? [], initialSort: { id: 'delay', dir: 'desc' },
  });

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
          <KPI label="Pending mutations"
               value={fmtNum(data?.mutations?.length ?? 0)}
               sub="ALTER … DELETE/UPDATE"
               cls={(data?.mutations?.length ?? 0) > 0 ? 'warn' : ''} />
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
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={slowDt} />
                      <DataTableHead dt={slowDt} />
                      <tbody>
                        {slowDt.sortedRows.map((q, i) => (
                          <tr key={i}>
                            <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                              {new Date(q.eventTimeNs / 1e6).toLocaleTimeString()}
                            </td>
                            <td className="mono" style={{ fontSize: 11 }}>{q.user || '—'}</td>
                            <td className="num mono">{q.elapsedMs.toFixed(0)} ms</td>
                            <td className="num mono">{q.memoryMb.toFixed(0)} MB</td>
                            <td className="num mono">{fmtNum(q.readRows)}</td>
                            <td className="mono" style={{ fontSize: 11 }} title={q.query}>
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
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={mergeDt} />
                      <DataTableHead dt={mergeDt} />
                      <tbody>
                        {mergeDt.sortedRows.map((m, i) => (
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
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={partDt} />
                      <DataTableHead dt={partDt} />
                      <tbody>
                        {partDt.sortedRows.map((p, i) => (
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
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={asyncDt} />
                    <DataTableHead dt={asyncDt} />
                    <tbody>
                      {asyncDt.sortedRows.map((a, i) => (
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

            {/* v0.6.22 — pending mutations panel. Empty on a
                healthy install; rows here mean an ALTER … DELETE/
                UPDATE is being rewritten by CH. Slow / sustained
                non-zero is the early-warning shape for the
                operator: time to swap the mutation pattern. */}
            {data.mutations && data.mutations.length > 0 && (
              <Section title={`Pending mutations (${data.mutations.length})`}>
                <p style={{ fontSize: 11, color: 'var(--text2)', margin: '0 0 8px' }}>
                  In-flight ALTER … DELETE / UPDATE rewriting parts. Healthy queue is empty;
                  a sustained non-zero row count usually means the table needs a tombstone or
                  ReplacingMergeTree pattern instead of in-place mutation.
                </p>
                <div className="table-wrap">
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={mutationDt} />
                    <DataTableHead dt={mutationDt} />
                    <tbody>
                      {mutationDt.sortedRows.map((m, i) => (
                        <tr key={i}>
                          <td className="mono">{m.database}.{m.table}</td>
                          <td className="num mono">{fmtNum(m.parts)}</td>
                          <td className="num mono">{fmtAge(m.elapsedMs)}</td>
                          <td className="mono" style={{ fontSize: 11 }} title={m.command}>{m.command}</td>
                          <td className="mono" style={{
                            fontSize: 11, color: m.latestFail ? 'var(--err)' : 'var(--text3)',
                          }} title={m.latestFail}>{m.latestFail || '—'}</td>
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
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={repLagDt} />
                    <DataTableHead dt={repLagDt} />
                    <tbody>
                      {repLagDt.sortedRows.map((r, i) => (
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

            {/* v0.6.8 — AI query optimizer. Operator pastes raw CH
                SQL; Copilot rewrites it against Coremetry's MV
                catalogue + hard-constraint checklist. Returns a
                cleaned-up query the operator reviews + copies into
                their CH client. Not a query runner — we don't
                want a half-helpful UI that makes it tempting to
                hand un-reviewed AI output a CH session. */}
            <CHQueryOptimizer />
          </>
        )}
      </div>
    </>
  );
}

function CHQueryOptimizer() {
  const [query, setQuery] = useState('');
  const [result, setResult] = useState<{
    optimized: string; explanation: string;
    warning?: string; raw?: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    const q = query.trim();
    if (!q) return;
    setBusy(true); setErr(null); setResult(null);
    try {
      const r = await api.optimizeCHQuery(q);
      setResult(r);
    } catch (e: any) {
      setErr(e?.message || String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section style={{
      marginTop: 24, padding: 16, background: 'var(--bg1)',
      border: '1px solid var(--border)', borderRadius: 8,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <h3 style={{ margin: 0, fontSize: 14 }}>AI query optimizer</h3>
        <span className="badge b-info">v0.6.8</span>
        <span style={{ fontSize: 11, color: 'var(--text3)', marginLeft: 'auto' }}>
          MV bypass · LIMIT · max_execution_time · time-bounded WHERE
        </span>
      </div>
      <p style={{ fontSize: 12, color: 'var(--text2)', margin: '0 0 10px' }}>
        Paste a ClickHouse query. AI rewrites it against Coremetry's MV catalogue
        (<code>service_summary_5m</code>, <code>topology_edges_5m</code>, …) and the
        hard-constraint checklist. Review before running — output is a suggestion, not auto-applied.
      </p>
      <textarea
        value={query}
        onChange={e => setQuery(e.target.value)}
        placeholder="SELECT service_name, count() FROM spans WHERE time >= now() - INTERVAL 1 HOUR GROUP BY service_name"
        rows={6}
        spellCheck={false}
        style={{
          width: '100%', boxSizing: 'border-box',
          fontFamily: 'ui-monospace, monospace', fontSize: 12,
          padding: 10, background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 6,
        }}
      />
      <div style={{ display: 'flex', gap: 8, marginTop: 8, alignItems: 'center' }}>
        <button onClick={run} disabled={busy || !query.trim()}>
          {busy ? 'Optimizing…' : 'Optimize'}
        </button>
        {err && <span style={{ color: 'var(--err)', fontSize: 12 }}>{err}</span>}
      </div>
      {result && (
        <div style={{ marginTop: 14 }}>
          {result.warning && (
            <div className="b-warn" style={{
              fontSize: 12, marginBottom: 8,
              padding: '6px 10px', borderRadius: 4,
            }}>{result.warning}</div>
          )}
          {result.optimized && (
            <>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>
                Optimized SQL
              </div>
              <pre style={{
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                padding: 10, background: 'var(--bg)', color: 'var(--text)',
                border: '1px solid var(--border)', borderRadius: 6,
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              }}>{result.optimized}</pre>
            </>
          )}
          {result.explanation && (
            <>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 10, marginBottom: 4 }}>
                Explanation
              </div>
              <p style={{ fontSize: 12, color: 'var(--text)', margin: 0, lineHeight: 1.5 }}>
                {result.explanation}
              </p>
            </>
          )}
          {!result.optimized && result.raw && (
            <>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 10, marginBottom: 4 }}>
                Raw model output
              </div>
              <pre style={{
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                padding: 10, background: 'var(--bg)', color: 'var(--text3)',
                border: '1px solid var(--border)', borderRadius: 6,
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              }}>{result.raw}</pre>
            </>
          )}
        </div>
      )}
    </section>
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
  // v0.5.439 — if the probe failed BUT a cached snapshot filled
  // `nodes` in (`clusterNodesStale`), drop the warn banner
  // entirely; the cluster is still healthy, we just couldn't
  // refresh the topology this tick. A small inline "stale"
  // pill carries the freshness signal at a softer volume.
  const probeFailed = !!t.clusterProbeError;
  const cacheStale = !!t.clusterNodesStale;
  const misconfigured = !!t.configuredCluster && (!t.nodes || t.nodes.length === 0) && !probeFailed && !cacheStale;

  // Shared sortable + resizable tables for the cluster-nodes list and
  // the resolved shard policy. Both hooks are unconditional; the tables
  // themselves render conditionally below.
  const nodesDt = useDataTable<ClusterNode>({
    storageKey: 'ch-clusternodes', columns: NODE_COLS,
    rows: t.nodes ?? [], initialSort: { id: 'shard', dir: 'asc' },
  });
  const shardRows: ShardPolicyRow[] = Object.entries(t.shardPolicy ?? {})
    .map(([table, expr]) => ({ table, expr }));
  const shardDt = useDataTable<ShardPolicyRow>({
    storageKey: 'ch-shardpolicy', columns: SHARD_POLICY_COLS,
    rows: shardRows, initialSort: { id: 'table', dir: 'asc' },
  });
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
          {!misconfigured && !probeFailed && t.mode === 'cluster' && (
            <>
              <strong style={{ color: 'var(--ok)' }}>● Cluster mode</strong> —
              connected to cluster <code className="mono">{t.configuredCluster}</code>
              {' '}with <strong>{t.nodes?.length ?? 0}</strong> registered node{(t.nodes?.length ?? 0) === 1 ? '' : 's'}.
              {cacheStale && (
                <span
                  title="Live system.clusters probe failed this tick — showing the last successful snapshot."
                  style={{
                    marginLeft: 8, padding: '1px 6px', borderRadius: 3,
                    background: 'rgba(250, 204, 21, 0.18)',
                    color: 'var(--warn)', fontSize: 11, fontWeight: 600,
                  }}>
                  stale: {fmtAge(t.clusterNodesAgeMs ?? 0)}
                </span>
              )}
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
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={nodesDt} />
            <DataTableHead dt={nodesDt} />
            <tbody>
              {nodesDt.sortedRows.map((n, i) => (
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
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={shardDt} />
              <DataTableHead dt={shardDt} />
              <tbody>
                {shardDt.sortedRows.map(({ table, expr }) => (
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

// v0.5.439 — short age string for the stale-cache pill. Drops
// sub-second precision; rounds to the unit that reads cleanest
// in a chip.
function fmtAge(ms: number): string {
  if (ms < 60_000) return `${Math.round(ms / 1000)}s ago`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m ago`;
  return `${Math.round(ms / 3_600_000)}h ago`;
}
