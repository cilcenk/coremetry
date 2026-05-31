import { Fragment, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Empty, Spinner } from './Spinner';
import { MultiLineChart } from './MultiLineChart';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from './DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, DBDetail, MessagingDetail, OracleMetrics, PostgresMetrics, MySQLMetrics, RedisMetrics, SpanMetricSeries } from '@/lib/types';

// OracleDrill — what the user clicked on. Carries enough state
// to build a metricQuery against /api/metrics/query and label
// the drill-down modal.
type OracleDrill = {
  metric: string;                    // e.g. 'oracledb.sessions.usage'
  label: string;                     // human-readable for the modal title
  unit?: string;                     // ms / % / bytes — feeds the chart's fmtSmart
  filters?: { key: string; op: '='; value: string }[]; // tablespace_name=SYSTEM etc.
};

// Row is the shape both /databases and /messaging hand to this
// component. We type-erase the row-specific labelling (Instance
// vs Destination, System vs System) via the props above; the
// table logic is otherwise identical.
export interface DepRow {
  system: string;
  // Messaging-only — physical cluster identifier. Empty / undefined
  // for DB rows. Surfaced as a Cluster column when any row in
  // the set has it populated so a Kafka deployment with one
  // cluster doesn't gain an empty column.
  cluster?: string;
  // EITHER instance (DB) OR destination (messaging). Both are
  // optional on the type so the caller can wire whichever it
  // has — the table renders whichever is non-empty.
  instance?: string;
  destination?: string;
  // v0.5.315 — per-database split. One DB host can serve many
  // databases (Oracle SIDs, PostgreSQL / MongoDB / MSSQL DBs).
  // When present, surface as a chip next to the instance so
  // operator sees (host, database) as a single addressable
  // unit instead of collapsing every DB on a host into one row.
  dbName?: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  callers: string[];
  // source: where the row came from. 'receiver' rows are
  // surfaced with a badge and zero RED stats — the drill-down
  // panel (e.g. OracleDB receiver) is the actionable surface.
  source?: 'spans' | 'receiver';
}

type SortKey = 'system' | 'cluster' | 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99';
const NATURAL: Record<SortKey, 'asc' | 'desc'> = {
  system: 'asc', cluster: 'asc', name: 'asc', spanCount: 'desc',
  errorRate: 'desc', avg: 'desc', p99: 'desc',
};

