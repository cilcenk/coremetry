import { useEffect, useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { X } from 'lucide-react';
import { Spinner, Empty } from '@/components/Spinner';
import { Sparkline } from '@/components/Sparkline';
import { TrendDelta } from '@/components/TrendDelta';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { useDBStmtDetail } from '@/lib/queries';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, SlowQueryRow, DBStmtDetail, DBStmtCaller } from '@/lib/types';
import { densifyTrend, type StmtRef } from './stmtParam';

// StmtDetailDrawer — v0.8.378 (Stage-2 slice D2). Row click on
// /slow-queries opens this right-side drawer (shell mirrors the
// endpoints DetailDrawer / InboxTriageDrawer: overlay + slide-in, Esc
// closes — one drawer language). URL-first: the parent owns `?stmt=`
// (encodeStmtParam), the drawer owns `?stmtcmp=` for its compare
// toggle — a copied link reproduces the exact drill-down including the
// prior-window deltas. Body = ONE /api/databases/statements/detail
// payload keyed on the v0.8.375 stmt_hash, with per-section NULL
// tolerance — a failed section renders its own fallback line, never
// blanking the drawer:
//
//   • header — normalized statement (mono, wrapped) + the real bucket
//     sample with literals (collapsible)
//   • summary strip — calls/errors/avg/p95/max tiles; compare=prior
//     deltas via the shared TrendDelta (v0.8.364 single implementation)
//   • trend — calls / errors / p95 sparkline rows over the MV's
//     5m-grain series (Sparkline primitive — no chart dep; densified
//     client-side so gaps render as zeros)
//   • callers — per-service breakdown (service_name is an MV dim),
//     top 20 by total time
//   • exemplars — TRUE trace pivots (slowest + worst error) resolved
//     from spans.db_stmt_hash, replacing the lossy LIKE deep-link
export function StmtDetailDrawer({ refObj, row, range, onClose }: {
  refObj: StmtRef;
  // A matching catalog row when one is in the loaded page — statement
  // text fallback while the payload loads / when the summary section
  // misses. undefined on stale deep-links; soft, sections still load.
  row: SlowQueryRow | undefined;
  range: TimeRange;
  onClose: () => void;
}) {
  // Esc closes — same muscle memory as the endpoints/inbox drawers.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  // Compare toggle rides the URL (house rule §4 — the E4 v0.8.376
  // posture): ?stmtcmp=1, replace:true, foreign params preserved.
  const [params, setParams] = useSearchParams();
  const compare = params.get('stmtcmp') === '1';
  const setCompare = (v: boolean) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('stmtcmp', '1'); else next.delete('stmtcmp');
    return next;
  }, { replace: true });

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const detailQ = useDBStmtDetail({
    hash: refObj.hash,
    ...(refObj.system ? { system: refObj.system } : {}),
    from, to,
    ...(compare ? { compare: 'prior' as const } : {}),
  });
  const detail: DBStmtDetail | null | undefined =
    detailQ.isPending ? undefined : detailQ.isError ? null : detailQ.data;

  const statement = detail?.statement || row?.statement || '';
  const sample = detail?.summary?.sampleStatement || row?.sampleStatement || '';
  const dbSystem = detail?.summary?.dbSystem || row?.dbSystem || '';

  return (
    <>
      <div onClick={onClose}
        style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
          zIndex: 30, animation: 'fadeIn 120ms ease-out',
        }} />
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: 'min(620px, 100vw)',
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
        zIndex: 31, overflowY: 'auto',
        animation: 'slideInRight 180ms ease-out',
      }}>
        {/* Header — statement identity + compare toggle + close. */}
        <div style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <span className="badge b-gray mono" style={{ fontSize: 10 }}>{dbSystem || 'db'}</span>
          {detail?.summary?.dbName && detail.summary.dbName !== 'default' && (
            <span className="badge b-info mono" style={{ fontSize: 10 }}
              title="db.name this statement class runs against">
              {detail.summary.dbName}
            </span>
          )}
          <span style={{ fontSize: 13, fontWeight: 600 }}>Statement detail</span>
          <span className="mono" style={{ fontSize: 10, color: 'var(--text3)' }}
            title={`Persistent statement identity (stmt_hash ${refObj.hash})`}>
            #{refObj.hash.slice(0, 8)}
          </span>
          <label style={{
            fontSize: 11, display: 'flex', alignItems: 'center', gap: 4,
            cursor: 'pointer', marginLeft: 'auto', whiteSpace: 'nowrap',
          }}
            title="Compare current window against the immediately-preceding equal-length window. Adds a second backend read; off by default.">
            <input type="checkbox" checked={compare}
              onChange={e => setCompare(e.target.checked)} />
            vs prior
          </label>
          <button type="button" onClick={onClose} title="Close (Esc)"
            style={{
              all: 'unset', cursor: 'pointer', color: 'var(--text3)',
              display: 'inline-flex', padding: 4,
            }}>
            <X size={15} strokeWidth={1.75} />
          </button>
        </div>

        <div style={{ padding: '14px 18px' }}>
          {/* Normalized statement — the class identity, wrapped mono. */}
          {statement ? (
            <pre style={{
              margin: '0 0 10px', fontSize: 12,
              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              color: 'var(--text)', maxHeight: 180, overflowY: 'auto',
              padding: 10, background: 'var(--bg1)',
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
                color: 'var(--text2)', maxHeight: 160, overflowY: 'auto',
                padding: 8, background: 'var(--bg2)', borderRadius: 4,
              }}>{sample}</pre>
            </details>
          )}

          {detail === undefined && <Spinner />}
          {detail === null && (
            <Empty icon="⚠" title="Detail query failed">
              The backend /api/databases/statements/detail request errored —
              the catalog row is still live.
            </Empty>
          )}
          {detail && (
            <>
              <SummarySection detail={detail} compare={compare} />
              <TrendSection detail={detail} />
              <CallersSection detail={detail} compare={compare} />
              <ExemplarsSection detail={detail} />
            </>
          )}
        </div>
      </div>
    </>
  );
}

