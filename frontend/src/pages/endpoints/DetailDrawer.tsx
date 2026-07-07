import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { X } from 'lucide-react';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { useEndpointDetail, useEndpointSplit } from '@/lib/queries';
import { fmtNum, timeRangeToNs, tsLong } from '@/lib/utils';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, EndpointRow, EndpointDetail, EndpointSplitValue } from '@/lib/types';
import { TrendDelta } from '@/components/TrendDelta';
import { trimHistogram, type EndpointRef } from './endpointParam';

// EndpointDetailDrawer — v0.8.360 (Stage-2 slice E2). Row click on
// /endpoints opens this right-side drawer (shell mirrors
// InboxTriageDrawer: overlay + slide-in panel, Esc closes — one drawer
// language). URL-first: the parent owns the `?endpoint=` param
// (encodeEndpointParam), so a copied link reproduces the exact
// drill-down. Body = ONE /api/endpoints/detail payload with
// per-section NULL tolerance — a failed section renders its own
// fallback line, never blanking the drawer:
//
//   • header RED strip (from the table row when present) + compare
//     deltas via the shared TrendDelta
//   • latency distribution — SVG bars over the heatmap's log-scale
//     duration bins (LogsHistogram precedent: plain SVG/divs, no
//     chart dep). Chosen over the 2-D LatencyHeatmap component: the
//     drawer is 560px wide and the question here is "what is THIS
//     endpoint's latency SHAPE" (bimodality, tail) — the time
//     dimension is already covered by the row sparkline/modal.
//   • error breakdown — status-class pills + per-code chips + top
//     exceptions (each → /problems?exception= deep link)
//   • top failing traces → /trace?id= pivots + slow/error exemplars
//   • split-by — whitelisted attribute select + top-10 RED table
export function EndpointDetailDrawer({ refObj, row, range, compare, onClose }: {
  refObj: EndpointRef;
  // The matching table row when it's in the loaded page — drives the
  // header RED strip + prior-window deltas. undefined on stale
  // deep-links (row filtered out / other page): soft fallback, the
  // sections below still load from the detail payload.
  row: EndpointRow | undefined;
  range: TimeRange;
  compare: boolean;
  onClose: () => void;
}) {
  // Esc closes — same muscle memory as the inbox / anomaly drawers.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const detailQ = useEndpointDetail({
    service: refObj.service, path: refObj.path, from, to,
    ...(refObj.sig ? { sig: '1' as const } : {}),
  });
  const detail: EndpointDetail | null | undefined =
    detailQ.isPending ? undefined : detailQ.isError ? null : detailQ.data;

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
        {/* Header — endpoint identity + close. */}
        <div style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          {row?.method && (
            <span className="badge b-gray" style={{ fontSize: 10 }}>{row.method}</span>
          )}
          {refObj.sig && (
            <span className="badge b-info" style={{ fontSize: 10 }}
              title="Grouped by shape — IDs in the path are collapsed to :id; the sections below aggregate every matching raw route.">
              shape
            </span>
          )}
          <span className="mono" style={{
            fontSize: 13, fontWeight: 600, overflow: 'hidden',
            textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }} title={refObj.path}>
            {refObj.path}
          </span>
          <Link to={`/service?name=${encodeURIComponent(refObj.service)}`}
            className="mono" style={{ fontSize: 11, color: 'var(--accent2)', marginLeft: 'auto', whiteSpace: 'nowrap' }}>
            {refObj.service}
          </Link>
          <button type="button" onClick={onClose} title="Close (Esc)"
            style={{
              all: 'unset', cursor: 'pointer', color: 'var(--text3)',
              display: 'inline-flex', padding: 4,
            }}>
            <X size={15} strokeWidth={1.75} />
          </button>
        </div>

        <div style={{ padding: '14px 18px' }}>
          {/* RED strip — repeats the row so the drawer reads on its
              own in a postmortem screenshot; deltas ride the row's
              prior* fields when compare is on. */}
          {row ? (
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(100px, 1fr))',
              gap: 10, marginBottom: 16,
            }}>
              <HeaderStat label="Calls">
                {fmtNum(row.calls)}
                {compare && <TrendDelta cur={row.calls} prior={row.priorCalls} kind="neutral" />}
              </HeaderStat>
              <HeaderStat label="Errors" tone={row.errorRate >= 5 ? 'err' : row.errorRate >= 1 ? 'warn' : undefined}>
                {fmtNum(row.errors)}
                {compare && <TrendDelta cur={row.errors} prior={row.priorErrors} kind="lowerBetter" />}
              </HeaderStat>
              <HeaderStat label="Err rate" tone={row.errorRate >= 5 ? 'err' : row.errorRate >= 1 ? 'warn' : undefined}>
                {row.errorRate.toFixed(2)}%
              </HeaderStat>
              <HeaderStat label="Avg">
                {row.avgMs.toFixed(1)} ms
                {compare && <TrendDelta cur={row.avgMs} prior={row.priorAvgMs} kind="lowerBetter" />}
              </HeaderStat>
              <HeaderStat label="P99">
                {row.p99Ms.toFixed(0)} ms
                {compare && <TrendDelta cur={row.p99Ms} prior={row.priorP99Ms} kind="lowerBetter" />}
              </HeaderStat>
            </div>
          ) : (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
              Endpoint not in the current table page — showing the window drill-down below.
            </div>
          )}

          {detail === undefined && <Spinner />}
          {detail === null && (
            <Empty icon="⚠" title="Detail query failed">
              The backend /api/endpoints/detail request errored — the
              table row above is still live.
            </Empty>
          )}
          {detail && (
            <>
              <HistogramSection detail={detail} />
              <StatusSection detail={detail} />
              <ExceptionsSection detail={detail} />
              <FailingTracesSection detail={detail} />
              <SplitSection refObj={refObj} from={from} to={to} />
            </>
          )}
        </div>
      </div>
    </>
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

