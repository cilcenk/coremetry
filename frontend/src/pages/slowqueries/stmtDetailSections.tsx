import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { SectionUnavailable, StatTile } from '@/components/ui';
import { Sparkline } from '@/components/Sparkline';
import { TrendDelta } from '@/components/TrendDelta';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { fmtNum } from '@/lib/utils';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, DBStmtDetail, DBStmtCaller } from '@/lib/types';
import { densifyTrend } from './stmtParam';
import { serviceHref } from '@/lib/serviceHref';
import { repeatsExploreHref } from '@/lib/pivotHref';
import { traceHref } from '@/lib/traceHref';

// stmtDetailSections — the statement detail BODY (v0.9.1374).
//
// Extracted verbatim from StmtDetailDrawer, which is deleted in the same
// release. Same move, same reason as /database one page-conversion
// earlier (v0.9.840): a 620px drawer had to hold the statement text, a
// six-tile summary, three sparkline rows, a six-column caller table AND
// the exemplar pivots. Every one of those is a table or a chart, and a
// drawer is the one surface that cannot give any of them width.
//
// The sections themselves are unchanged — this is a MOVE, not a redesign.
// The only page-shaped parameter is `sparkWidth`: the drawer's 420px was
// a constant sized to its own shell, and a page that keeps it would look
// like a drawer pasted onto a page.

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 12, fontWeight: 700, color: 'var(--text2)',
      margin: '16px 0 8px',
    }}>{children}</div>
  );
}

/** Wall-clock total in the largest unit that keeps it readable. */
function fmtTotal(totalMs: number): string {
  const sec = totalMs / 1000;
  if (sec >= 60) return `${(sec / 60).toFixed(1)} min`;
  if (sec >= 1) return `${sec.toFixed(1)} s`;
  return `${totalMs.toFixed(0)} ms`;
}

/** Normalized statement text + the real literal-bearing sample. */
export function StmtText({ statement, sample }: { statement: string; sample: string }) {
  return (
    <>
      {statement ? (
        <pre style={{
          margin: '0 0 10px', fontSize: 12,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          whiteSpace: 'pre-wrap', wordBreak: 'break-word',
          color: 'var(--text)', maxHeight: 260, overflowY: 'auto',
          padding: 12, background: 'var(--bg1)',
          border: '1px solid var(--border)', borderRadius: 6,
        }}>{statement}</pre>
      ) : (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 10 }}>
          Statement text unavailable — no data for this class in the window.
        </div>
      )}
      {sample && (
        <details style={{ marginBottom: 14 }}>
          <summary style={{ fontSize: 10, color: 'var(--text3)', cursor: 'pointer',
            textTransform: 'uppercase', letterSpacing: 0.5 }}>
            Real sample (literals shown)
          </summary>
          <pre style={{
            margin: '6px 0 0', fontSize: 11,
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            whiteSpace: 'pre-wrap', wordBreak: 'break-word',
            color: 'var(--text2)', maxHeight: 200, overflowY: 'auto',
            padding: 8, background: 'var(--bg2)', borderRadius: 4,
          }}>{sample}</pre>
        </details>
      )}
    </>
  );
}

// StmtSummarySection — window totals; deltas ride the payload's prior*
// fields when compare is on (kind conventions match Endpoints: calls
// neutral, latency/errors lowerBetter).
export function StmtSummarySection({ detail, compare }: {
  detail: DBStmtDetail; compare: boolean;
}) {
  const s = detail.summary;
  if (!s) return <SectionUnavailable what="Window summary" />;
  const errTone = s.calls > 0 && s.errors / s.calls >= 0.05 ? 'err'
    : s.errors > 0 ? 'warn' : undefined;
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(100px, 1fr))',
      gap: 10,
    }}>
      <StatTile label="Calls">
        {fmtNum(s.calls)}
        {compare && <TrendDelta cur={s.calls} prior={s.priorCalls} kind="neutral" />}
      </StatTile>
      <StatTile label="Errors" tone={errTone}>
        {fmtNum(s.errors)}
        {compare && <TrendDelta cur={s.errors} prior={s.priorErrors} kind="lowerBetter" />}
      </StatTile>
      <StatTile label="Avg">
        {s.avgMs.toFixed(1)} ms
        {compare && <TrendDelta cur={s.avgMs} prior={s.priorAvgMs} kind="lowerBetter" />}
      </StatTile>
      <StatTile label="P95">
        {s.p95Ms.toFixed(0)} ms
        {compare && <TrendDelta cur={s.p95Ms} prior={s.priorP95Ms} kind="lowerBetter" />}
      </StatTile>
      <StatTile label="Max">
        {s.maxMs.toFixed(0)} ms
      </StatTile>
      <StatTile label="Total time">
        {fmtTotal(s.totalMs)}
      </StatTile>
    </div>
  );
}