function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 12, fontWeight: 700, color: 'var(--text2)',
      margin: '16px 0 8px',
    }}>{children}</div>
  );
}

function SectionUnavailable({ what }: { what: string }) {
  return (
    <div style={{ fontSize: 11, color: 'var(--text3)' }}>
      {what} unavailable for this window.
    </div>
  );
}

function HeaderStat({ label, tone, children }: {
  label: string; tone?: 'err' | 'warn'; children: React.ReactNode;
}) {
  return (
    <div style={{
      padding: '8px 10px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', marginBottom: 2 }}>{label}</div>
      <div className="mono" style={{
        fontSize: 15, fontWeight: 600,
        color: tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{children}</div>
    </div>
  );
}

// SummarySection — window totals; deltas ride the payload's prior*
// fields when compare is on (kind conventions match Endpoints: calls
// neutral, latency/errors lowerBetter).
function SummarySection({ detail, compare }: { detail: DBStmtDetail; compare: boolean }) {
  const s = detail.summary;
  if (!s) return <SectionUnavailable what="Window summary" />;
  const errTone = s.calls > 0 && s.errors / s.calls >= 0.05 ? 'err'
    : s.errors > 0 ? 'warn' : undefined;
  const totalSec = s.totalMs / 1000;
  const totalLabel = totalSec >= 60 ? `${(totalSec / 60).toFixed(1)} min`
    : totalSec >= 1 ? `${totalSec.toFixed(1)} s`
    : `${s.totalMs.toFixed(0)} ms`;
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(100px, 1fr))',
      gap: 10,
    }}>
      <HeaderStat label="Calls">
        {fmtNum(s.calls)}
        {compare && <TrendDelta cur={s.calls} prior={s.priorCalls} kind="neutral" />}
      </HeaderStat>
      <HeaderStat label="Errors" tone={errTone}>
        {fmtNum(s.errors)}
        {compare && <TrendDelta cur={s.errors} prior={s.priorErrors} kind="lowerBetter" />}
      </HeaderStat>
      <HeaderStat label="Avg">
        {s.avgMs.toFixed(1)} ms
        {compare && <TrendDelta cur={s.avgMs} prior={s.priorAvgMs} kind="lowerBetter" />}
      </HeaderStat>
      <HeaderStat label="P95">
        {s.p95Ms.toFixed(0)} ms
        {compare && <TrendDelta cur={s.p95Ms} prior={s.priorP95Ms} kind="lowerBetter" />}
      </HeaderStat>
      <HeaderStat label="Max">
        {s.maxMs.toFixed(0)} ms
      </HeaderStat>
      <HeaderStat label="Total time">
        {totalLabel}
      </HeaderStat>
    </div>
  );
}