// DependenciesTable renders the system+instance+RED+callers grid
// shared by /databases and /messaging. Kind controls the column
// header label and the click-through DSL pre-filter so a row
// click lands on /explore scoped to that system+instance.
export function DependenciesTable({
  rows, kind, range,
}: {
  rows: DepRow[];
  // 'db' → uses instance + filters by db.system; 'queue' → uses
  // destination + filters by messaging.system.
  kind: 'db' | 'queue';
  // Time range — drives the detail drawer's per-(service, pod)
  // breakdown query. Same window the parent /databases or
  // /messaging page uses for the overview.
  range: TimeRange;
}) {
  const [systemFilter, setSystemFilter] = useState<string>('');
  const [search, setSearch] = useState('');
  // Which row's drawer is open. Stores `system|name` so the
  // drawer survives sort + filter changes (system+name are
  // stable identifiers).
  const [openKey, setOpenKey] = useState<string | null>(null);

  const systems = useMemo(() => {
    const s = new Set<string>();
    for (const r of rows) s.add(r.system);
    return Array.from(s).sort();
  }, [rows]);
  // Show the Cluster column only when at least one row carries
  // a non-default cluster. Single-Kafka-cluster deployments
  // skip the column entirely.
  const hasClusterCol = useMemo(
    () => rows.some(r => r.cluster && r.cluster !== '(default)'),
    [rows]);

  // v0.5.349 — when instance is the legacy 'unknown' sentinel
  // and a real db.name is available, surface the db.name as
  // the primary label. The MV migration in store.go rewrites
  // 'unknown' → server.address / net.peer.name / etc. for
  // fresh data, but past 5-min buckets keep the literal
  // 'unknown' until they roll out of the retention window.
  // This client-side fallback gives operators readable labels
  // on existing data immediately.
  const nameOf = (r: DepRow) => {
    const inst = r.instance ?? r.destination ?? '';
    if (inst === 'unknown' && r.dbName && r.dbName !== 'default') {
      return r.dbName;
    }
    return inst;
  };

  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    return rows.filter(r => {
      if (systemFilter && r.system !== systemFilter) return false;
      if (term) {
        return r.system.toLowerCase().includes(term)
            || (r.cluster ?? '').toLowerCase().includes(term)
            || nameOf(r).toLowerCase().includes(term)
            || r.callers.some(c => c.toLowerCase().includes(term));
      }
      return true;
    });
  }, [rows, systemFilter, search]);

  // Shared sortable + resizable table. Columns built per-render so the
  // name label (Instance vs Destination) + the optional Cluster column
  // track kind/hasClusterCol. Accessors mirror the prior sort keys.
  const depCols = useMemo<DataTableColumn<DepRow>[]>(() => [
    { id: 'system', label: 'System', sortValue: r => r.system, naturalDir: NATURAL.system, width: 150 },
    ...(hasClusterCol
      ? [{ id: 'cluster', label: 'Cluster', sortValue: (r: DepRow) => r.cluster ?? '', naturalDir: NATURAL.cluster, width: 120 } as DataTableColumn<DepRow>]
      : []),
    { id: 'name', label: kind === 'db' ? 'Instance' : 'Destination', sortValue: r => nameOf(r), naturalDir: NATURAL.name, width: 210 },
    { id: 'spanCount', label: 'Calls', sortValue: r => r.spanCount, numeric: true, naturalDir: NATURAL.spanCount, width: 96 },
    { id: 'errorRate', label: 'Err %', sortValue: r => r.errorRate, numeric: true, naturalDir: NATURAL.errorRate, width: 96 },
    { id: 'avg', label: 'Avg', sortValue: r => r.avgDurationMs, numeric: true, naturalDir: NATURAL.avg, width: 90 },
    { id: 'p99', label: 'P99', sortValue: r => r.p99DurationMs, numeric: true, naturalDir: NATURAL.p99, width: 90 },
    { id: 'callers', label: 'Top callers', width: 240 },
    // eslint-disable-next-line react-hooks/exhaustive-deps
  ], [hasClusterCol, kind]);

  const dt = useDataTable<DepRow>({
    storageKey: `deps-${kind}`,
    columns: depCols,
    rows: filtered,
    initialSort: { id: 'spanCount', dir: 'desc' },
  });

  // Click-through DSL — pre-filters /explore by the chosen
  // system + instance. For DBs the key is db.system; for
  // messaging it's messaging.system + messaging.destination.name.
  const exploreHref = (r: DepRow) => {
    if (kind === 'db') {
      const dsl =
        `db.system = "${r.system}"\n` +
        (r.instance && r.instance !== 'unknown'
          ? `peer.service = "${r.instance}"` : '');
      return `/explore?dsl=${encodeURIComponent(dsl)}&mode=advanced&result=traces`;
    }
    const dsl =
      `messaging.system = "${r.system}"\n` +
      (r.destination && r.destination !== 'unknown'
        ? `messaging.destination.name = "${r.destination}"` : '');
    return `/explore?dsl=${encodeURIComponent(dsl)}&mode=advanced&result=traces`;
  };

  if (rows.length === 0) {
    return (
      <Empty icon="◯" title={kind === 'db'
        ? 'No database calls in this window'
        : 'No messaging activity in this window'}>
        {kind === 'db'
          ? 'Coremetry derives this view from spans with a populated db.system attribute.'
          : 'Derived from spans with a populated messaging.system attribute.'}
      </Empty>
    );
  }

  return (
    <>
      <div className="controls" style={{ marginBottom: 12, flexWrap: 'wrap' }}>
        <span style={{ color: 'var(--text2)', fontSize: 12 }}>System:</span>
        <select value={systemFilter} onChange={e => setSystemFilter(e.target.value)}
                style={{ fontSize: 12 }}>
          <option value="">All ({rows.length})</option>
          {systems.map(s => <option key={s} value={s}>{s}</option>)}
        </select>
        <input value={search} onChange={e => setSearch(e.target.value)}
               placeholder={kind === 'db'
                 ? 'Search system / instance / caller…'
                 : 'Search system / destination / caller…'}
               style={{ width: 280 }} />
        <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 'auto' }}>
          {dt.sortedRows.length} of {rows.length}
        </span>
      </div>

      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} leading={[24]} />
          <DataTableHead dt={dt} leading={<th style={{ width: 24 }} aria-label="Expand"></th>} />
          <tbody>
            {dt.sortedRows.map((r, i) => {
              const errCls = r.errorRate > 5 ? 'err' : r.errorRate > 0 ? 'warn' : 'ok';
              // Key includes cluster so two rows with the same
              // (system, destination) but different physical
              // Kafka clusters don't collide in expansion state.
              const rowKey = `${r.system}|${r.cluster ?? ''}|${nameOf(r)}`;
              const isOpen = openKey === rowKey;
              return (
                <Fragment key={`${rowKey}|${i}`}>
                  <tr onClick={() => setOpenKey(isOpen ? null : rowKey)}
                      style={{ cursor: 'pointer',
                               background: isOpen ? 'var(--bg2)' : undefined }}>
                    <td style={{ color: 'var(--text3)', width: 24, textAlign: 'center' }}>
                      {isOpen ? '▾' : '▸'}
                    </td>
                    <td>
                      <SystemBadge system={r.system} kind={kind} />
                      {r.source === 'receiver' && (
                        <span title="Discovered via OpenTelemetry database receiver (oracledb / postgres / mysql / …). No application spans yet — drill down to see receiver metrics directly."
                              style={{
                                marginLeft: 6, fontSize: 9, padding: '1px 6px',
                                borderRadius: 3, fontWeight: 600,
                                background: 'rgba(56,139,253,0.15)',
                                color: 'var(--accent2)',
                                fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                                textTransform: 'uppercase', letterSpacing: '.5px',
                                verticalAlign: 'middle',
                              }}>via receiver</span>
                      )}
                    </td>
                    {hasClusterCol && (
                      <td style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text2)' }}>
                        {r.cluster === '(default)' ? (
                          <span style={{ color: 'var(--text3)' }}>—</span>
                        ) : (
                          r.cluster
                        )}
                      </td>
                    )}
                    <td onClick={e => e.stopPropagation()}>
                      <Link to={exploreHref(r)}
                            style={{ fontFamily: 'monospace', fontSize: 12, fontWeight: 500 }}
                            title={r.instance === 'unknown'
                              ? `peer.service was empty on these spans — label sourced from ${r.dbName && r.dbName !== 'default' ? 'db.name' : 'fallback'}`
                              : 'Open in Explore (spans pre-filtered)'}>
                        {nameOf(r) || <span style={{ color: 'var(--text3)' }}>(anonymous)</span>}
                      </Link>
                      {/* v0.5.349 — surface a small chip when the
                          label is a fallback so the operator
                          knows the peer.service attribute is
                          missing on the source spans (actionable:
                          tell the SDK team to set it). Hidden
                          for fresh rows that carry a real
                          instance identifier. */}
                      {r.instance === 'unknown' && (
                        <span title="peer.service missing on these spans — backend fell back to db.name / host / service_name"
                          style={{
                            marginLeft: 6, fontSize: 9,
                            padding: '1px 5px', borderRadius: 3,
                            background: 'var(--bg3)',
                            border: '1px solid var(--border)',
                            color: 'var(--text3)',
                            verticalAlign: 'middle',
                            fontWeight: 700,
                          }}>
                          fallback
                        </span>
                      )}
                      {/* v0.5.315 — db.name chip when present.
                          One host serving N databases (Oracle
                          SID/service, PostgreSQL / MongoDB /
                          MSSQL DB) → row is keyed on (host,
                          dbName). Chip surfaces the dbName so
                          the operator sees the addressable
                          unit at a glance. Hidden for the
                          'default' fallback (means the OTel
                          instrumentation didn't emit db.name). */}
                      {r.dbName && r.dbName !== 'default' && (
                        <span title={`db.name = ${r.dbName}`}
                          style={{
                            marginLeft: 8, fontSize: 10,
                            padding: '1px 6px', borderRadius: 3,
                            background: 'var(--bg3)',
                            border: '1px solid var(--border)',
                            color: 'var(--text2)',
                            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                            verticalAlign: 'middle',
                          }}>
                          ⛁ {r.dbName}
                        </span>
                      )}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(r.spanCount)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      <span className={`badge b-${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.avgDurationMs.toFixed(1)}ms</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.p99DurationMs.toFixed(1)}ms</td>
                    <td style={{ fontSize: 11 }} onClick={e => e.stopPropagation()}>
                      {r.callers.length === 0
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : r.callers.slice(0, 3).map((c, idx) => (
                            <span key={c}>
                              <Link to={`/service?name=${encodeURIComponent(c)}`}
                                    style={{ fontFamily: 'monospace' }}>{c}</Link>
                              {idx < Math.min(2, r.callers.length - 1) && <span style={{ color: 'var(--text3)' }}>, </span>}
                            </span>
                          ))}
                      {r.callers.length > 3 && (
                        <span style={{ color: 'var(--text3)' }}> +{r.callers.length - 3}</span>
                      )}
                    </td>
                  </tr>
                  {isOpen && (
                    <tr>
                      <td colSpan={hasClusterCol ? 9 : 8} style={{
                        background: 'var(--bg1)', padding: '12px 16px',
                        borderTop: '1px solid var(--border)',
                      }}>
                        <DetailDrawer
                          system={r.system}
                          cluster={r.cluster ?? '(default)'}
                          name={nameOf(r)}
                          kind={kind}
                          source={r.source ?? 'spans'}
                          range={range} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

// DetailDrawer fetches and renders the per-(service, pod) caller
// breakdown + top operations for one (system, instance) tuple.
// Lazy — only fires when the row is expanded; bounded server-
// side at LIMIT 100 callers / LIMIT 20 ops so the response stays
// cheap even for a 50-pod fleet.
function DetailDrawer({ system, cluster, name, kind, source, range }: {
  system: string;
  // Cluster identifier — only meaningful for queue/messaging
  // rows. DB callers pass "(default)" and the backend ignores
  // it (DB queries don't have a cluster dimension).
  cluster: string;
  name: string;
  kind: 'db' | 'queue';
  // 'spans' = row came from app-emitted traces; 'receiver' = row
  // came from an OTel DB receiver. We only render the receiver-
  // specific metric panel (oracle / postgres / mysql / redis)
  // for receiver rows so the upper "Called from services" panel
  // doesn't bleed receiver-side metrics into a span-derived row.
  source: 'spans' | 'receiver';
  range: TimeRange;
}) {
  type D = DBDetail | MessagingDetail;
  const [data, setData] = useState<D | null | undefined>(undefined);

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    const p = kind === 'db'
      ? api.databaseDetail(system, name, from, to)
      : api.messagingDetail(system, cluster, name, from, to);
    p.then(r => setData(r ?? null))
     .catch(() => setData(null));
  }, [system, cluster, name, kind, range]);

  if (data === undefined) return <Spinner />;
  if (data === null) return (
    <div style={{ fontSize: 12, color: 'var(--err)' }}>
      Detail query failed.
    </div>
  );

  // Defensive null-coalesce — pre-v0.4.87 the backend returned
  // null for empty slices (Go nil → JSON null), which crashed
  // [...data.callers]. The store now emits [] but we keep the
  // guard in case the cache returns a stale payload across an
  // upgrade.
  const allCallers = data.callers ?? [];
  const allTopOps  = data.topOps ?? [];

  // Worst-impact callers first — operator's first triage
  // question is "which client is hitting this DB hardest?".
  // We sort by spanCount × avgMs (impact, Elastic-APM style)
  // since a 200ms call made 10k times beats a 5s call made
  // twice for cumulative load on the backend.
  const callers = [...allCallers].sort((a, b) =>
    (b.spanCount * b.avgDurationMs) - (a.spanCount * a.avgDurationMs));

  // For messaging detail we split Producers / Consumers visually
  // — the SRE's "who's publishing" and "who's consuming"
  // questions are different (publisher is usually the load
  // generator, consumer is where back-pressure shows up).
  const producers = kind === 'queue'
    ? callers.filter(c => c.role === 'producer')
    : [];
  const consumers = kind === 'queue'
    ? callers.filter(c => c.role === 'consumer')
    : [];
  const otherClients = kind === 'queue'
    ? callers.filter(c => c.role && c.role !== 'producer' && c.role !== 'consumer')
    : callers;

  return (
    <div>
      {/* Aggregate strip on top — same numbers as the row but
          repeated here so the drawer reads on its own when
          screenshotted into a postmortem. */}
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))',
        gap: 10, marginBottom: 14,
      }}>
        <Stat label="Calls"     value={fmtNum(data.spanCount)} />
        <Stat label="Errors"    value={fmtNum(data.errorCount)} />
        <Stat label="Err rate"  value={`${data.errorRate.toFixed(2)}%`}
              tone={data.errorRate > 5 ? 'err' : data.errorRate > 0 ? 'warn' : 'ok'} />
        <Stat label="Avg"       value={`${data.avgDurationMs.toFixed(1)} ms`} />
        <Stat label="P99"       value={`${data.p99DurationMs.toFixed(1)} ms`} />
      </div>

      {/* Oracle-specific drill-down — only renders when the system
          is oracle. Reads the oracledb-receiver-flavoured metrics
          panel (sessions / processes / counters / tablespaces).
          The backend serves synthetic numbers with synthetic=true
          when no receiver data exists in the window so the panel
          still renders during integration setup. */}
      {/* Receiver-specific panels gated on source — app-derived
          rows (the "Called from services" panel) intentionally
          hide these so receiver-side metrics don't bleed in. */}
      {source === 'receiver' && kind === 'db' && system.toLowerCase() === 'oracle' && (
        <OraclePanel instance={name} range={range} />
      )}
      {source === 'receiver' && kind === 'db' && (system.toLowerCase() === 'postgresql' || system.toLowerCase() === 'postgres') && (
        <PostgresPanel instance={name} range={range} />
      )}
      {source === 'receiver' && kind === 'db' && (system.toLowerCase() === 'mysql' || system.toLowerCase() === 'mariadb') && (
        <MySQLPanel instance={name} range={range} />
      )}
      {source === 'receiver' && kind === 'db' && system.toLowerCase() === 'redis' && (
        <RedisPanel instance={name} range={range} />
      )}

      {/* Per-(service, pod) breakdown — the SRE's "which client
          is shouting at this DB / queue" answer. Sorted by impact
          (spanCount × avgMs) so the heaviest cumulative consumer
          surfaces first. For messaging we split Producers /
          Consumers since they answer different questions. */}
      {kind === 'queue' ? (
        <>
          <CallerSection
            title={`Publishers · ${producers.length} ${producers.length === 1 ? 'row' : 'rows'}`}
            rows={producers}
            emptyMessage="No producer spans for this destination in the window."
            tone="producer" />
          <CallerSection
            title={`Consumers · ${consumers.length} ${consumers.length === 1 ? 'row' : 'rows'}`}
            rows={consumers}
            emptyMessage="No consumer spans for this destination in the window."
            tone="consumer" />
          {otherClients.length > 0 && (
            <CallerSection
              title={`Other clients · ${otherClients.length}`}
              rows={otherClients}
              emptyMessage=""
              tone="other" />
          )}
        </>
      ) : (
        <CallerSection
          title={`By client (service + pod) · ${callers.length} ${callers.length === 1 ? 'row' : 'rows'}`}
          rows={callers}
          emptyMessage="No callers in this window."
          tone="db" />
      )}

      {/* Top operations — for DBs the first 80 chars of
          db_statement (collapses unparameterised SQL); for
          messaging the span name (publish / consume / process). */}
      {allTopOps.length > 0 && (
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, marginBottom: 6,
                         color: 'var(--text2)' }}>
            {kind === 'db'
              ? `Top ${allTopOps.length} statements (first 80 chars)`
              : `Top ${allTopOps.length} operations`}
          </div>
          <div className="table-wrap" style={{ maxHeight: 240, overflowY: 'auto' }}>
            <table>
              <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
                <tr>
                  <th>{kind === 'db' ? 'Statement' : 'Operation'}</th>
                  <th className="num">Count</th>
                  <th className="num">Avg</th>
                </tr>
              </thead>
              <tbody>
                {allTopOps.map((o, i) => (
                  <tr key={i}>
                    <td style={{
                      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      fontSize: 11, wordBreak: 'break-word', maxWidth: 600,
                    }}>{o.statement || <span style={{ color: 'var(--text3)' }}>(empty)</span>}</td>
                    <td className="num mono">{fmtNum(o.count)}</td>
                    <td className="num mono">{o.avgDurationMs.toFixed(1)}ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

// CallerSection renders one labelled table of (service, pod) rows
// with their RED metrics. Tone colours the header so producers /
// consumers / DB clients read at a glance. Empty sections render
// a one-line placeholder so the operator sees that we did look
// for the data and there just isn't any in this window.
//
// Sort headers added in v0.4.92 — sorting is client-side because
// the backend caps the result at LIMIT 100, so a 100-row sort
// is O(n log n) ≈ 700 comparisons on the operator's machine.
// At enterprise scale (5k+ pods) the same cap protects: the
// drawer always shows the top-100-by-calls cohort and the
// operator sorts within that page, no full-fleet re-fetch
// trip. Default sort is Calls desc so the heaviest caller
// keeps surfacing on first paint.
type CallerSortKey = 'service' | 'pod' | 'role' | 'calls' | 'errRate' | 'avg' | 'p99';
const CALLER_NATURAL: Record<CallerSortKey, 'asc' | 'desc'> = {
  service: 'asc', pod: 'asc', role: 'asc',
  calls: 'desc', errRate: 'desc', avg: 'desc', p99: 'desc',
};

function CallerSection({ title, rows, emptyMessage, tone }: {
  title: string;
  rows: import('@/lib/types').DBCallerBreakdown[];
  emptyMessage: string;
  tone: 'producer' | 'consumer' | 'other' | 'db';
}) {
  const dotColor =
    tone === 'producer' ? 'var(--accent2)' :
    tone === 'consumer' ? 'var(--ok)' :
    tone === 'other'    ? 'var(--text3)' :
                          'var(--accent2)';
  const hasRole = rows.some(r => r.role);
  const [sortBy, setSortBy] = useState<CallerSortKey>('calls');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');
  // Client-side search across service / pod / role so an
  // operator with hundreds of pods hitting one DB can pinpoint
  // a specific instance fast. Backend caps the result set at
  // LIMIT 500 (v0.5.12) so filtering 500 rows stays
  // sub-millisecond.
  const [search, setSearch] = useState('');
  const toggleSort = (col: CallerSortKey) => {
    if (sortBy === col) setSortDir(d => d === 'desc' ? 'asc' : 'desc');
    else { setSortBy(col); setSortDir(CALLER_NATURAL[col]); }
  };
  const filtered = useMemo(() => {
    const t = search.trim().toLowerCase();
    if (!t) return rows;
    return rows.filter(r =>
      r.service.toLowerCase().includes(t) ||
      r.pod.toLowerCase().includes(t) ||
      (r.role ?? '').toLowerCase().includes(t));
  }, [rows, search]);
  const sorted = useMemo(() => {
    const arr = [...filtered];
    arr.sort((a, b) => {
      switch (sortBy) {
        case 'service': return a.service.localeCompare(b.service);
        case 'pod':     return a.pod.localeCompare(b.pod);
        case 'role':    return (a.role ?? '').localeCompare(b.role ?? '');
        case 'calls':   return a.spanCount - b.spanCount;
        case 'errRate': return a.errorRate - b.errorRate;
        case 'avg':     return a.avgDurationMs - b.avgDurationMs;
        case 'p99':     return a.p99DurationMs - b.p99DurationMs;
      }
    });
    return sortDir === 'desc' ? arr.reverse() : arr;
  }, [filtered, sortBy, sortDir]);
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8,
        fontSize: 12, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
      }}>
        <span aria-hidden style={{
          width: 8, height: 8, borderRadius: 2, background: dotColor,
        }} />
        {title}
        {rows.length > 10 && (
          <input value={search} onChange={e => setSearch(e.target.value)}
            placeholder="Search service / pod / role…"
            style={{
              marginLeft: 'auto', fontSize: 11, padding: '3px 8px',
              width: 200, fontWeight: 400,
            }} />
        )}
        {search && (
          <span style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 400 }}>
            {sorted.length} of {rows.length}
          </span>
        )}
      </div>
      {rows.length === 0 ? (
        emptyMessage && <div style={{ fontSize: 12, color: 'var(--text3)' }}>{emptyMessage}</div>
      ) : (
        <div className="table-wrap" style={{ maxHeight: 320, overflowY: 'auto' }}>
          <table>
            <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
              <tr>
                <CallerSortTh col="service" label="Service"    sort={sortBy} dir={sortDir} onSort={toggleSort} />
                <CallerSortTh col="pod"     label="Pod / host" sort={sortBy} dir={sortDir} onSort={toggleSort} />
                {hasRole && (
                  <CallerSortTh col="role" label="Role" sort={sortBy} dir={sortDir} onSort={toggleSort} />
                )}
                <CallerSortTh col="calls"   label="Calls"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                <CallerSortTh col="errRate" label="Err %"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                <CallerSortTh col="avg"     label="Avg"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                <CallerSortTh col="p99"     label="P99"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
              </tr>
            </thead>
            <tbody>
              {sorted.map((c, i) => {
                const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
                return (
                  <tr key={`${c.service}|${c.pod}|${c.role ?? ''}|${i}`}>
                    <td>
                      <Link to={`/service?name=${encodeURIComponent(c.service)}`}
                            style={{ fontFamily: 'monospace', fontSize: 12 }}>
                        {c.service}
                      </Link>
                    </td>
                    <td style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text2)' }}>
                      {c.pod}
                    </td>
                    {hasRole && (
                      <td>
                        {c.role && <RoleBadge role={c.role} />}
                      </td>
                    )}
                    <td className="num mono">{fmtNum(c.spanCount)}</td>
                    <td className="num mono">
                      <span className={`badge b-${errCls}`} style={{ fontSize: 9 }}>
                        {c.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                    <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// CallerSortTh is the sortable header used inside the drawer's
// per-(service, pod) tables. Same accessibility shape as the
// outer-table SortHeader (aria-sort + button + Enter/Space) so
// keyboard users can sort the breakdown.
function CallerSortTh({ col, label, sort, dir, onSort, align }: {
  col: CallerSortKey; label: string;
  sort: CallerSortKey; dir: 'asc' | 'desc';
  onSort: (c: CallerSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        style={{ textAlign: align ?? 'left' }}
        aria-sort={active ? (dir === 'desc' ? 'descending' : 'ascending') : 'none'}>
      <button type="button" onClick={() => onSort(col)}
        style={{
          all: 'unset', display: 'inline-flex', alignItems: 'baseline',
          gap: 4, width: '100%', cursor: 'pointer',
          justifyContent: align === 'right' ? 'flex-end' : 'flex-start',
        }}>
        {label}
        <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
      </button>
    </th>
  );
}

function RoleBadge({ role }: { role: string }) {
  const r = role.toLowerCase();
  const tone =
    r === 'producer' ? { bg: 'rgba(56,139,253,0.15)', fg: 'var(--accent2)' } :
    r === 'consumer' ? { bg: 'rgba(63,185,80,0.15)',  fg: 'var(--ok)' } :
    r === 'client'   ? { bg: 'var(--bg3)',            fg: 'var(--text2)' } :
                       { bg: 'var(--bg3)',            fg: 'var(--text3)' };
  return (
    <span style={{
      fontSize: 10, padding: '1px 6px', borderRadius: 3, fontWeight: 600,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      background: tone.bg, color: tone.fg,
      textTransform: 'uppercase', letterSpacing: '.5px',
    }}>{role}</span>
  );
}

function Stat({ label, value, tone, onClick, sub }: {
  label: string; value: string; tone?: 'ok' | 'warn' | 'err';
  onClick?: () => void;
  sub?: string;
}) {
  const color = tone === 'err' ? 'var(--err)'
              : tone === 'warn' ? 'var(--warn)'
              : tone === 'ok'  ? 'var(--ok)'
              : 'var(--text)';
  // When clickable we render the tile as a button so the
  // operator gets keyboard + screen-reader treatment for free,
  // a subtle hover state, and an arrow affordance in the
  // corner to telegraph the drill-down.
  const inner = (
    <>
      <div style={{
        fontSize: 9, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
        display: 'flex', alignItems: 'center', gap: 4,
      }}>
        {label}
        {onClick && (
          <span aria-hidden style={{ marginLeft: 'auto', opacity: 0.5 }}>↗</span>
        )}
      </div>
      <div style={{ fontSize: 16, fontWeight: 700, color,
                     fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
        {value}
      </div>
      {sub && (
        <div style={{
          fontSize: 10, color: 'var(--text3)', marginTop: 2,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>{sub}</div>
      )}
    </>
  );
  if (onClick) {
    return (
      <button type="button" onClick={onClick}
        title="Open metric chart"
        style={{
          all: 'unset', display: 'block', cursor: 'pointer',
          padding: '8px 10px', borderRadius: 4,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          transition: 'border-color 0.12s, background 0.12s',
        }}
        onMouseEnter={e => {
          e.currentTarget.style.borderColor = 'var(--accent2)';
          e.currentTarget.style.background = 'var(--bg3)';
        }}
        onMouseLeave={e => {
          e.currentTarget.style.borderColor = 'var(--border)';
          e.currentTarget.style.background = 'var(--bg2)';
        }}>
        {inner}
      </button>
    );
  }
  return (
    <div style={{
      padding: '8px 10px', borderRadius: 4,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      {inner}
    </div>
  );
}

// SystemBadge renders the system name in its conventional colour
// — Postgres blue, Redis red, Kafka dark, etc. — so an operator
// scanning the list recognises the technology at a glance.
function SystemBadge({ system, kind }: { system: string; kind: 'db' | 'queue' }) {
  const s = system.toLowerCase();
  const tone: Record<string, { bg: string; fg: string }> = {
    postgresql: { bg: 'rgba(51,103,145,0.18)', fg: '#5b8fb9' },
    postgres:   { bg: 'rgba(51,103,145,0.18)', fg: '#5b8fb9' },
    mysql:      { bg: 'rgba(0,117,143,0.18)',  fg: '#21a0a0' },
    mariadb:    { bg: 'rgba(0,117,143,0.18)',  fg: '#21a0a0' },
    oracle:     { bg: 'rgba(216,72,57,0.18)',  fg: '#d84839' },
    redis:      { bg: 'rgba(220,38,38,0.18)',  fg: '#dc2626' },
    mongodb:    { bg: 'rgba(76,175,80,0.18)',  fg: '#5cb85c' },
    mongo:      { bg: 'rgba(76,175,80,0.18)',  fg: '#5cb85c' },
    cassandra:  { bg: 'rgba(34,87,180,0.18)',  fg: '#5b8fff' },
    elasticsearch: { bg: 'rgba(0,127,127,0.18)', fg: '#1a8c8c' },
    clickhouse: { bg: 'rgba(252,212,52,0.18)', fg: '#e0b400' },
    kafka:      { bg: 'rgba(30,30,30,0.25)',   fg: 'var(--text)' },
    rabbitmq:   { bg: 'rgba(255,102,0,0.18)',  fg: '#ff6600' },
    ibmmq:      { bg: 'rgba(15,98,254,0.18)',  fg: '#0f62fe' },
    nats:       { bg: 'rgba(39,174,96,0.18)',  fg: '#27ae60' },
    sqs:        { bg: 'rgba(255,153,0,0.18)',  fg: '#ff9900' },
    kinesis:    { bg: 'rgba(255,153,0,0.18)',  fg: '#ff9900' },
  };
  const t = tone[s] ?? { bg: 'var(--bg3)', fg: 'var(--text2)' };
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 6,
      padding: '2px 8px', borderRadius: 4, fontSize: 11, fontWeight: 600,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      background: t.bg, color: t.fg,
      border: `1px solid ${t.fg}33`,
    }}>
      <span aria-hidden style={{ fontSize: 10 }}>{kind === 'db' ? '⛁' : '⌬'}</span>
      {system}
    </span>
  );
}

// OraclePanel renders the OracleDB-receiver drill-down. Fetches
// `/api/databases/oracle?instance=…` and shows a KPI grid +
// tablespace usage bars. When the backend has no real
// oracledb.* points it returns synthetic=true and a "demo data"
// chip is rendered so the operator knows the integration
// isn't actually online yet.
function OraclePanel({ instance, range }: { instance: string; range: TimeRange }) {
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
            <TopSQLTable rows={data.topSQL} />
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

// OracleMetricDrillModal renders a time-series chart for one
// metric over the panel's current window. Same MultiLineChart
// the services / dashboards use, so an operator who already
// reads our other charts gets identical mechanics (hover
// crosshair, axis formatting, legend). Filters ride through
// to /api/metrics/query so a tablespace row chart only shows
// that tablespace's usage, not every tablespace's blended.
function OracleMetricDrillModal({ drill, range, onClose }: {
  drill: OracleDrill;
  range: TimeRange;
  onClose: () => void;
}) {
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  useEffect(() => {
    setSeries(undefined);
    const { from, to } = timeRangeToNs(range);
    const filterArg = drill.filters && drill.filters.length > 0
      ? JSON.stringify(drill.filters)
      : undefined;
    api.metricQuery({
      name: drill.metric,
      filters: filterArg,
      agg: 'avg',
      from, to,
    })
      .then(r => setSeries(r ?? []))
      .catch(() => setSeries(null));
  }, [drill, range]);

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
      display: 'grid', placeItems: 'center', zIndex: 200,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 880, maxWidth: '94vw', maxHeight: '88vh', overflow: 'auto',
        padding: 20, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{
          display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 14,
        }}>
          <div style={{ fontSize: 14, fontWeight: 700 }}>{drill.label}</div>
          <code style={{
            fontSize: 11, color: 'var(--text3)',
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          }}>{drill.metric}</code>
          {drill.filters && drill.filters.length > 0 && (
            <span style={{ fontSize: 10, color: 'var(--text3)' }}>
              {drill.filters.map(f => `${f.key} ${f.op} "${f.value}"`).join(' · ')}
            </span>
          )}
          <span style={{ marginLeft: 'auto' }}>
            <Link to={`/explore?metric=${encodeURIComponent(drill.metric)}&result=metric`}
                  style={{ fontSize: 11, marginRight: 12 }}>
              Open in Explore →
            </Link>
            <button className="sec" onClick={onClose} style={{ fontSize: 12 }}>Close</button>
          </span>
        </div>
        {series === undefined && <Spinner />}
        {series === null && (
          <div style={{ fontSize: 12, color: 'var(--err)' }}>
            Failed to load metric series.
          </div>
        )}
        {series && series.length === 0 && (
          <Empty icon="◯" title="No data points">
            No metric_points found for <code>{drill.metric}</code> in this window.
            Wire the OracleDB receiver against this instance to populate.
          </Empty>
        )}
        {series && series.length > 0 && (
          <MultiLineChart series={series} unit={drill.unit} height={360} />
        )}
      </div>
    </div>
  );
}

function GaugeStat({ label, usage, limit, sub, onClick }: {
  label: string; usage: number; limit: number; sub?: string;
  onClick?: () => void;
}) {
  const pct = limit > 0 ? (usage / limit) * 100 : 0;
  const tone: 'ok' | 'warn' | 'err' =
    pct >= 90 ? 'err' : pct >= 75 ? 'warn' : 'ok';
  const fill = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--ok)';
  const inner = (
    <>
      <div style={{
        fontSize: 9, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
        display: 'flex', alignItems: 'center',
      }}>
        {label}
        {onClick && (
          <span aria-hidden style={{ marginLeft: 'auto', opacity: 0.5 }}>↗</span>
        )}
      </div>
      <div style={{
        fontSize: 14, fontWeight: 700,
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        marginBottom: 4,
      }}>
        {fmtNum(usage)} <span style={{ color: 'var(--text3)', fontWeight: 400 }}>/ {fmtNum(limit)}</span>
      </div>
      <div style={{
        height: 4, background: 'var(--bg3)', borderRadius: 2, overflow: 'hidden',
      }}>
        <div style={{
          width: `${Math.min(100, pct)}%`, height: '100%', background: fill,
          transition: 'width 0.2s',
        }} />
      </div>
      {sub && (
        <div style={{
          fontSize: 10, color: 'var(--text3)', marginTop: 4,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>{sub}</div>
      )}
    </>
  );
  if (onClick) {
    return (
      <button type="button" onClick={onClick}
        title="Open metric chart"
        style={{
          all: 'unset', display: 'block', cursor: 'pointer',
          padding: '8px 10px', borderRadius: 4,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          transition: 'border-color 0.12s, background 0.12s',
        }}
        onMouseEnter={e => {
          e.currentTarget.style.borderColor = 'var(--accent2)';
          e.currentTarget.style.background = 'var(--bg3)';
        }}
        onMouseLeave={e => {
          e.currentTarget.style.borderColor = 'var(--border)';
          e.currentTarget.style.background = 'var(--bg2)';
        }}>
        {inner}
      </button>
    );
  }
  return (
    <div style={{
      padding: '8px 10px', borderRadius: 4,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      {inner}
    </div>
  );
}

// WaitClassesBar renders Oracle's 10 wait classes as a single
// stacked horizontal bar — at-a-glance "where is the DB
// spending its time". Mirrors the System Wait Classes panel
// in Oracle's reference Grafana dashboard. Sum of perSec
// across classes is the total wait pressure: a 1.0 result
// means one concurrent client fully blocked on the DB.
function WaitClassesBar({ waits, onClickClass }: {
  waits: { name: string; perSec: number }[];
  onClickClass?: (cls: string) => void;
}) {
  const total = waits.reduce((a, w) => a + w.perSec, 0);
  // Stable, semantic colour-per-class. user_io is the heaviest
  // typical class so we give it the most-visible blue; commit
  // gets green (success-coded); concurrency red (where row
  // locks live).
  const CLASS_COLOR: Record<string, string> = {
    user_io:       '#388bfd',
    system_io:     '#5b8fb9',
    commit:        '#3fb950',
    network:       '#a371f7',
    concurrency:   '#f0703f',
    application:   '#f5b343',
    configuration: '#39c5cf',
    scheduler:     '#db61a2',
    cluster:       '#7d8590',
    other:         '#6dbf5b',
  };
  const colorOf = (n: string) => CLASS_COLOR[n.toLowerCase()] ?? '#7d8590';
  if (total <= 0) return null;
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8,
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        System wait classes
        <span style={{
          fontWeight: 400, color: 'var(--text3)',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          textTransform: 'none', letterSpacing: 0,
        }}>
          total {total.toFixed(2)} s/s
        </span>
      </div>
      <div style={{
        display: 'flex', height: 18, borderRadius: 3, overflow: 'hidden',
        border: '1px solid var(--border)',
      }}>
        {waits.map(w => {
          const pct = (w.perSec / total) * 100;
          if (pct < 0.5) return null; // suppress sub-pixel slivers
          const handleClick = onClickClass ? () => onClickClass(w.name) : undefined;
          return (
            <div key={w.name}
              onClick={handleClick}
              title={`${w.name}: ${w.perSec.toFixed(3)} s/s (${pct.toFixed(1)}%)${handleClick ? ' · click to chart' : ''}`}
              style={{
                width: `${pct}%`, background: colorOf(w.name),
                cursor: handleClick ? 'pointer' : 'help',
              }} />
          );
        })}
      </div>
      <div style={{
        display: 'flex', flexWrap: 'wrap', gap: 10, marginTop: 6, fontSize: 10,
      }}>
        {waits
          .filter(w => w.perSec > 0)
          .slice(0, 8)
          .map(w => {
            const handleClick = onClickClass ? () => onClickClass(w.name) : undefined;
            const labelInner = (
              <>
                <span style={{
                  width: 8, height: 8, borderRadius: 2,
                  background: colorOf(w.name),
                }} />
                <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
                  {w.name}
                </span>
                <span style={{ color: 'var(--text3)' }}>
                  {w.perSec.toFixed(2)}
                </span>
              </>
            );
            if (handleClick) {
              return (
                <button key={w.name} type="button" onClick={handleClick}
                  title={`Chart wait time · ${w.name}`}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    display: 'inline-flex', alignItems: 'center', gap: 4,
                    color: 'var(--text2)',
                  }}>
                  {labelInner}
                </button>
              );
            }
            return (
              <span key={w.name} style={{
                display: 'inline-flex', alignItems: 'center', gap: 4,
                color: 'var(--text2)',
              }}>
                {labelInner}
              </span>
            );
          })}
      </div>
    </div>
  );
}

// TopSQLTable lists the heaviest SQL statements by accumulated
// elapsed time over the window — Oracle's authoritative
// "which statement is the DB working hardest on" view.
// Complementary to the span-derived "Top statements" further
// down: V$SQL sees everything the DB executes, traces only see
// what the application emits.
function TopSQLTable({ rows }: { rows: { sql: string; elapsedSec: number; executions: number; avgElapsedMs: number }[] }) {
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Top SQL by elapsed time
      </div>
      <div className="table-wrap" style={{ maxHeight: 240, overflowY: 'auto' }}>
        <table>
          <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
            <tr>
              <th>SQL</th>
              <th className="num">Elapsed</th>
              <th className="num">Execs</th>
              <th className="num">Avg</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                <td style={{
                  fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 11,
                  maxWidth: 600, wordBreak: 'break-word',
                }}>
                  {r.sql || <span style={{ color: 'var(--text3)' }}>(unknown)</span>}
                </td>
                <td className="num mono">{r.elapsedSec.toFixed(1)}s</td>
                <td className="num mono">{fmtNum(r.executions)}</td>
                <td className="num mono">{r.avgElapsedMs.toFixed(1)}ms</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

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

function fmtBytes(v: number): string {
  if (!isFinite(v) || v <= 0) return '0 B';
  if (v >= 1e12) return (v / 1e12).toFixed(2) + ' TB';
  if (v >= 1e9)  return (v / 1e9).toFixed(2)  + ' GB';
  if (v >= 1e6)  return (v / 1e6).toFixed(1)  + ' MB';
  if (v >= 1e3)  return (v / 1e3).toFixed(1)  + ' kB';
  return v.toFixed(0) + ' B';
}

// PostgresPanel — drill-down for one Postgres instance, mirrors
// OraclePanel's shape (status badge + KPI tiles + per-DB table).
// Tile clicks open the same metric-chart modal used by Oracle.
function PostgresPanel({ instance, range }: { instance: string; range: TimeRange }) {
  const [data, setData] = useState<PostgresMetrics | null | undefined>(undefined);
  const [drill, setDrill] = useState<OracleDrill | null>(null);
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.postgresMetrics(instance, from, to)
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [instance, range]);
  return (
    <div style={{
      marginTop: 6, marginBottom: 14, padding: 12, borderRadius: 6,
      background: 'rgba(51,103,145,0.05)',
      border: '1px solid rgba(91,143,185,0.25)',
    }}>
      <PanelHeader engineLabel="PostgreSQL receiver" instance={instance}
        status={data?.status} color="#5b8fb9" />
      {data === undefined && <Spinner />}
      {data === null && <PanelErr />}
      {data && (
        <>
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
          </div>

          {data.databases.length > 0 && (
            <div style={{ marginBottom: 12 }}>
              <SubHeader label={`Databases (${data.databases.length})`} />
              <div className="table-wrap" style={{ maxHeight: 240, overflowY: 'auto' }}>
                <table>
                  <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
                    <tr>
                      <th>Name</th>
                      <th className="num">Size</th>
                      <th className="num">Backends</th>
                      <th className="num">Commits/s</th>
                      <th className="num">Rollbacks/s</th>
                    </tr>
                  </thead>
                  <tbody>
                    {[...data.databases].sort((a, b) => b.sizeBytes - a.sizeBytes).map(d => (
                      <tr key={d.name}
                        onClick={() => setDrill({
                          metric: 'postgresql.database.size',
                          label: `Database · ${d.name}`,
                          unit: 'B',
                          filters: [{ key: 'database', op: '=', value: d.name }],
                        })}
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
            <div>
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
        </>
      )}
      {drill && (
        <OracleMetricDrillModal drill={drill} range={range} onClose={() => setDrill(null)} />
      )}
    </div>
  );
}

// MySQLPanel — drill-down for one MySQL/MariaDB instance.
function MySQLPanel({ instance, range }: { instance: string; range: TimeRange }) {
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
      background: warn ? 'rgba(245,179,67,0.15)' : 'var(--bg3)',
      color: warn ? 'var(--warn)' : 'var(--text2)',
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
    }}>
      {label} <span style={{ opacity: 0.7 }}>{fmtNum(v)}/s</span>
    </span>
  );
}

// RedisPanel — drill-down for one Redis instance.
function RedisPanel({ instance, range }: { instance: string; range: TimeRange }) {
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

// PanelHeader is the engine-tile chrome shared by Postgres /
// MySQL / Redis (and now Oracle by copy). status badge +
// optional secondary chip (Redis role) + instance label on
// the right. Centralised so all three panels read identically.
function PanelHeader({ engineLabel, instance, status, color, extraBadge }: {
  engineLabel: string;
  instance: string;
  status: 'up' | 'down' | undefined;
  color: string;
  extraBadge?: string;
}) {
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10,
      fontSize: 12, fontWeight: 700, color,
    }}>
      <span style={{ fontSize: 13 }}>⛁</span>
      {engineLabel}
      {status && (
        <span title={status === 'up'
          ? 'receiver metric_points present in window'
          : 'No receiver metric_points seen — receiver may be down or not yet wired'}
          style={{
            fontSize: 9, padding: '1px 6px', borderRadius: 3,
            background: status === 'up' ? 'rgba(63,185,80,0.15)' : 'rgba(248,81,73,0.15)',
            color: status === 'up' ? 'var(--ok)' : 'var(--err)',
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            textTransform: 'uppercase', letterSpacing: '.5px',
          }}>{status}</span>
      )}
      {extraBadge && (
        <span style={{
          fontSize: 9, padding: '1px 6px', borderRadius: 3,
          background: 'rgba(120,120,120,0.15)', color: 'var(--text2)',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          textTransform: 'uppercase', letterSpacing: '.5px',
        }}>{extraBadge}</span>
      )}
      <span style={{
        marginLeft: 'auto', fontSize: 10, color: 'var(--text3)',
        fontWeight: 400, fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      }}>
        instance: {instance || '(unknown)'}
      </span>
    </div>
  );
}

function PanelErr() {
  return (
    <div style={{ fontSize: 12, color: 'var(--err)' }}>Receiver metrics query failed.</div>
  );
}

function SubHeader({ label }: { label: string }) {
  return (
    <div style={{
      fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
      textTransform: 'uppercase', letterSpacing: 0.4,
    }}>{label}</div>
  );
}
