import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Spinner } from './Spinner';
import { DisclosureButton } from '@/components/ui';
import { useDataTable, DataTableColgroup, DataTableHead } from './DataTable';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import { encodeFilters } from '@/lib/urlState';
import type { DataTableColumn } from '@/lib/dataTable';
import type { DBQueryStat, FilterExpr } from '@/lib/types';
import { tracesPivotHref } from '@/lib/pivotHref';

// Database query analyzer — Datadog DBM-style "where is my
// query time going" view for a single service in a time
// window. Each row is a normalised DB statement (literals
// replaced with "?") aggregated across every span that ran
// it; the table is sorted by total wall-clock cost
// (count × avgMs) so the queries actually worth optimising
// land at the top.
//
// Click any row to expand: full sample statement (with real
// literals), p95 / p99 / max breakdown, and error rate. The
// panel starts collapsed so /service makes zero round-trips
// until the operator opens it — same pattern as
// ServiceStructure.
// Columns for the shared sortable + resizable DataTable primitive
// (v0.8.306 — replaces the hand-rolled SortTh/toggleSort pair).
// Default sort stays total wall-clock desc; Statement + DB gain
// sorting for free. The trailing Traces-drill column is layout-only
// (no sortValue → not clickable, still resizable).
const DBQ_COLS: DataTableColumn<DBQueryStat>[] = [
  { id: 'statement',  label: 'Statement', sortValue: r => r.statement,      naturalDir: 'asc', flex: true, minWidth: 160 },
  { id: 'dbSystem',   label: 'DB',        sortValue: r => r.dbSystem || '', naturalDir: 'asc', width: 90 },
  { id: 'count',      label: '×N',     sortValue: r => r.count,      numeric: true, width: 80 },
  { id: 'totalMs',    label: 'Total',  sortValue: r => r.totalMs,    numeric: true, width: 90 },
  { id: 'avgMs',      label: 'Avg',    sortValue: r => r.avgMs,      numeric: true, width: 80 },
  { id: 'p95Ms',      label: 'P95',    sortValue: r => r.p95Ms,      numeric: true, width: 80 },
  { id: 'p99Ms',      label: 'P99',    sortValue: r => r.p99Ms,      numeric: true, width: 80 },
  { id: 'maxMs',      label: 'Max',    sortValue: r => r.maxMs,      numeric: true, width: 80 },
  { id: 'errorCount', label: 'Errors', sortValue: r => r.errorCount, numeric: true, width: 110 },
  { id: 'traces',     label: '',       width: 90 },
];

// Kaç normalleştirilmiş statement gösterilir. Sunucu ağırlığa göre sıralı
// döndürüyor, yani kırpılan kuyruk EN HAFİF olanlar — ama bu, kırpmanın
// söylenmemesini haklı çıkarmaz (v0.9.349).
const DBQ_LIMIT = 100;

