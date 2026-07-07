import { Fragment, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Empty } from './Spinner';
import { Sparkline } from './Sparkline';
import { TrendDelta } from './TrendDelta';
import { Button } from './ui/Button';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from './DataTable';
import { DetailDrawer } from '@/features/dependencies/DetailDrawer';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, DBTrend } from '@/lib/types';

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
  // v0.8.364 (Stage-2 M1) — messaging-only producer/consumer split,
  // p50, and prior-window deltas. The PAGE computes the /min rates
  // (it owns the window length); raw counts ride along for the
  // error-percentage tooltips. All optional — DB rows never set
  // them and the columns only render for kind='queue'.
  producePerMin?: number;
  consumePerMin?: number;
  produceCount?: number;
  consumeCount?: number;
  produceErrors?: number;
  consumeErrors?: number;
  p50DurationMs?: number;
  // Prior equal-length window (compare=prior) — undefined when the
  // row had no prior twin, so delta badges stay hidden.
  priorSpanCount?: number;
  priorErrorCount?: number;
  priorProducePerMin?: number;
  priorConsumePerMin?: number;
  priorAvgMs?: number;
  priorP50Ms?: number;
  priorP99Ms?: number;
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
  rows, kind, range, compare, extraControls, openRowKey, onOpenRowChange,
}: {
  rows: DepRow[];
  // 'db' → uses instance + filters by db.system; 'queue' → uses
  // destination + filters by messaging.system.
  kind: 'db' | 'queue';
  // Time range — drives the detail drawer's per-(service, pod)
  // breakdown query. Same window the parent /databases or
  // /messaging page uses for the overview.
  range: TimeRange;
  // v0.8.364 (Stage-2 M1) — when true, rows carry prior* fields and
  // the metric cells render TrendDelta badges (endpoints pattern).
  compare?: boolean;
  // Extra page-owned controls (e.g. the "Compare vs prior" toggle)
  // rendered inside the filter row so the page keeps one strip.
  extraControls?: ReactNode;
  // v0.8.364 — controlled drawer mode for URL-first pages. When
  // onOpenRowChange is provided the parent owns which row is open
  // (openRowKey, same `system|cluster|name` shape as the internal
  // key) and receives the clicked row (null = close). Uncontrolled
  // pages (/databases) keep the internal useState behaviour.
  openRowKey?: string | null;
  onOpenRowChange?: (row: DepRow | null) => void;
}) {
  const [systemFilter, setSystemFilter] = useState<string>('');
  const [search, setSearch] = useState('');
  // Which row's drawer is open. Stores `system|cluster|name` so the
  // drawer survives sort + filter changes (stable identifiers).
  // Controlled mode (v0.8.364) hands ownership to the parent so
  // /messaging can drive it from the ?destination= URL param.
  const [openKeyState, setOpenKeyState] = useState<string | null>(null);
  const controlled = onOpenRowChange !== undefined;
  const openKey = controlled ? (openRowKey ?? null) : openKeyState;
  const setOpen = (row: DepRow | null, key: string | null) => {
    if (controlled) onOpenRowChange!(row);
    else setOpenKeyState(key);
  };
  // #1 sparkline + #6 health-chip source. One DBTrend per
  // (dbSystem, instance, dbName); we join to the overview rows
  // by (system, instance, dbName) — see trendFor below. null =
  // backend returned null / fetch failed (render the '—'
  // placeholder), undefined = not yet loaded.
  const [trends, setTrends] = useState<Map<string, DBTrend> | null | undefined>(undefined);

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
    // db.name only makes sense for databases — Kafka/RabbitMQ/etc.
    // (kind === 'queue') have no db.name, so the column is db-only.
    ...(kind === 'db'
      ? [{ id: 'database', label: 'Database', sortValue: (r: DepRow) => r.dbName ?? '', naturalDir: 'asc', width: 120 } as DataTableColumn<DepRow>]
      : []),
    { id: 'spanCount', label: 'Calls', sortValue: r => r.spanCount, numeric: true, naturalDir: NATURAL.spanCount, width: 96 },
    // v0.8.364 (Stage-2 M1) — messaging-only producer/consumer
    // split. Rates are precomputed by the page (it owns the window
    // length); zero-produce / zero-consume destinations sort to the
    // bottom naturally.
    ...(kind === 'queue'
      ? [
          { id: 'produce', label: 'Produce/min', sortValue: (r: DepRow) => r.producePerMin ?? 0, numeric: true, naturalDir: 'desc', width: 116 } as DataTableColumn<DepRow>,
          { id: 'consume', label: 'Consume/min', sortValue: (r: DepRow) => r.consumePerMin ?? 0, numeric: true, naturalDir: 'desc', width: 116 } as DataTableColumn<DepRow>,
        ]
      : []),
    { id: 'errorRate', label: 'Err %', sortValue: r => r.errorRate, numeric: true, naturalDir: NATURAL.errorRate, width: 96 },
    { id: 'avg', label: 'Avg', sortValue: r => r.avgDurationMs, numeric: true, naturalDir: NATURAL.avg, width: 90 },
    // v0.8.364 — P50 alongside P99 (queue-only; the DB grid keeps
    // its existing shape). Same TDigest state the MV always had.
    ...(kind === 'queue'
      ? [{ id: 'p50', label: 'P50', sortValue: (r: DepRow) => r.p50DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 } as DataTableColumn<DepRow>]
      : []),
    { id: 'p99', label: 'P99', sortValue: r => r.p99DurationMs, numeric: true, naturalDir: NATURAL.p99, width: 90 },
    // #1 — non-sortable RED sparkline column. No sortValue so the
    // shared DataTable head renders it as a plain (un-clickable)
    // header. Body cell joins the row to its DBTrend via trendFor.
    { id: 'trend', label: 'Trend', width: 140 },
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

  // #1 + #6 — fetch per-row RED trends on range change. Kept
  // unconditional (above the rows.length===0 early return) so the
  // hook order is stable. Stores two keys per trend in the Map:
  // the precise (system|instance|dbName) and a looser
  // (system|instance) fallback — db.name on the trend ('' /
  // 'default') doesn't always line up with the row's dbName, so
  // trendFor tries exact first then falls back to (system,
  // instance). cluster is empty for DB rows so it isn't part of
  // the join.
  useEffect(() => {
    let live = true;
    setTrends(undefined);
    const { from, to } = timeRangeToNs(range);
    api.dbTrends(from, to)
      .then(list => {
        if (!live) return;
        if (!list) { setTrends(null); return; }
        const m = new Map<string, DBTrend>();
        for (const t of list) {
          m.set(`${t.dbSystem}|${t.instance}|${t.dbName}`, t);
          // Looser fallback key — first writer wins so a real
          // db.name'd trend isn't clobbered by a 'default' sibling.
          const loose = `${t.dbSystem}|${t.instance}`;
          if (!m.has(loose)) m.set(loose, t);
        }
        setTrends(m);
      })
      .catch(() => { if (live) setTrends(null); });
    return () => { live = false; };
  }, [range]);

  // trendFor — join one overview row to its DBTrend. Match on
  // (system, instance, dbName) exactly; fall back to
  // (system, instance) when dbName doesn't line up (trend's
  // db.name may be '' / 'default' while the row carries a real
  // one, or vice-versa). nameOf(r) is the instance/destination.
  const trendFor = (r: DepRow): DBTrend | undefined => {
    if (!trends) return undefined;
    const inst = nameOf(r);
    const db = r.dbName ?? '';
    return trends.get(`${r.system}|${inst}|${db}`)
        ?? trends.get(`${r.system}|${inst}`);
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
        {extraControls}
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
                  <tr onClick={() => setOpen(isOpen ? null : r, isOpen ? null : rowKey)}
                      style={{ cursor: 'pointer',
                               // scale-audit v0.8.203 — skip off-screen rows
                               // (matches the instance table below); at a bank
                               // with many DB schemas this list reaches 1000s.
                               contentVisibility: 'auto', containIntrinsicSize: 'auto 32px',
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
                    </td>
                    {/* v0.5.315 / dedicated Database column — one host
                        serving N databases (Oracle SID/service,
                        PostgreSQL / MongoDB / MSSQL DB) → row is keyed
                        on (host, dbName). Surfaced as its own column
                        (was an inline ⛁ chip) so the operator can scan
                        + sort by database. '—' for the 'default'
                        fallback (OTel instrumentation didn't emit
                        db.name). */}
                    {kind === 'db' && (
                      <td>
                        {r.dbName && r.dbName !== 'default' ? (
                          <span title={`db.name = ${r.dbName}`}
                            style={{
                              fontSize: 10,
                              padding: '1px 6px', borderRadius: 3,
                              background: 'var(--bg3)',
                              border: '1px solid var(--border)',
                              color: 'var(--text2)',
                              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                              verticalAlign: 'middle',
                            }}>
                            ⛁ {r.dbName}
                          </span>
                        ) : (
                          <span style={{ color: 'var(--text3)' }}>—</span>
                        )}
                      </td>
                    )}
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {fmtNum(r.spanCount)}
                      {compare && <TrendDelta cur={r.spanCount} prior={r.priorSpanCount} kind="neutral" />}
                    </td>
                    {/* v0.8.364 (Stage-2 M1) — producer/consumer split.
                        Per-kind error % badge (scale-free, same
                        semantics as the row's Err % column) instead of
                        raw error counts; the tooltip carries the raw
                        numbers for the postmortem screenshot. */}
                    {kind === 'queue' && (
                      <>
                        <KindRateCell perMin={r.producePerMin} count={r.produceCount}
                          errors={r.produceErrors} priorPerMin={r.priorProducePerMin}
                          compare={compare} what="produce" />
                        <KindRateCell perMin={r.consumePerMin} count={r.consumeCount}
                          errors={r.consumeErrors} priorPerMin={r.priorConsumePerMin}
                          compare={compare} what="consume" />
                      </>
                    )}
                    <td className="mono" style={{ textAlign: 'right' }}>
                      <span className={`badge b-${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                      {compare && <TrendDelta cur={r.errorCount} prior={r.priorErrorCount} kind="lowerBetter" />}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {r.avgDurationMs.toFixed(1)}ms
                      {compare && <TrendDelta cur={r.avgDurationMs} prior={r.priorAvgMs} kind="lowerBetter" />}
                    </td>
                    {kind === 'queue' && (
                      <td className="mono" style={{ textAlign: 'right' }}>
                        {r.p50DurationMs === undefined
                          ? <span style={{ color: 'var(--text3)' }}>—</span>
                          : <>{r.p50DurationMs.toFixed(1)}ms</>}
                        {compare && r.p50DurationMs !== undefined
                          && <TrendDelta cur={r.p50DurationMs} prior={r.priorP50Ms} kind="lowerBetter" />}
                      </td>
                    )}
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {r.p99DurationMs.toFixed(1)}ms
                      {compare && <TrendDelta cur={r.p99DurationMs} prior={r.priorP99Ms} kind="lowerBetter" />}
                    </td>
                    {/* #1 Trend + #6 health chips. Sparkline plots
                        the call-rate (rps) over the window; it flips
                        red/amber when the trend's CURRENT error rate
                        is elevated so a row reads "unhealthy" without
                        a mouse-over. Under it, compact p99 + err
                        chips surface the latest-bucket gauge. '—'
                        when no trend joins (or trends failed/loading). */}
                    <td onClick={e => e.stopPropagation()}>
                      <TrendCell trend={trendFor(r)} loading={trends === undefined} />
                    </td>
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
                      {/* colSpan tracks the conditional columns: base 9
                          + cluster + Database (db) + produce/consume/p50
                          (queue, v0.8.364). */}
                      <td colSpan={9 + (hasClusterCol ? 1 : 0) + (kind === 'db' ? 1 : 0) + (kind === 'queue' ? 3 : 0)} style={{
                        background: 'var(--bg1)', padding: '12px 16px',
                        borderTop: '1px solid var(--border)',
                      }}>
                        {/* v0.8.364 — explicit ✕ affordance (pairs with
                            the page-level Esc handler on /messaging;
                            row re-click still toggles too). */}
                        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 2 }}>
                          <Button variant="ghost" size="sm" aria-label="Close detail"
                            title="Close detail (Esc)"
                            onClick={() => setOpen(null, null)}>✕</Button>
                        </div>
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

// fmtPerMin — Produce/min · Consume/min cells (v0.8.364). Sub-10
// rates keep one decimal so a trickle topic doesn't read "0";
// larger rates round to locale ints (the endpoints fmtRate shape).
// '—' when the backend predates the split (rolling deploy).
function fmtPerMin(v?: number): string {
  if (v === undefined || v === null) return '—';
  return v < 10 ? v.toFixed(1) : fmtNum(Math.round(v));
}

// KindRateCell — one Produce/min or Consume/min cell (v0.8.364,
// Stage-2 M1). Renders the rate, a per-kind error % badge when that
// side errored (percentage, not raw count: scale-free and it reads
// on the same axis as the row's Err % column — the raw counts live
// in the tooltip), and the compare=prior delta badge.
function KindRateCell({ perMin, count, errors, priorPerMin, compare, what }: {
  perMin?: number;
  count?: number;
  errors?: number;
  priorPerMin?: number;
  compare?: boolean;
  what: 'produce' | 'consume';
}) {
  const errPct = count && count > 0 && errors ? (errors / count) * 100 : 0;
  const errTone = errPct > 5 ? 'err' : errPct > 0 ? 'warn' : null;
  return (
    <td className="mono" style={{ textAlign: 'right' }}>
      {fmtPerMin(perMin)}
      {errTone && (
        <span className={`badge b-${errTone}`} style={{ marginLeft: 4, fontSize: 9 }}
          title={`${fmtNum(errors ?? 0)} of ${fmtNum(count ?? 0)} ${what} spans errored`}>
          {errPct.toFixed(1)}%
        </span>
      )}
      {compare && <TrendDelta cur={perMin ?? 0} prior={priorPerMin} kind="neutral" />}
    </td>
  );
}

// TrendCell renders the #1 RED sparkline + #6 latest-bucket
// health chips for one overview row. Plots the call-rate (rps)
// series; the sparkline tints red when the trend's CURRENT error
// rate is high (>5) / amber (>1) reusing the badge tone vars.
// Under the sparkline, compact p99 + err chips surface the
// latest-bucket gauge (curP99Ms tinted by threshold; the err
// chip only shows when curErrorRate > 0). A missing trend (no
// join / failed / still loading) renders a muted '—'.
function TrendCell({ trend, loading }: {
  trend: DBTrend | undefined;
  loading: boolean;
}) {
  if (!trend) {
    return (
      <span title={loading ? 'loading trend…' : 'no trend in this window'}
        style={{ color: 'var(--text3)', fontSize: 11 }}>—</span>
    );
  }
  const rps = trend.points.map(p => p.rps);
  // Health tone from the latest-bucket gauge — drives both the
  // sparkline colour and the err chip. Mirrors the row's errCls
  // thresholds (>5 err, >0 warn) so the eye doesn't recalibrate.
  const errTone: 'err' | 'warn' | 'ok' =
    trend.curErrorRate > 5 ? 'err'
    : trend.curErrorRate > 1 ? 'warn' : 'ok';
  const sparkColor =
    errTone === 'err' ? 'var(--err)'
    : errTone === 'warn' ? 'var(--warn)'
    : undefined; // undefined → Sparkline's default --accent2
  // p99 chip tone — same ms thresholds the drawer's Stat tiles
  // use elsewhere wouldn't fit (those are domain-specific); a
  // generic latency band reads fine here: >500ms err, >200ms warn.
  const p99Tone: 'err' | 'warn' | 'ok' =
    trend.curP99Ms > 500 ? 'err'
    : trend.curP99Ms > 200 ? 'warn' : 'ok';
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <Sparkline
        values={rps}
        width={120}
        height={20}
        color={sparkColor}
        unit="/s"
        title={`call-rate · cur ${trend.curRps.toFixed(1)}/s`} />
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <span className={`badge b-${p99Tone}`} style={{ fontSize: 9 }}
          title="latest-bucket p99">
          {trend.curP99Ms.toFixed(0)}ms
        </span>
        {trend.curErrorRate > 0 && (
          <span className={`badge b-${errTone}`} style={{ fontSize: 9 }}
            title="latest-bucket error rate">
            {trend.curErrorRate.toFixed(1)}%
          </span>
        )}
      </div>
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