// TrendSection — three sparkline rows over the densified 5m-grain
// series (Sparkline primitive — the house no-chart-dep affordance for
// row-scale trends; uPlot buys crosshair/zoom this drawer doesn't need).
function TrendSection({ detail }: { detail: DBStmtDetail }) {
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
          <TrendRow label="Calls" values={dense.calls} />
          <TrendRow label="Errors" values={dense.errors} color="var(--err)" />
          <TrendRow label="P95 ms" values={dense.p95Ms} color="var(--orange)" unit="ms" />
        </div>
      )}
    </div>
  );
}

function TrendRow({ label, values, color, unit }: {
  label: string; values: number[]; color?: string; unit?: string;
}) {
  const max = values.reduce((m, v) => Math.max(m, v), 0);
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <span style={{ fontSize: 10, color: 'var(--text3)', width: 52, flexShrink: 0 }}>
        {label}
      </span>
      <Sparkline values={values} width={420} height={34}
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

// CallersSection — which services issue this statement class
// (service_name is a real dimension in db_statement_summary_5m, so this
// is a pure MV read). Top 20 by total wall-clock time.
function CallersSection({ detail, compare }: { detail: DBStmtDetail; compare: boolean }) {
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
              {dt.sortedRows.map(c => {
                const totalSec = c.totalMs / 1000;
                const totalLabel = totalSec >= 60 ? `${(totalSec / 60).toFixed(1)} min`
                  : totalSec >= 1 ? `${totalSec.toFixed(1)} s`
                  : `${c.totalMs.toFixed(0)} ms`;
                return (
                  <tr key={c.service}>
                    <td>
                      <Link to={`/service?name=${encodeURIComponent(c.service)}`}
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
                    <td className="num mono" style={{ fontWeight: 600 }}>{totalLabel}</td>
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

// ExemplarsSection — the TRUE trace pivots: slowest + worst-error span
// of THIS statement class (spans.db_stmt_hash = hash), not a LIKE-prefix
// approximation.
function ExemplarsSection({ detail }: { detail: DBStmtDetail }) {
  const ex = detail.exemplars;
  return (
    <div>
      <SectionTitle>Exemplar traces</SectionTitle>
      {!ex && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No exemplar spans for this statement in the window.
        </div>
      )}
      {ex && (
        <div style={{ display: 'flex', gap: 16, fontSize: 12, flexWrap: 'wrap' }}>
          {ex.slowTraceId && (
            <Link to={`/trace?id=${encodeURIComponent(ex.slowTraceId)}`}
              style={{ color: 'var(--accent2)' }}
              title={`Slowest span of this statement class in the window (trace ${ex.slowTraceId})`}>
              slowest →
            </Link>
          )}
          {ex.errorTraceId && (
            <Link to={`/trace?id=${encodeURIComponent(ex.errorTraceId)}`}
              style={{ color: 'var(--err)' }}
              title={`Slowest ERRORED span of this statement class in the window (trace ${ex.errorTraceId})`}>
              worst error →
            </Link>
          )}
        </div>
      )}
    </div>
  );
}