export function DBQueriesPanel({ service, from, to, defaultOpen = false }: {
  service: string;
  from: number;
  to: number;
  // v0.5.294 — render expanded on first paint when the caller
  // has already signalled "show me details" (Service detail
  // Details tab). Per-row expand-for-EXPLAIN still works
  // independently.
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [data, setData] = useState<DBQueryStat[] | null | undefined>(undefined);
  // v0.9.349 — "daha fazlası var mı" sondası. Bkz. fetch aşağıda.
  const [capped, setCapped] = useState(false);
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);

  useEffect(() => {
    if (!open || !service) return;
    setData(undefined);
    // v0.9.349 — limit+1 sondası (/services'in probeLimit deseni).
    //
    // Panel 100 satır çekiyor ve bunu HİÇBİR YERDE söylemiyordu: 100'den
    // fazla farklı statement'ı olan bir serviste en ağır 100'ü gösterip
    // "sorgularımın hepsi bu" diye okunuyordu. CLAUDE.md'nin sessiz-kırpma
    // yasağı; bu oturumda aynı kural inbox listesinde (v0.9.221), aday
    // taramasında (v0.9.318) ve /services süzgeçlerinde (v0.9.344) uygulandı.
    //
    // Bir fazlasını istemek "tam 100 var" ile "100'den fazla var"ı ayırt
    // ediyor — `rows.length === 100` tek başına ikisini ayıramaz.
    api.serviceDBQueries(service, { from, to, limit: DBQ_LIMIT + 1 })
      .then(rows => {
        const all = rows ?? [];
        setCapped(all.length > DBQ_LIMIT);
        setData(all.slice(0, DBQ_LIMIT));
      })
      .catch(() => { setCapped(false); setData(null); });
  }, [open, service, from, to]);

  // Shared sortable + resizable table. Client sort — the panel holds
  // its whole result set (one bounded fetch, limit 100), so there is
  // no server ordering to preserve. Called unconditionally (hooks
  // rule) with [] while collapsed/loading.
  const dt = useDataTable<DBQueryStat>({
    storageKey: 'db-queries-panel',
    columns: DBQ_COLS,
    rows: data ?? [],
    initialSort: { id: 'totalMs', dir: 'desc' },
  });

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, marginBottom: 14,
    }}>
      <DisclosureButton anatomy="section" expanded={open} onClick={() => setOpen(o => !o)}>
        <span style={{ fontSize: 12, color: 'var(--text2)', fontWeight: 600 }}>
          DB queries by <span style={{ color: 'var(--text)' }}>{service}</span>
        </span>
        {open && data && data.length > 0 && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            {data.length} normalised statement{data.length === 1 ? '' : 's'}
          </span>
        )}
        {/* v0.9.349 — sınır varsa görünür olacak. Yalnız GERÇEKTEN kırpıldıysa
            çıkıyor: tam 100 statement'ı olan bir servis uyarı görmüyor. */}
        {open && capped && (
          <span className="badge b-warn"
            title={`En ağır ${DBQ_LIMIT} statement gösteriliyor — bu serviste daha fazlası var. Sıralama sunucuda, yani kırpılan kuyruk en hafif olanlar.`}>
            ⚠ ilk {DBQ_LIMIT}
          </span>
        )}
        <span style={{ flex: 1 }} />
        {!open && (
          <span style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
            click to expand
          </span>
        )}
      </DisclosureButton>

      {open && (
        <div style={{ padding: 14, paddingTop: 10 }}>
          {data === undefined && (
            <div style={{ minHeight: 120, display: 'grid', placeItems: 'center' }}>
              <Spinner />
            </div>
          )}
          {data === null && (
            <div style={{ fontSize: 12, color: 'var(--err)', padding: '12px 4px' }}>
              Failed to load DB queries.
            </div>
          )}
          {data && data.length === 0 && (
            <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic', padding: '12px 4px' }}>
              No spans with <code>db.statement</code> from <code>{service}</code> in this window.
              {' '}If your DB instrumentation strips statements for security, that's expected.
            </div>
          )}
          {data && data.length > 0 && (
            <div className="table-wrap">
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={dt} />
                <DataTableHead dt={dt} />
                <tbody>
                  {dt.sortedRows.map((r, i) => {
                    const expanded = expandedIdx === i;
                    const errPct = r.count > 0 ? (r.errorCount / r.count) * 100 : 0;
                    const errCls = errPct > 5 ? 'b-err' : errPct > 0 ? 'b-warn' : 'b-ok';
                    return (
                      <Row key={i}>
                        <tr onClick={() => setExpandedIdx(e => e === i ? null : i)}
                            style={{ cursor: 'pointer', ...(dt.sortedRows.length > 100 ? { contentVisibility: 'auto', containIntrinsicSize: 'auto 34px' } : null) }}>
                          <td className="mono"
                              style={{ maxWidth: 540, overflow: 'hidden',
                                       textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                                       fontSize: 12 }}
                              title={r.statement}>
                            {r.statement}
                          </td>
                          <td>
                            <span style={{
                              fontSize: 11, padding: '1px 6px',
                              background: 'var(--bg3)', borderRadius: 3,
                              fontFamily: 'monospace',
                            }}>
                              {r.dbSystem || '—'}
                            </span>
                          </td>
                          <td className="mono num">{fmtNum(r.count)}</td>
                          <td className="mono num">{fmtMs(r.totalMs)}</td>
                          <td className="mono num">{fmtMs(r.avgMs)}</td>
                          <td className="mono num">{fmtMs(r.p95Ms)}</td>
                          <td className="mono num">{fmtMs(r.p99Ms)}</td>
                          <td className="mono num">{fmtMs(r.maxMs)}</td>
                          <td className="num">
                            {r.errorCount > 0
                              ? <span className={`badge ${errCls}`}>{r.errorCount} ({errPct.toFixed(1)}%)</span>
                              : <span style={{ color: 'var(--text3)' }}>0</span>}
                          </td>
                          {/* Traces drill — link to /traces filtered by
                              service + db.statement LIKE the normalised
                              form. The normalised statement's "?"
                              placeholders are converted to SQL LIKE "%"
                              wildcards so all literal variants of the
                              same query class show up in one search. */}
                          <td onClick={e => e.stopPropagation()}>
                            <Link to={tracesURL(service, r, { fromNs: from, toNs: to })}
                                  className="sec"
                                  title={`Open /traces filtered to ${service} + this query class`}
                                  style={{
                                    display: 'inline-flex', alignItems: 'center', gap: 4,
                                    fontSize: 11, padding: '2px 8px',
                                    border: '1px solid var(--border)',
                                    borderRadius: 4,
                                    color: 'var(--text)', textDecoration: 'none',
                                    fontFamily: 'inherit',
                                  }}>
                              Traces →
                            </Link>
                          </td>
                        </tr>
                        {expanded && (
                          <tr>
                            <td colSpan={10}
                                style={{ background: 'var(--bg0)', padding: '12px 16px' }}>
                              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>
                                Sample statement (with real literals)
                              </div>
                              <pre style={{
                                margin: 0, fontSize: 12, lineHeight: 1.5,
                                whiteSpace: 'pre-wrap', overflowWrap: 'anywhere',
                                color: 'var(--text)',
                                background: 'var(--bg1)',
                                border: '1px solid var(--border)',
                                borderRadius: 6,
                                padding: '10px 12px',
                                fontFamily: 'monospace',
                              }}>
                                {r.sampleStatement}
                              </pre>
                              <div style={{
                                marginTop: 8, display: 'flex', gap: 14,
                                fontSize: 11, color: 'var(--text2)',
                                flexWrap: 'wrap',
                              }}>
                                <Stat label="executions" value={fmtNum(r.count)} />
                                <Stat label="total"     value={fmtMs(r.totalMs)} />
                                <Stat label="avg"       value={fmtMs(r.avgMs)} />
                                <Stat label="p95"       value={fmtMs(r.p95Ms)} />
                                <Stat label="p99"       value={fmtMs(r.p99Ms)} />
                                <Stat label="max"       value={fmtMs(r.maxMs)} />
                                <Stat label="errors"    value={`${r.errorCount} (${errPct.toFixed(2)}%)`} />
                              </div>
                            </td>
                          </tr>
                        )}
                      </Row>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Row({ children }: { children: React.ReactNode }) {
  // Tiny wrapper so we can return both the main row and the
  // expansion row from a single iteration without adding a
  // <Fragment> at every call site.
  return <>{children}</>;
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span style={{ display: 'inline-flex', gap: 6, alignItems: 'baseline' }}>
      <span style={{ color: 'var(--text3)' }}>{label}</span>
      <span style={{ fontFamily: 'monospace', color: 'var(--text)' }}>{value}</span>
    </span>
  );
}

function fmtMs(ms: number): string {
  if (ms >= 1000) return (ms / 1000).toFixed(2) + 's';
  if (ms >= 10)   return ms.toFixed(0) + 'ms';
  if (ms >= 1)    return ms.toFixed(1) + 'ms';
  return ms.toFixed(2) + 'ms';
}

// tracesURL — build a /traces deep link filtered to spans that
// match this query class for the focused service. Two filters:
//   • service.name = <svc>          (exact match)
//   • db.statement LIKE <pattern>   (LIKE with `%` placeholders
//                                    instead of the normalised
//                                    `?`, so every literal
//                                    variant of the query class
//                                    matches)
// Falls back to exact match on the sample statement when the
// normalised form is empty.
//
// Two URL flags pin the landing experience:
//   • view=list      — /traces defaults to 'aggregate' (group
//                      by op/service); db spans live deep in
//                      the hierarchy and the aggregated view
//                      collapses every match into one row, so
//                      the operator sees nothing useful.
//   • rootOnly=false — /traces defaults to "root traces only"
//                      so a search for a DB-statement filter
//                      finds nothing (db spans are never the
//                      trace root). Disabling root-only lets
//                      the search hit non-root spans.
// v0.9.862 (UX denetimi Ö1) — PENCERE. Bu link `range` yazmıyordu: zoom'lu
// bir custom pencerede DB satırının "traces" linki sticky pencereyle
// açılıyor, boş liste "bu sorgunun trace'i yok" diye okunuyordu. Panel
// from/to'yu PROP olarak zaten biliyordu. pivotHref.ts bu sınıfı kendi
// yorumunda "the exact class pivotHref exists to prevent" diye belgeliyor —
// bu yüzden pencere artık ZORUNLU argüman.
export function tracesURL(
  service: string, r: DBQueryStat, window: { fromNs: number; toNs: number },
): string {
  const filters: FilterExpr[] = [
    { k: 'service.name', op: '=', v: [service] },
  ];
  const norm = (r.statement || '').trim();
  if (norm) {
    // Convert normalisation `?` to SQL LIKE `%`. Escape any
    // existing `%` / `_` / `\` so they're treated as literal
    // characters rather than additional wildcards.
    const escaped = norm
      .replace(/\\/g, '\\\\')
      .replace(/%/g, '\\%')
      .replace(/_/g, '\\_');
    const pattern = escaped.replace(/\?/g, '%');
    filters.push({ k: 'db.statement', op: 'LIKE', v: [pattern] });
  } else if (r.sampleStatement) {
    filters.push({ k: 'db.statement', op: '=', v: [r.sampleStatement] });
  }
  return tracesPivotHref({
    window,
    // view=list: /traces aggregate görünümü her eşleşmeyi tek satıra
    // çökertirdi. rootOnly=false: db span'i asla kök değildir.
    view: 'list',
    rootOnly: false,
    filters: encodeFilters(filters),
  });
}