// StmtTrendSection — three sparkline rows over the densified 5m-grain
// series (Sparkline primitive — the house no-chart-dep affordance for
// row-scale trends; uPlot buys crosshair/zoom this page doesn't need).
export function StmtTrendSection({ detail, sparkWidth = 420 }: {
  detail: DBStmtDetail; sparkWidth?: number;
}) {
  const dense = useMemo(
    () => densifyTrend(detail.trend ?? [], detail.fromNs, detail.toNs,
      detail.trendBucketSec ?? 0),
    [detail],
  );
  if (!detail.trend) return (
    <div>
      <SectionTitle>Trend</SectionTitle>
      <SectionUnavailable what="Trend" />
    </div>
  );
  const bucketMin = Math.round((detail.trendBucketSec ?? 0) / 60);
  const hasData = dense.calls.some(v => v > 0);
  return (
    <div>
      <SectionTitle>
        Trend
        {bucketMin > 0 && (
          <span style={{ fontWeight: 400, fontSize: 10, color: 'var(--text3)', marginLeft: 6 }}>
            {bucketMin}m buckets
          </span>
        )}
      </SectionTitle>
      {!hasData && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>No calls in window.</div>
      )}
      {hasData && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <TrendRow label="Calls" values={dense.calls} width={sparkWidth} />
          <TrendRow label="Errors" values={dense.errors} color="var(--err)" width={sparkWidth} />
          <TrendRow label="P95 ms" values={dense.p95Ms} color="var(--orange)" unit="ms" width={sparkWidth} />
        </div>
      )}
    </div>
  );
}

function TrendRow({ label, values, color, unit, width }: {
  label: string; values: number[]; color?: string; unit?: string; width: number;
}) {
  const max = values.reduce((m, v) => Math.max(m, v), 0);
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <span style={{ fontSize: 10, color: 'var(--text3)', width: 52, flexShrink: 0 }}>
        {label}
      </span>
      <Sparkline values={values} width={width} height={34}
        color={color} unit={unit}
        title={`${label} per bucket across the window`} />
      <span className="mono" style={{ fontSize: 10, color: 'var(--text3)', whiteSpace: 'nowrap' }}>
        max {unit === 'ms' ? max.toFixed(0) : fmtNum(max)}{unit ? ` ${unit}` : ''}
      </span>
    </div>
  );
}

const CALLER_COLS: DataTableColumn<DBStmtCaller>[] = [
  { id: 'service', label: 'Service',    sortValue: r => r.service, naturalDir: 'asc', width: 170 },
  { id: 'calls',   label: 'Calls',      sortValue: r => r.calls,   numeric: true, width: 76 },
  { id: 'errors',  label: 'Errors',     sortValue: r => r.errors,  numeric: true, width: 66 },
  { id: 'avgMs',   label: 'Avg',        sortValue: r => r.avgMs,   numeric: true, width: 70 },
  { id: 'p95Ms',   label: 'P95',        sortValue: r => r.p95Ms,   numeric: true, width: 70 },
  { id: 'totalMs', label: 'Total time', sortValue: r => r.totalMs, numeric: true, width: 90 },
];