// fmtMsShort — log-bin axis labels: µs under 1ms, one decimal under
// 10ms, whole ms to 1s, seconds above.
function fmtMsShort(v: number): string {
  if (v < 1) return `${(v * 1000).toFixed(0)}µs`;
  if (v < 10) return `${v.toFixed(1)}ms`;
  if (v < 1000) return `${v.toFixed(0)}ms`;
  return `${(v / 1000).toFixed(1)}s`;
}

// HistogramSection — 1-D latency distribution as plain div bars
// (LogsHistogram precedent — no chart dep for a ≤28-bar static
// histogram; uPlot buys crosshair/zoom we don't need here and its
// x-scale is time-shaped). Bin i covers (bins[i-1], bins[i]] ms on the
// heatmap's log grid; native title tooltips carry the exact range.
function HistogramSection({ detail }: { detail: EndpointDetail }) {
  const h = detail.histogram;
  const t = useMemo(
    () => (h ? trimHistogram(h.bins, h.counts) : { bins: [], counts: [] }),
    [h],
  );
  const total = useMemo(() => t.counts.reduce((s, c) => s + c, 0), [t]);
  const sampled = h?.samplingRate !== undefined && h.samplingRate > 0 && h.samplingRate < 1;
  return (
    <div>
      <SectionTitle>
        Latency distribution
        {sampled && (
          <span className="badge b-gray" style={{ fontSize: 9, marginLeft: 6 }}
            title={`Counts extrapolated from a deterministic 1-in-${Math.round(1 / (h!.samplingRate as number))} trace sample (wide window). Shape is exact.`}>
            sampled ×{Math.round(1 / (h!.samplingRate as number))}
          </span>
        )}
      </SectionTitle>
      {!h && <SectionUnavailable what="Distribution" />}
      {h && t.counts.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>No spans in window.</div>
      )}
      {h && t.counts.length > 0 && (
        <>
          <div style={{
            display: 'flex', alignItems: 'flex-end', gap: 2,
            height: 110, padding: '0 2px',
            borderBottom: '1px solid var(--border)',
          }}>
            {t.counts.map((c, i) => {
              const max = Math.max(...t.counts, 1);
              const lo = i > 0 ? t.bins[i - 1] : 0;
              return (
                <div key={i}
                  title={`${fmtNum(c)} spans in ${fmtMsShort(lo)} – ${fmtMsShort(t.bins[i])}`}
                  style={{
                    flex: 1, minWidth: 3,
                    height: `${Math.max(c > 0 ? 3 : 0, (c / max) * 100)}%`,
                    background: 'var(--accent)', opacity: 0.85,
                    borderRadius: '2px 2px 0 0',
                  }} />
              );
            })}
          </div>
          <div style={{
            display: 'flex', justifyContent: 'space-between',
            fontSize: 9, color: 'var(--text3)', fontFamily: 'ui-monospace, monospace',
            marginTop: 2,
          }}>
            <span>{fmtMsShort(t.bins.length > 1 ? t.bins[0] : 0)}</span>
            {t.bins.length > 2 && <span>{fmtMsShort(t.bins[Math.floor(t.bins.length / 2)])}</span>}
            <span>{fmtMsShort(t.bins[t.bins.length - 1])}</span>
          </div>
          <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 4 }}>
            {fmtNum(total)} spans · log-scale duration bins
          </div>
        </>
      )}
    </div>
  );
}

