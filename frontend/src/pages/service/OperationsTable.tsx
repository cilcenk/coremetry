import { useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { TrendDelta } from '@/components/TrendDelta';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from '@/components/Spinner';
import { Sparkline } from '@/components/Sparkline';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Modal } from '@/components/ui';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { encodeFilters, encodeRange, buildQuery } from '@/lib/urlState';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, OperationSummary, SpanMetricSeries, SpanAgg } from '@/lib/types';

// OperationsTable — per-operation aggregate (count / err / avg / p50 /
// p95 / p99 / apdex). Click an operation to drill into Traces with
// the service + operation name pre-filtered. Sortable; aggregate
// "All" row at the top mirrors the services page so totals are visible
// without scrolling.
//
// v0.7.54 — adopted the shared sortable + resizable table primitive
// (useDataTable / DataTableHead / DataTableColgroup). The bespoke
// OpSortKey state + OpSortTh header + manual .sort() were replaced by
// the OP_COLS column defs below; client-side FILTER (name substring)
// stays and feeds the hook as `rows`. Default sort preserved: impact
// desc (Elastic-APM's heaviest-cumulative-consumer first). Trend /
// P50 / P95 stay non-sortable (no sortValue) — they had no sort before.
// Split out of the Service.tsx monolith (v0.8.252 refactor) verbatim.
function impactOf(r: OperationSummary): number {
  return r.avgDurationMs * r.spanCount;
}

// Elastic-APM's "Impact" = avg_duration × count. Surfaces the
// heaviest cumulative consumers — the operation that's slow OR
// runs a lot. A 5ms operation called 100k times shows up; a
// once-an-hour 30s job doesn't. Default sort so the top of the
// table answers "what should I optimise first" without the
// operator combining columns by eye.
// Apdex cell color (README Status semantics): ≥0.94 ok, ≥0.85 warn, else err.
function apdexColor(a: number): string {
  if (!isFinite(a)) return 'var(--text3)';
  return a >= 0.94 ? 'var(--ok)' : a >= 0.85 ? 'var(--warn)' : 'var(--err)';
}

// v0.9.61 (Elastic-parity 2/3, onaylı mockup) — 10 kolon → 5:
// Avg/P50/P95/P99 tek "Latency" kolonuna indi (başlıkta percentile
// seçici, ?lat= URL'de), Calls → Throughput (rps), Trend kolonu
// kalktı (trend artık her metrik hücresinin içinde), Apdex
// varsayılandan çıktı (operasyon detay modalında yaşıyor). Her metrik
// hücresi Elastic'in ListMetric'i: sparkline + değer + TrendDelta.
type LatKey = 'avg' | 'p50' | 'p95' | 'p99';
const LAT_KEYS: LatKey[] = ['avg', 'p50', 'p95', 'p99'];
function latOf(r: OperationSummary, k: LatKey): number {
  return k === 'avg' ? r.avgDurationMs : k === 'p50' ? r.p50DurationMs
    : k === 'p95' ? r.p95DurationMs : r.p99DurationMs;
}
function latSparkOf(r: OperationSummary, k: LatKey): number[] | undefined {
  return k === 'avg' ? r.avgSparkline : k === 'p50' ? r.p50Sparkline
    : k === 'p95' ? r.p95Sparkline : r.p99Sparkline;
}
function priorLatOf(r: OperationSummary, k: LatKey): number {
  return k === 'avg' ? (r.priorAvgDurationMs ?? 0) : k === 'p50' ? (r.priorP50DurationMs ?? 0)
    : k === 'p95' ? (r.priorP95DurationMs ?? 0) : (r.priorP99DurationMs ?? 0);
}
function fmtMsShort(v: number): string {
  if (!isFinite(v)) return '—';
  if (v >= 1000) return `${(v / 1000).toFixed(2)}s`;
  return `${v.toFixed(v < 10 ? 1 : 0)}ms`;
}
function fmtRps(v: number): string {
  return v >= 100 ? `${v.toFixed(0)} rps` : v >= 1 ? `${v.toFixed(1)} rps` : `${v.toFixed(2)} rps`;
}