// StmtCallersSection — which services issue this statement class
// (service_name is a real dimension in db_statement_summary_5m, so this
// is a pure MV read). Top 20 by total wall-clock time.
export function StmtCallersSection({ detail, compare, range }: {
  detail: DBStmtDetail; compare: boolean;
  // v0.9.967 — the page's window. Its whole point is "vs prior"; sending
  // the operator to a service page on a DIFFERENT window undoes that.
  range: TimeRange;
}) {
  const rows = detail.callers ?? [];
  const dt = useDataTable<DBStmtCaller>({
    storageKey: 'dbstmt-callers',
    columns: CALLER_COLS,
    rows,
    initialSort: { id: 'totalMs', dir: 'desc' },
  });
  return (
    <div>
      <SectionTitle>Callers</SectionTitle>
      {!detail.callers && <SectionUnavailable what="Caller breakdown" />}
      {detail.callers && rows.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No services issued this statement in the window.
        </div>
      )}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(c => (
                <tr key={c.service}>
                  <td>
                    <Link to={serviceHref(c.service, { range })}
                      className="mono" style={{ fontSize: 11 }}>
                      {c.service}
                    </Link>
                  </td>
                  <td className="num mono">
                    {fmtNum(c.calls)}
                    {compare && <TrendDelta cur={c.calls} prior={c.priorCalls} kind="neutral" />}
                  </td>
                  <td className="num mono" style={{
                    color: c.errors > 0 ? 'var(--err)' : 'var(--text3)',
                  }}>{fmtNum(c.errors)}</td>
                  <td className="num mono">
                    {c.avgMs.toFixed(1)}
                    {compare && <TrendDelta cur={c.avgMs} prior={c.priorAvgMs} kind="lowerBetter" />}
                  </td>
                  <td className="num mono">{c.p95Ms.toFixed(0)}</td>
                  <td className="num mono" style={{ fontWeight: 600 }}>{fmtTotal(c.totalMs)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// StmtExemplarsSection — the TRUE trace pivots: slowest + worst-error span
// of THIS statement class (spans.db_stmt_hash = hash), not a LIKE-prefix
// approximation.
//
// v0.9.1277 — üçüncü pivot: "N+1 trace'leri →". İlk ikisi TEK bir
// trace'e götürür ("bu ifadenin en yavaş örneği"); bu ise DESENE
// götürür ("bu ifadeyi tek istek içinde defalarca çağıran trace'ler").
// Yavaş bir sorgunun asıl hikâyesi çoğu zaman ikincisi: 4ms'lik bir
// SELECT, istek başına 200 kez çağrıldığında 800ms'dir.
//
// Kapsam KASITLI olarak servis + db.system: normalize ifade metni
// span'lerdeki ham `db.statement` ile eşleşmez, gerekçe
// repeatsExploreHref'in başında.
export function StmtExemplarsSection({ detail, range }: {
  detail: DBStmtDetail; range: TimeRange;
}) {
  const ex = detail.exemplars;
  // Bu ifadeyi GERÇEKTEN çağıran servisler — MV'nin kendi boyutu, tahmin
  // değil. Yoksa link servis filtresi olmadan gider (dürüst: daha geniş
  // bir soru sorar, YANLIŞ bir soru değil).
  const callerServices = (detail.callers ?? []).map(c => c.service).filter(Boolean);
  const repeatsHref = repeatsExploreHref({
    window: range,
    services: callerServices,
    dbSystem: detail.summary?.dbSystem || undefined,
  });
  return (
    <div>
      <SectionTitle>Exemplar traces</SectionTitle>
      {!ex && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No exemplar spans for this statement in the window.
        </div>
      )}
      <div style={{ display: 'flex', gap: 16, fontSize: 12, flexWrap: 'wrap',
        marginTop: ex ? 0 : 8 }}>
        {ex?.slowTraceId && (
          <Link to={traceHref(ex.slowTraceId, { pageRange: range })}
            style={{ color: 'var(--accent2)' }}
            title={`Slowest span of this statement class in the window (trace ${ex.slowTraceId})`}>
            slowest →
          </Link>
        )}
        {ex?.errorTraceId && (
          <Link to={traceHref(ex.errorTraceId, { pageRange: range })}
            style={{ color: 'var(--err)' }}
            title={`Slowest ERRORED span of this statement class in the window (trace ${ex.errorTraceId})`}>
            worst error →
          </Link>
        )}
        <Link to={repeatsHref}
          style={{ color: 'var(--warn)' }}
          title={callerServices.length
            ? `Explore → Repeats: aynı istek içinde ≥5 kez DB çağıran trace'ler`
              + ` (kapsam: ${callerServices.slice(0, 4).join(', ')}`
              + `${callerServices.length > 4 ? ` +${callerServices.length - 4}` : ''}`
              + `, gruplama db.statement, aynı zaman penceresi).`
              + ` İfade metni FİLTREYE konmaz — buradaki SQL normalize, span'lerdeki ham.`
            : `Explore → Repeats: aynı istek içinde ≥5 kez DB çağıran trace'ler`
              + ` (bu pencerede çağıran servis listelenemedi, kapsam tüm servisler).`}>
          N+1 trace'leri →
        </Link>
      </div>
    </div>
  );
}