// StatusSection — class pills + per-code chips.
function StatusSection({ detail }: { detail: EndpointDetail }) {
  const st = detail.statusBreakdown;
  const codes = useMemo(() => {
    if (!st) return [];
    return Object.entries(st.codes)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 8);
  }, [st]);
  const classTotal = st ? st.http2xx + st.http3xx + st.http4xx + st.http5xx : 0;
  return (
    <div>
      <SectionTitle>Status breakdown</SectionTitle>
      {!st && <SectionUnavailable what="Status breakdown" />}
      {st && classTotal === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No http.status_code on this endpoint's spans (non-HTTP / gRPC-only).
        </div>
      )}
      {st && classTotal > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5, alignItems: 'center' }}>
          {st.http2xx > 0 && <span className="badge b-ok">2xx {fmtNum(st.http2xx)}</span>}
          {st.http3xx > 0 && <span className="badge b-gray">3xx {fmtNum(st.http3xx)}</span>}
          {st.http4xx > 0 && <span className="badge b-warn">4xx {fmtNum(st.http4xx)}</span>}
          {st.http5xx > 0 && <span className="badge b-err">5xx {fmtNum(st.http5xx)}</span>}
          <span style={{ width: 1, height: 14, background: 'var(--border)', margin: '0 4px' }} />
          {codes.map(([code, cnt]) => (
            <span key={code} className="mono" style={{ fontSize: 10, color: 'var(--text2)' }}
              title={`${fmtNum(cnt)} responses with status ${code}`}>
              {code}×{fmtNum(cnt)}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

// ExceptionsSection — top exception types ON this route's spans.
function ExceptionsSection({ detail }: { detail: EndpointDetail }) {
  const exs = detail.topExceptions;
  return (
    <div>
      <SectionTitle>Top exceptions</SectionTitle>
      {!exs && <SectionUnavailable what="Exceptions" />}
      {exs && exs.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No exceptions recorded on this endpoint's spans in the window.
        </div>
      )}
      {exs && exs.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {exs.map(ex => (
            <div key={ex.fingerprint + ex.type} style={{
              display: 'flex', alignItems: 'baseline', gap: 8, fontSize: 11,
            }}>
              <span className="badge b-err" style={{ fontSize: 9, flexShrink: 0 }}>
                ×{fmtNum(ex.count)}
              </span>
              <Link to={`/problems?tab=open&exception=${encodeURIComponent(ex.fingerprint)}`}
                className="mono" style={{ color: 'var(--accent2)', flexShrink: 0 }}
                title={`Open in the Problems inbox (last seen ${tsLong(ex.lastSeenNs)})`}>
                {ex.type}
              </Link>
              <span style={{
                color: 'var(--text3)', overflow: 'hidden',
                textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={ex.message}>
                {ex.message || '(no message)'}
              </span>
            </div>
          ))}
          <div style={{ fontSize: 10, color: 'var(--text3)' }}>
            Scoped to spans carrying this route (or named exactly like it) —
            exceptions thrown on unrelated child spans stay under the
            service's inbox.
          </div>
        </div>
      )}
    </div>
  );
}

// FailingTracesSection — direct /trace pivots, worst first, plus the
// slow/error exemplars off the metrics rollup.
function FailingTracesSection({ detail }: { detail: EndpointDetail }) {
  const traces = detail.failingTraces;
  const ex = detail.exemplars;
  return (
    <div>
      <SectionTitle>
        Failing traces
        {(ex?.slowTraceId || ex?.errorTraceId) && (
          <span style={{ fontWeight: 400, fontSize: 11, marginLeft: 8 }}>
            {ex?.slowTraceId && (
              <Link to={`/trace?id=${encodeURIComponent(ex.slowTraceId)}`}
                style={{ color: 'var(--accent2)', marginRight: 10 }}
                title="Slowest trace in the window (metrics-rollup exemplar)">
                slowest →
              </Link>
            )}
            {ex?.errorTraceId && (
              <Link to={`/trace?id=${encodeURIComponent(ex.errorTraceId)}`}
                style={{ color: 'var(--err)' }}
                title="Slowest ERRORED trace in the window (metrics-rollup exemplar)">
                worst error →
              </Link>
            )}
          </span>
        )}
      </SectionTitle>
      {!traces && <SectionUnavailable what="Failing traces" />}
      {traces && traces.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No error spans on this endpoint in the window.
        </div>
      )}
      {traces && traces.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          {traces.map(t => (
            <div key={t.traceId} style={{
              display: 'flex', alignItems: 'baseline', gap: 8, fontSize: 11,
            }}>
              <Link to={`/trace?id=${encodeURIComponent(t.traceId)}`}
                className="mono" style={{ color: 'var(--accent2)', flexShrink: 0 }}
                title={`Open trace ${t.traceId} (${tsLong(t.timeNs)})`}>
                {t.traceId.slice(0, 16)}…
              </Link>
              <span className="mono" style={{ color: 'var(--text)', flexShrink: 0 }}>
                {t.durationMs.toFixed(1)} ms
              </span>
              {t.httpStatus ? (
                <span className={`badge ${t.httpStatus >= 500 ? 'b-err' : 'b-warn'}`}
                  style={{ fontSize: 9, flexShrink: 0 }}>
                  {t.httpStatus}
                </span>
              ) : null}
              <span style={{
                color: 'var(--text3)', overflow: 'hidden',
                textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={t.statusMsg || t.spanName}>
                {t.statusMsg || t.spanName}
                {t.errorSpans > 1 ? ` · ${t.errorSpans} error spans` : ''}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ENDPOINT_SPLIT_DIMS mirrors the backend whitelist
// (chstore.EndpointSplitDims — endpoints_detail.go). Keep in lockstep:
// an id missing there 400s loudly with the allowed list.
const ENDPOINT_SPLIT_DIMS = [
  'deployment.environment',
  'host.name',
  'http.method',
  'http.status_code',
  'k8s.pod.name',
  'peer.service',
  'service.version',
  'span.kind',
  'status_code',
] as const;

const SPLIT_COLS: DataTableColumn<EndpointSplitValue>[] = [
  { id: 'value',     label: 'Value',  sortValue: r => r.value,     naturalDir: 'asc', width: 180 },
  { id: 'calls',     label: 'Calls',  sortValue: r => r.calls,     numeric: true, width: 70 },
  { id: 'errors',    label: 'Errors', sortValue: r => r.errors,    numeric: true, width: 64 },
  { id: 'errorRate', label: 'Err %',  sortValue: r => r.errorRate, numeric: true, width: 64 },
  { id: 'avgMs',     label: 'Avg',    sortValue: r => r.avgMs,     numeric: true, width: 66 },
  { id: 'p99Ms',     label: 'P99',    sortValue: r => r.p99Ms,     numeric: true, width: 66 },
];

// SplitSection — pick a whitelisted attribute, get its top-10 values
// with RED each. Fetches ONLY once a dimension is picked (enabled
// gate in useEndpointSplit) so opening the drawer costs nothing here.
function SplitSection({ refObj, from, to }: {
  refObj: EndpointRef; from: number; to: number;
}) {
  const [by, setBy] = useState('');
  const splitQ = useEndpointSplit(by ? {
    service: refObj.service, path: refObj.path, by, from, to,
    ...(refObj.sig ? { sig: '1' as const } : {}),
  } : null);
  const rows = splitQ.data?.values ?? [];
  const dt = useDataTable<EndpointSplitValue>({
    storageKey: 'endpoint-split',
    columns: SPLIT_COLS,
    rows,
    initialSort: { id: 'calls', dir: 'desc' },
  });
  return (
    <div>
      <SectionTitle>Split by attribute</SectionTitle>
      {/* Small fixed whitelist → plain <select> per the picker rule. */}
      <select value={by} onChange={e => setBy(e.target.value)}
        style={{ fontSize: 12, marginBottom: 8 }}
        title="Break this endpoint's RED metrics down by one attribute (top 10 values by calls)">
        <option value="">Pick an attribute…</option>
        {ENDPOINT_SPLIT_DIMS.map(d => (
          <option key={d} value={d}>{d}</option>
        ))}
      </select>
      {by && splitQ.isPending && <Spinner />}
      {by && splitQ.isError && (
        <div style={{ fontSize: 11, color: 'var(--err)' }}>Split query failed.</div>
      )}
      {by && splitQ.data && rows.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No values for <code>{by}</code> on this endpoint in the window.
        </div>
      )}
      {by && rows.length > 0 && (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((r, i) => {
                const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                return (
                  <tr key={`${r.value}|${i}`}>
                    <td className="mono" style={{ fontSize: 11 }} title={r.value}>{r.value}</td>
                    <td className="num mono">{fmtNum(r.calls)}</td>
                    <td className="num mono">{fmtNum(r.errors)}</td>
                    <td className="num mono">
                      <span className={`badge ${errCls}`} style={{ fontSize: 9 }}>
                        {r.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">{r.avgMs.toFixed(1)}ms</td>
                    <td className="num mono">{r.p99Ms.toFixed(1)}ms</td>
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