export function OperationsTable({ service, rows, range, preset, onWiden, normalized, onToggleNormalized, compare, onToggleCompare, loading }: {
  service: string;
  rows: OperationSummary[];
  range: TimeRange;
  // v0.5.292 — when the table comes back empty (typically
  // because the user's 15m default window had no traffic),
  // surface a one-click "widen to 1h" CTA rather than the
  // bare "no operations" message. preset is read to scope
  // the suggestion ("widen to 1h" only makes sense on short
  // windows; on a 7d range, empty really means empty).
  preset?: string;
  onWiden?: () => void;
  // group_id rel C — Raw ⇄ Normalized toggle. normalized reflects
  // the current mode; onToggleNormalized flips it (the parent owns
  // the fetch). loading covers the normalized refetch so the table
  // shows a Spinner instead of flashing the previous mode's rows.
  normalized: boolean;
  onToggleNormalized: (v: boolean) => void;
  // v0.9.61 — ?compare=prior: bir-önceki eş-pencere gölge serileri +
  // delta çipleri. Parent fetch'i ve URL paramını sahiplenir.
  compare: boolean;
  onToggleCompare: (v: boolean) => void;
  loading: boolean;
}) {
  // v0.5.374 — client-side filter. At 500+ operations on a
  // monolith service the scroll-then-eyeball loop fails;
  // typing narrows live with no server round-trip.
  const [filter, setFilter] = useState('');
  const navigate = useNavigate();
  // Filter input ref — wired into useDataTable's searchRef so the
  // app-wide "/" shortcut focuses it (UX#4 keyboard nav).
  const searchRef = useRef<HTMLInputElement>(null);
  // v0.5.392 — per-row metric drill-in. Clicking the sparkline
  // opens a Modal with three synced uPlot charts (calls, errors,
  // p99) for the same (service, op) tuple. Same pattern the
  // endpoints page uses; here it pulls from the row's stored
  // sparkline + companion errors/p99 sparklines added in the
  // same release.
  const [opDetail, setOpDetail] = useState<OperationSummary | null>(null);

  // v0.5.313 — Operator-reported: drill-down used to land on
  // /traces (familiar view with the trace list + aggregate
  // tabs). Recent refactor pushed it to /explore which the
  // operator finds less direct. Reverted to /traces with the
  // service pre-selected and the operation name as the search
  // term. /traces' free-text search matches span name out of
  // the box, so an operation like "POST /payment" lands on
  // exactly the traces that touched it.
  // v0.5.317 — Operator-reported: prior link landed on the
  // Aggregated tab (default) where filter+search produced no
  // results, and rootOnly defaulted ON, hiding partial traces.
  // Now: explicit ?view=list&rootOnly=false so the operator
  // lands on the list view with every matching trace visible.
  const opHref = (op: string) =>
    `/traces?service=${encodeURIComponent(service)}&filters=${encodeURIComponent(encodeFilters([{ k: 'name', op: '=', v: [op] }]))}&range=${encodeURIComponent(encodeRange(range))}&view=list&rootOnly=false`; // v0.8.488 — kesin isim filtresi (search değil)

  // v0.8.416 (Tempo-parity T4) — row → the operation-scoped Details
  // view (?op=, v0.8.414/415): RED triple with the percentile band +
  // the latency heatmap narrowed to exactly this operation. The name
  // link keeps going to Traces (operator-picked, v0.5.317); this is
  // the metrics-shaped sibling.
  const detailsHref = (op: string) =>
    `/service?name=${encodeURIComponent(service)}&tab=details&op=${encodeURIComponent(op)}&range=${encodeURIComponent(encodeRange(range))}`;

  // v0.5.374 — client-side filter. Case-insensitive substring
  // match on the operation name, same idiom as the /endpoints
  // page filter. The shared table primitive (useDataTable below)
  // owns the SORT half; we feed it this filtered array as `rows`.
  const filtered = useMemo(() => {
    const trimmed = filter.trim().toLowerCase();
    return trimmed ? rows.filter(r => r.name.toLowerCase().includes(trimmed)) : rows;
  }, [rows, filter]);

  // Same weighted-aggregate scheme as the services page totals row:
  // sum spans/errs, weight avg/apdex by span count, take max for p99.
  const agg = useMemo(() => {
    if (rows.length === 0) return null;
    let totalSpans = 0, totalErrs = 0, wAvg = 0, wApdex = 0, maxP99 = 0;
    for (const r of rows) {
      totalSpans += r.spanCount;
      totalErrs += r.errorCount;
      wAvg += r.avgDurationMs * r.spanCount;
      wApdex += (r.apdex ?? 0) * r.spanCount;
      if (r.p99DurationMs > maxP99) maxP99 = r.p99DurationMs;
    }
    return {
      spans: totalSpans, errs: totalErrs,
      errorRate: totalSpans > 0 ? (totalErrs / totalSpans) * 100 : 0,
      avgMs: totalSpans > 0 ? wAvg / totalSpans : 0,
      p99Ms: maxP99,
      apdex: totalSpans > 0 ? wApdex / totalSpans : 0,
    };
  }, [rows]);

  // Element-wise sum of every operation's sparkline → service-wide
  // call-rate trend rendered on the "All" aggregate row. Uses the
  // longest sparkline as the canvas length; shorter ones (only one
  // bucket of activity, e.g. a single-call operation) just contribute
  // zeros to the trailing slots.
  const aggSparkline = useMemo(() => {
    let len = 0;
    for (const r of rows) {
      if (r.sparkline && r.sparkline.length > len) len = r.sparkline.length;
    }
    if (len === 0) return [];
    const out = new Array(len).fill(0);
    for (const r of rows) {
      if (!r.sparkline) continue;
      for (let i = 0; i < r.sparkline.length; i++) {
        out[i] += r.sparkline[i];
      }
    }
    return out;
  }, [rows]);

  // Shared sortable + resizable table primitive (v0.7.54). Feed the
  // FILTERED rows so sorting acts on what's visible; default sort
  // preserved as impact desc. Hook is unconditional + above the
  // empty-state early return (rules-of-hooks).
  // v0.7.x — app-wide keyboard nav (UX#4). onOpen drills the selected
  // operation into /traces (service + name pre-filtered, same as the row
  // Link's opHref); searchRef binds "/" to focus the filter input; j/k move
  // the row selection and Enter/o open. dt.rowProps(i) on each <tr> paints
  // the .row-selected accent + the data-row-idx the auto-scroll needs.
  // v0.9.61 — Latency percentile seçimi URL'de (?lat=, default p95 —
  // onaylı mockup). Kolon sortValue'su seçime bağlı olduğundan kolon
  // seti lat'a memo'lu; id'ler sabit kaldığından persisted sort/resize
  // seçim değişiminde bozulmaz.
  const [sp, setSp] = useSearchParams();
  const latRaw = sp.get('lat') ?? 'p95';
  const lat: LatKey = (LAT_KEYS as string[]).includes(latRaw) ? latRaw as LatKey : 'p95';
  const setLat = (k: LatKey) => setSp(prev => {
    const next = new URLSearchParams(prev);
    if (k === 'p95') next.delete('lat'); else next.set('lat', k);
    return next;
  }, { replace: true });
  // rps payda penceresi — timeRangeToNs YALNIZ memo içinde (v0.5.184).
  const winSec = useMemo(() => {
    const { from, to } = timeRangeToNs(range);
    return Math.max(1, (to - from) / 1e9);
  }, [range]);
  const opCols = useMemo<DataTableColumn<OperationSummary>[]>(() => [
    { id: 'name',       label: 'Operation',  sortValue: r => r.name,        naturalDir: 'asc', width: 300 },
    { id: 'latency',    label: 'Latency',    sortValue: r => latOf(r, lat), numeric: true,     width: 200 },
    { id: 'throughput', label: 'Throughput', sortValue: r => r.spanCount,   numeric: true,     width: 180 },
    { id: 'errorRate',  label: 'Error rate', sortValue: r => r.errorRate,   numeric: true,     width: 180 },
    { id: 'impact',     label: 'Impact',     sortValue: r => impactOf(r),   numeric: true,     width: 130 },
  ], [lat]);

  const dt = useDataTable<OperationSummary>({
    // v2: kolon kimlikleri değişti (Elastic-parity) — eski persisted
    // sort id'leri (spanCount/avg/p50…) yeni sette geçersiz.
    storageKey: 'service-operations-v2',
    columns: opCols,
    rows: filtered,
    initialSort: { id: 'impact', dir: 'desc' },
    onOpen: (op) => navigate(opHref(op.name)),
    searchRef,
  });

  // group_id rel C — the Raw ⇄ Normalized toggle + helper caption.
  // Rendered above EVERY state (loading / empty / populated) so the
  // operator can always flip back to raw — never trap them in a
  // normalized-empty view with no escape. Reuses the shared <Button>
  // atom (the v0.7.54 one-design-language rule); no hand-rolled button
  // styles. Viewer SEES the toggle — read-only data, no gating.
  const modeToggle = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginLeft: 'auto' }}>
      {/* v0.9.61 — Elastic-parity compare: bir-önceki eş-pencere gölge
          serileri + delta çipleri (Endpoints ?compare=prior deseni). */}
      <label style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11.5, color: 'var(--text2)', cursor: 'pointer' }}
        title="Overlay the previous period of the same length — ghost sparklines + change chips">
        <input type="checkbox" checked={compare} onChange={e => onToggleCompare(e.target.checked)} />
        Compare: previous period
      </label>
      <span style={{ display: 'inline-flex', gap: 4 }}>
        <Button variant={normalized ? 'ghost' : 'secondary'} size="sm"
          onClick={() => onToggleNormalized(false)}
          title="Show operations by raw span name">Raw</Button>
        <Button variant={normalized ? 'secondary' : 'ghost'} size="sm"
          onClick={() => onToggleNormalized(true)}
          title="Collapse id-bearing operations into shapes (GET /users/:id)">Normalized</Button>
      </span>
    </div>
  );

  // Loading covers the normalized refetch (raw arrives in the bundle).
  if (loading) {
    return (
      <div style={{ marginTop: 18 }}>
        <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
          <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
          {modeToggle}
        </div>
        <Spinner />
      </div>
    );
  }

  if (rows.length === 0) {
    // group_id rel C — normalized-empty is a DIFFERENT story than
    // raw-empty: it's not "no traffic", it's "no op_group shapes in
    // this window yet" (forward-only — grouping starts with newly-
    // ingested spans, so old windows legitimately have none). Honest
    // <Empty> message + the toggle stays visible so the operator flips
    // back to raw without losing the page.
    if (normalized) {
      return (
        <div style={{ marginTop: 18 }}>
          <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
            <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
            {modeToggle}
          </div>
          <Empty icon="∅" title="No normalized shapes in this window">
            Normalized grouping starts with newly-ingested spans — no shapes in this window yet.
          </Empty>
        </div>
      );
    }
    // v0.5.292 — short-window (≤30 min) default hits "no
    // traffic" often enough that operators reported the page
    // as broken. Surface a one-click widen-to-1h instead of
    // the bare-empty message. Wider windows keep the plain
    // empty state since "no ops in 24h" is genuinely "no
    // ops".
    const isShortWindow = preset
      && ['5m', '10m', '15m', '30m'].includes(preset);
    return (
      <div style={{ marginTop: 18 }}>
        <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
          <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
          {modeToggle}
        </div>
        <div className="empty" style={{ padding: 30 }}>
          {isShortWindow ? (
            <>
              <div style={{ marginBottom: 12 }}>
                No traffic for <b>{service}</b> in the last <b>{preset}</b>.
                Idle or low-traffic services often don't produce
                spans in a short window.
              </div>
              {onWiden && (
                <Button onClick={onWiden}>
                  Widen to last 1h
                </Button>
              )}
            </>
          ) : (
            <>No operations seen in this window</>
          )}
        </div>
      </div>
    );
  }

  return (
    <div style={{ marginTop: 18 }}>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
        {modeToggle}
      </div>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {normalized && <b style={{ color: 'var(--text2)' }}>normalized · </b>}
          {filter.trim()
            ? `${dt.sortedRows.length} / ${rows.length} matching`
            : `${rows.length} ${normalized ? 'operation shape' : 'distinct span name'}${rows.length === 1 ? '' : 's'} in ${service}`}
        </span>
        <input ref={searchRef} className="field" value={filter} onChange={e => setFilter(e.target.value)}
          placeholder="Filter by name…  ( / to focus, j/k to move, Enter to open )"
          style={{ marginLeft: 'auto', width: 320 }} />
      </div>
      {/* v0.5.462 — operator-reported: the previous maxHeight:540
          inner-scroll wrapper made even a 50-op service feel
          claustrophobic. Virtualization via content-visibility:auto
          on each row (set below) handles the 500+ op perf case
          per CLAUDE.md's "tables > 100 rows" guidance, so the
          inner scroll isn't earning its keep.
          v0.7.54 — header is now the shared <DataTableHead> (sortable
          + per-column resize); fixed layout via the <colgroup> +
          tableLayout:fixed. Body cell order tracks OP_COLS:
          Operation · Trend · Impact · Calls · Err% · Avg · P50 · P95 ·
          P99 · Apdex. */}
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          {/* v0.9.61 — Latency başlığında percentile seçici (mockup):
              tıklamalar sort'u tetiklememesi için stopPropagation. */}
          <DataTableHead dt={dt} renderLabel={c => c.id !== 'latency' ? c.label : (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              Latency
              <span onClick={e => e.stopPropagation()} style={{ display: 'inline-flex', gap: 2 }}>
                {LAT_KEYS.map(k => (
                  <button key={k} type="button"
                    onClick={() => setLat(k)}
                    title={`Show ${k} latency in this column`}
                    style={{
                      all: 'unset', cursor: 'pointer', padding: '0 4px', fontSize: 10,
                      color: lat === k ? 'var(--accent2)' : 'var(--text3)',
                      fontWeight: lat === k ? 700 : 400, textTransform: 'none',
                    }}>{k}</button>
                ))}
              </span>
            </span>
          )} />
          <tbody>
            {agg && (
              <tr className="agg-row">
                <td><span style={{ fontWeight: 700 }}>All ({rows.length})</span></td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  {lat === 'avg' ? fmtMsShort(agg.avgMs) : lat === 'p99' ? fmtMsShort(agg.p99Ms)
                    : <span style={{ color: 'var(--text3)' }} title="p50/p95 toplulaştırması yok — satırlara bak">—</span>}
                </td>
                <td>
                  {/* Servis-geneli çağrı trendi: satır sparkline'larının
                      element-wise toplamı (aynı pencere/bucket sınırları). */}
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8 }}>
                    <Sparkline values={aggSparkline} title={`total calls/bucket × ${rows.length} ops`} />
                    <b className="mono" style={{ fontSize: 12 }}>{fmtRps(agg.spans / winSec)}</b>
                  </div>
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  <span className={`badge b-${agg.errorRate > 5 ? 'err' : agg.errorRate > 0 ? 'warn' : 'ok'}`}>
                    {agg.errorRate.toFixed(2)}%
                  </span>
                </td>
                <td className="mono" style={{ textAlign: 'right', fontWeight: 700 }}>
                  {fmtImpact(rows.reduce((n, r) => n + impactOf(r), 0))}
                </td>
              </tr>
            )}
            {dt.sortedRows.map((op, i) => {
              const errCls = op.errorRate > 5 ? 'err' : op.errorRate > 0 ? 'warn' : 'ok';
              const rp = dt.rowProps(i);
              const maxImpact = Math.max(...rows.map(impactOf));
              const priorImpact = op.hasPrior ? (op.priorAvgDurationMs ?? 0) * (op.priorSpanCount ?? 0) : 0;
              return (
                <tr key={op.name} {...rp}
                    style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                  <td>
                    <Link
                      to={opHref(op.name)}
                      style={{ fontWeight: 500 }}
                      title="Open this operation in Traces — service + name pre-filtered"
                    >{op.name}</Link>
                    {/* v0.8.422 — raw rows only: Normalized modda op.name
                        şablondur, ?op= kapsaması boş panele düşer. */}
                    {!normalized && (
                      <Link
                        to={detailsHref(op.name)}
                        title="Operation-scoped charts — RPS / error rate / p50–p99 duration band + latency heatmap for just this operation"
                        aria-label={`Open scoped charts for ${op.name}`}
                        style={{
                          marginLeft: 8, color: 'var(--accent2)',
                          textDecoration: 'none', verticalAlign: 'middle',
                          display: 'inline-flex',
                        }}>
                        <svg width="13" height="13" viewBox="0 0 24 24" fill="none"
                             stroke="currentColor" strokeWidth="2" strokeLinecap="round"
                             aria-hidden="true">
                          <path d="M3 3v18h18" />
                          <path d="M7 14l4-6 4 3 5-8" />
                        </svg>
                      </Link>
                    )}
                  </td>
                  <td>
                    <MetricCell values={latSparkOf(op, lat)}
                      color="var(--purple)"
                      text={fmtMsShort(latOf(op, lat))}
                      compare={compare} hasPrior={op.hasPrior}
                      cur={latOf(op, lat)} prior={priorLatOf(op, lat)} kind="lowerBetter" />
                  </td>
                  <td>
                    {/* Throughput sparkline'ı tıklanınca detay modalı
                        (calls/errors/p99) — eski Trend kolonunun mirası. */}
                    <button type="button" onClick={() => setOpDetail(op)}
                      title={`${fmtNum(op.spanCount)} calls — click for calls / errors / p99 detail`}
                      style={{ background: 'transparent', border: 0, padding: 0, cursor: 'pointer', display: 'block', width: '100%' }}>
                      <MetricCell values={op.sparkline}
                        priorValues={op.priorSparkline}
                        color="var(--accent2)"
                        text={fmtRps(op.spanCount / winSec)}
                        compare={compare} hasPrior={op.hasPrior}
                        cur={op.spanCount} prior={op.priorSpanCount ?? 0} kind="neutral" />
                    </button>
                  </td>
                  <td>
                    <MetricCell values={op.errorsSparkline}
                      priorValues={op.priorErrorsSparkline}
                      color={errCls === 'ok' ? 'var(--text3)' : `var(--${errCls})`}
                      textColor={errCls === 'ok' ? undefined : `var(--${errCls})`}
                      text={`${op.errorRate.toFixed(2)}%`}
                      compare={compare} hasPrior={op.hasPrior}
                      cur={op.errorRate} prior={op.priorErrorRate ?? 0} kind="lowerBetter" />
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <ImpactBar value={impactOf(op)} max={maxImpact} />
                    {compare && op.hasPrior && (
                      <div style={{
                        height: 3, borderRadius: 2, background: 'var(--accent2)', opacity: .35,
                        width: `${maxImpact > 0 ? Math.min(100, priorImpact / maxImpact * 100) : 0}%`,
                        marginTop: 2, marginLeft: 'auto',
                      }} title="previous period impact (same scale)" />
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <OperationMetricModal
        service={service}
        op={opDetail}
        onClose={() => setOpDetail(null)}
        range={range}
      />
    </div>
  );
}

// OperationMetricModal — opens on per-op sparkline click. Same
// three-RED-dimensions pattern as the Endpoints modal: calls,
// errors, p99 latency, drawn as full uPlot charts with a synced
// crosshair so the operator correlates spikes across all three
// at one instant. v0.5.392 — applies the metric drill-in pattern
// to /service per-operation rows so the operator gets the same
// reading affordance everywhere they see a sparkline.
function OperationMetricModal({
  service, op, onClose, range,
}: {
  service: string;
  op: OperationSummary | null;
  onClose: () => void;
  range: TimeRange;
}) {
  const series = useMemo(() => {
    if (!op) return { calls: [] as SpanMetricSeries[], errors: [] as SpanMetricSeries[], p99: [] as SpanMetricSeries[] };
    const { from, to } = timeRangeToNs(range);
    const calls = op.sparkline ?? [];
    const errs = op.errorsSparkline ?? [];
    const p99s = op.p99Sparkline ?? [];
    const n = Math.max(calls.length, errs.length, p99s.length);
    if (n === 0 || to <= from) {
      return { calls: [], errors: [], p99: [] };
    }
    const bucketNs = (to - from) / n;
    const t = (i: number) => from + bucketNs * i + bucketNs / 2;
    const pts = (arr: number[]) => arr.map((v, i) => ({ time: t(i), value: v }));
    return {
      calls: calls.length ? [{ groupKey: ['calls'], points: pts(calls) }] : [],
      errors: errs.length ? [{ groupKey: ['errors'], points: pts(errs) }] : [],
      p99: p99s.length ? [{ groupKey: ['p99 ms'], points: pts(p99s) }] : [],
    };
  }, [op, range]);

  if (!op) return <Modal open={false} onClose={onClose} />;
  const peakCalls = (op.sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const totalErrs = (op.errorsSparkline ?? []).reduce((s, v) => s + v, 0);
  const maxP99 = (op.p99Sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const errCls = op.errorRate >= 5 ? 'err' : op.errorRate >= 1 ? 'warn' : '';
  // v0.8.488 — operatör-reported: drill link serbest-metin ?search=
  // taşıyordu; arama trace'in HERHANGİ bir yerinde eşleşir ve satır
  // KÖK operasyonu gösterdiğinden listeye başka POST'lar da düşüyordu.
  // Artık KESİN isim filtresi (name = "<op>") gider — yalnız bu
  // operasyonu içeren trace'ler.
  const tracesHref =
    `/traces?service=${encodeURIComponent(service)}` +
    `&filters=${encodeURIComponent(encodeFilters([{ k: 'name', op: '=', v: [op.name] }]))}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    `&view=list&rootOnly=false`;

  // v0.8.x — drill from this popup to /explore, operation-scoped.
  // Mirrors the v0.6.55 /services sparkline→/explore pattern
  // (Services.tsx goToExplore) but carries TWO filters: the service
  // pin AND the span name, so the operator lands on the exact metric
  // chart for THIS (service, operation) tuple with the clicked
  // aggregation preselected (Calls→rate, Errors→error_rate, P99→p99).
  // We reuse the SAME legacy ?result=metric URL shape that
  // urlCodec.seedFromLegacyParams decodes — extractScope lifts
  // service.name into the builder scope, `name=` stays a chip
  // (model.pinnedOperation reads it). No new URL scheme invented.
  const exploreHref = (agg: SpanAgg) => {
    const filters = encodeFilters([
      { k: 'service.name', op: '=', v: [service] },
      { k: 'name', op: '=', v: [op.name] },
    ]);
    return `/explore?${buildQuery([
      ['range', encodeRange(range)],
      ['filters', filters],
      ['agg', agg],
      ['field', 'duration_ms'],
      ['result', 'metric'],
    ])}`;
  };
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span className="mono" style={{ fontSize: 13 }}>
          {op.name}
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            ({service})
          </span>
          {/* v0.9.61 — Apdex tablodan çıktı (5-kolon sadeleşmesi),
              operasyon detayında yaşıyor. */}
          {isFinite(op.apdex) && (
            <span title="Apdex — user-satisfaction score (T=200ms)"
              style={{ marginLeft: 10, fontSize: 11, fontWeight: 700, color: apdexColor(op.apdex) }}>
              apdex {op.apdex.toFixed(2)}
            </span>
          )}
        </span>
      }
    >
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 12, marginBottom: 14,
      }}>
        <OpMetricTile label="Calls" big={fmtNum(op.spanCount)}
          sub={`peak ${fmtNum(peakCalls)} / bucket`}
          series={series.calls} />
        <OpMetricTile label="Errors" big={fmtNum(op.errorCount)}
          sub={`${op.errorRate.toFixed(2)}% rate`}
          subCls={errCls}
          series={series.errors} />
        <OpMetricTile label="P99 latency"
          big={`${op.p99DurationMs.toFixed(0)} ms`}
          sub={`peak ${maxP99.toFixed(0)} ms · avg ${op.avgDurationMs.toFixed(0)} ms`}
          series={series.p99} unit="ms" />
      </div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
        Hover any chart to read the bucket value; crosshair syncs
        across all three so you can correlate calls / errors /
        p99 at the same instant. Total errors in window:
        {' '}<strong>{fmtNum(totalErrs)}</strong>.
      </div>
      <div style={{ display: 'flex', gap: 14, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <Link to={tracesHref} style={{ fontSize: 12, color: 'var(--accent2)' }}>
          View traces →
        </Link>
        {/* v0.8.x — open this (service, operation) tuple in Explore,
            carrying the clicked metric's aggregation so the operator
            lands on the exact chart with the full builder toolbar.
            Mirrors the /services sparkline→/explore drill (v0.6.55),
            operation-scoped. */}
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>Explore:</span>
        <Link to={exploreHref('rate')} style={{ fontSize: 12, color: 'var(--accent2)' }}
          title={`Open call rate for ${op.name} in Explore (service + operation scoped)`}>
          Calls →
        </Link>
        <Link to={exploreHref('error_rate')} style={{ fontSize: 12, color: 'var(--accent2)' }}
          title={`Open error rate for ${op.name} in Explore (service + operation scoped)`}>
          Errors →
        </Link>
        <Link to={exploreHref('p99')} style={{ fontSize: 12, color: 'var(--accent2)' }}
          title={`Open p99 latency for ${op.name} in Explore (service + operation scoped)`}>
          P99 →
        </Link>
      </div>
    </Modal>
  );
}

function OpMetricTile({
  label, big, sub, subCls, series, unit,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
}) {
  return (
    <div style={{
      padding: '10px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 600, marginBottom: 2 }}>{big}</div>
      <div style={{
        fontSize: 11, marginBottom: 8,
        color: subCls === 'err' ? 'var(--err)' : subCls === 'warn' ? 'var(--warn)' : 'var(--text3)',
      }}>{sub}</div>
      {series.length > 0 && series[0].points.length > 0 ? (
        <MultiLineChart series={series} unit={unit} height={140} syncKey="op-detail" />
      ) : (
        <div style={{
          height: 140, display: 'flex', alignItems: 'center',
          justifyContent: 'center', color: 'var(--text3)', fontSize: 11,
        }}>no data in window</div>
      )}
    </div>
  );
}

// ImpactBar renders a horizontal proportion bar + numeric label —
// Elastic APM's signature pattern in the transaction list. Bar
// width = row impact / max impact across the visible rows so the
// busiest operation always fills the cell. Keeps the heaviest
// cumulative consumer visually obvious without forcing the
// operator to read tabular numbers.
// MetricCell — Elastic'in "ListMetric" hücresi (v0.9.61): sparkline +
// sağa dayalı değer + (compare açıkken) TrendDelta çipi. Prior seri
// varsa aynı kutuda %35 opaklıkta gölge olarak current'ın ALTINA
// çizilir (grid-overlay — Sparkline bileşenine dokunmadan).
function MetricCell({ values, priorValues, color, text, textColor, compare, hasPrior, cur, prior, kind }: {
  values?: number[];
  priorValues?: number[];
  color?: string;
  text: string;
  textColor?: string;
  compare: boolean;
  hasPrior?: boolean;
  cur: number;
  prior: number;
  kind: 'lowerBetter' | 'neutral';
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8 }}>
      <span style={{ display: 'grid', flexShrink: 0 }}>
        {compare && priorValues && priorValues.length > 0 && (
          <span style={{ gridArea: '1 / 1', opacity: .35 }}>
            <Sparkline values={priorValues} color={color} title="previous period" />
          </span>
        )}
        <span style={{ gridArea: '1 / 1' }}>
          <Sparkline values={values ?? []} color={color} title="" />
        </span>
      </span>
      <b className="mono" style={{ fontSize: 12, minWidth: 56, textAlign: 'right', color: textColor }}>{text}</b>
      {compare && hasPrior && <TrendDelta cur={cur} prior={prior} kind={kind} />}
    </div>
  );
}

function ImpactBar({ value, max }: { value: number; max: number }) {
  const pct = max > 0 ? (value / max) * 100 : 0;
  return (
    <div style={{ position: 'relative', minWidth: 90, display: 'inline-block' }}>
      <div style={{
        position: 'absolute', inset: 0, width: `${pct}%`,
        background: 'color-mix(in oklab, var(--accent) 12%, transparent)',
        borderRadius: 3,
      }} />
      <span style={{ position: 'relative', paddingRight: 4 }}>
        {fmtImpact(value)}
      </span>
    </div>
  );
}

// fmtImpact renders impact in time units. Below a second we keep
// ms with at most one decimal; past a second we promote to s/min/h
// so a 30M-ms ops job reads as "8.3h" rather than "30000000".
function fmtImpact(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)}ms`;
  if (ms < 1000) return `${ms.toFixed(1)}ms`;
  const sec = ms / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  const min = sec / 60;
  if (min < 60) return `${min.toFixed(1)}min`;
  const hr = min / 60;
  return `${hr.toFixed(1)}h`;
}
