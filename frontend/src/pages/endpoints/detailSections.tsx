import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { useEndpointSplit, useEndpointDownstream, useEndpointCallers } from '@/lib/queries';
import { fmtNum, tsLong } from '@/lib/utils';
import type { DataTableColumn } from '@/lib/dataTable';
import type { EndpointDetail, EndpointSplitValue, EndpointCaller, EndpointFailingTrace } from '@/lib/types';

// v0.9.874 (tutarlılık denetimi BT10) — aynı dosyadaki Split ve Callers
// tabloları çoktan primitifteydi, bu kalmıştı. Kolon genişlikleri elle
// yazılmış <th style={{width}}>'lerden AYNEN alındı; Error kolonu esner.
const FAILING_TRACE_COLS: DataTableColumn<EndpointFailingTrace>[] = [
  { id: 'time',     label: 'Time',     sortValue: t => t.timeNs,     width: 96 },
  { id: 'trace',    label: 'Trace',    sortValue: t => t.traceId,    naturalDir: 'asc', width: 150 },
  { id: 'error',    label: 'Error',    sortValue: t => t.statusMsg || t.spanName, naturalDir: 'asc', flex: true },
  { id: 'duration', label: 'Duration', sortValue: t => t.durationMs, numeric: true, width: 92 },
];
import { trimHistogram, type EndpointRef } from './endpointParam';

// detailSections — the /endpoint page's body (v0.9.839).
//
// PROMOTED, not copied: these six sections were the body of
// pages/endpoints/DetailDrawer.tsx (v0.8.360 → v0.9.311). The drawer
// SHELL is gone — a row click now navigates to a full page — and the
// sections moved here whole. Behaviour is unchanged section by section;
// what changed is the room they get.
//
// The seventh, CallersSection, is new (operator ask): the /databases
// caller table, on the endpoint surface.
//
// Every section keeps the drawer's NULL TOLERANCE contract: a failed
// section renders its own fallback line, never blanks its neighbours.

/** PanelTitle — the mockup's uppercase card head with an optional
 *  lower-case qualifier (the honesty slot: "sampled", "log-bin", …). */
export function PanelTitle({ children, sub, right }: {
  children: React.ReactNode; sub?: React.ReactNode; right?: React.ReactNode;
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
      <span style={{
        fontSize: 11, fontWeight: 700, letterSpacing: 0.4,
        textTransform: 'uppercase', color: 'var(--text2)',
      }}>{children}</span>
      {sub && (
        <span style={{ fontSize: 10.5, fontWeight: 400, color: 'var(--text3)' }}>{sub}</span>
      )}
      {right && <span style={{ marginLeft: 'auto', fontWeight: 400 }}>{right}</span>}
    </div>
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
export function HistogramSection({ detail }: { detail: EndpointDetail }) {
  const h = detail.histogram;
  const t = useMemo(
    () => (h ? trimHistogram(h.bins, h.counts) : { bins: [], counts: [] }),
    [h],
  );
  const total = useMemo(() => t.counts.reduce((s, c) => s + c, 0), [t]);
  const max = useMemo(() => Math.max(...t.counts, 1), [t]);
  const sampled = h?.samplingRate !== undefined && h.samplingRate > 0 && h.samplingRate < 1;
  return (
    <Card header={
      <PanelTitle sub="log-bin histogram">
        Latency distribution
        {sampled && (
          <span className="badge b-gray" style={{ fontSize: 9, marginLeft: 6 }}
            title={`Counts extrapolated from a deterministic 1-in-${Math.round(1 / (h!.samplingRate as number))} trace sample (wide window). Shape is exact.`}>
            sampled ×{Math.round(1 / (h!.samplingRate as number))}
          </span>
        )}
      </PanelTitle>
    }>
      {!h && <SectionUnavailable what="Distribution" />}
      {h && t.counts.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>No spans in window.</div>
      )}
      {h && t.counts.length > 0 && (
        <>
          <div style={{
            display: 'flex', alignItems: 'flex-end', gap: 2,
            height: 130, padding: '0 2px',
            borderBottom: '1px solid var(--border)',
          }}>
            {t.counts.map((c, i) => {
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
    </Card>
  );
}

// StatusSection — class pills + per-code chips.
export function StatusSection({ detail }: { detail: EndpointDetail }) {
  const st = detail.statusBreakdown;
  const codes = useMemo(() => {
    if (!st) return [];
    return Object.entries(st.codes)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 8);
  }, [st]);
  const classTotal = st ? st.http2xx + st.http3xx + st.http4xx + st.http5xx : 0;
  // The mockup's proportion bars: a class list without shares reads as
  // "there are some 5xx", which is not the question. Shares are of the
  // CLASSED spans, not of all calls — spans with no http.status_code
  // are outside this section by definition (see the empty state).
  const bars: Array<{ label: string; n: number; cls: string; color: string }> = st ? [
    { label: '2xx', n: st.http2xx, cls: 'b-ok', color: 'var(--ok)' },
    { label: '3xx', n: st.http3xx, cls: 'b-gray', color: 'var(--text3)' },
    { label: '4xx', n: st.http4xx, cls: 'b-warn', color: 'var(--warn)' },
    { label: '5xx', n: st.http5xx, cls: 'b-err', color: 'var(--err)' },
  ].filter(b => b.n > 0) : [];
  return (
    <Card header={<PanelTitle>Status breakdown</PanelTitle>}>
      {!st && <SectionUnavailable what="Status breakdown" />}
      {st && classTotal === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No http.status_code on this endpoint's spans (non-HTTP / gRPC-only).
        </div>
      )}
      {st && classTotal > 0 && (
        <>
          {bars.map(b => (
            <div key={b.label} style={{
              display: 'flex', alignItems: 'center', gap: 8, margin: '3px 0', fontSize: 11.5,
            }}>
              <span className={`badge ${b.cls}`} style={{ fontSize: 9, flex: '0 0 42px' }}>
                {b.label}
              </span>
              <div style={{
                flex: 1, height: 12, borderRadius: 3,
                background: 'color-mix(in srgb, var(--text3) 15%, transparent)',
              }}>
                <div style={{
                  width: `${(b.n / classTotal) * 100}%`, height: '100%',
                  background: b.color, opacity: 0.75, borderRadius: 3,
                }} />
              </div>
              <span className="mono" style={{
                flex: '0 0 96px', textAlign: 'right', color: 'var(--text2)',
              }}>
                {fmtNum(b.n)} · {((b.n / classTotal) * 100).toFixed(1)}%
              </span>
            </div>
          ))}
          <div style={{
            display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 8,
            fontSize: 10.5, color: 'var(--text2)',
          }}>
            {codes.map(([code, cnt]) => (
              <span key={code} className="mono"
                title={`${fmtNum(cnt)} responses with status ${code}`}>
                {code}×{fmtNum(cnt)}
              </span>
            ))}
          </div>
        </>
      )}
    </Card>
  );
}

// ExceptionsSection — top exception types ON this route's spans.
export function ExceptionsSection({ detail }: { detail: EndpointDetail }) {
  const exs = detail.topExceptions;
  return (
    <Card header={<PanelTitle sub="on this route's spans">Related exception groups</PanelTitle>}>
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
              display: 'flex', alignItems: 'baseline', gap: 8, fontSize: 11.5,
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
    </Card>
  );
}

// FailingTracesSection — direct /trace pivots, worst first, plus the
// slow/error exemplars off the metrics rollup.
export function FailingTracesSection({ detail }: { detail: EndpointDetail }) {
  const traces = detail.failingTraces;
  const ex = detail.exemplars;
  const dt = useDataTable<EndpointFailingTrace>({
    storageKey: 'endpoint-failing-traces', columns: FAILING_TRACE_COLS,
    rows: traces ?? [], initialSort: { id: 'time', dir: 'desc' },
  });
  return (
    <Card header={
      <PanelTitle sub="worst first" right={
        (ex?.slowTraceId || ex?.errorTraceId) ? (
          <span style={{ fontSize: 11 }}>
            {ex?.slowTraceId && (
              <Link to={`/trace?id=${encodeURIComponent(ex.slowTraceId)}`}
                style={{ color: 'var(--warn)', marginRight: 10 }}
                title="Slowest trace in the window (metrics-rollup exemplar)">
                ⚡ slowest trace →
              </Link>
            )}
            {ex?.errorTraceId && (
              <Link to={`/trace?id=${encodeURIComponent(ex.errorTraceId)}`}
                style={{ color: 'var(--err)' }}
                title="Slowest ERRORED trace in the window (metrics-rollup exemplar)">
                ✖ worst error →
              </Link>
            )}
          </span>
        ) : undefined
      }>Failing traces</PanelTitle>
    }>
      {!traces && <SectionUnavailable what="Failing traces" />}
      {traces && traces.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No error spans on this endpoint in the window.
        </div>
      )}
      {traces && traces.length > 0 && (
        <div className="table-wrap">
          <table style={{ width: '100%', tableLayout: 'fixed' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(t => (
                <tr key={t.traceId}>
                  <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                    {tsLong(t.timeNs)}
                  </td>
                  <td>
                    <Link to={`/trace?id=${encodeURIComponent(t.traceId)}`}
                      className="mono" style={{ fontSize: 11, color: 'var(--accent2)' }}
                      title={`Open trace ${t.traceId}`}>
                      {t.traceId.slice(0, 16)}… →
                    </Link>
                  </td>
                  <td style={{
                    fontSize: 11.5, maxWidth: 0,
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={t.statusMsg || t.spanName}>
                    {t.httpStatus ? (
                      <span className={`badge ${t.httpStatus >= 500 ? 'b-err' : 'b-warn'}`}
                        style={{ fontSize: 9, marginRight: 6 }}>
                        {t.httpStatus}
                      </span>
                    ) : null}
                    {t.statusMsg || t.spanName}
                    {t.errorSpans > 1 ? ` · ${t.errorSpans} error spans` : ''}
                  </td>
                  <td className="num mono">{t.durationMs.toFixed(1)} ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
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
// with RED each. Fetches ONLY once a dimension is picked (enabled gate
// in useEndpointSplit) so loading the page costs nothing here.
export function SplitSection({ refObj, from, to, env, cluster }: {
  refObj: EndpointRef; from: number; to: number; env?: string; cluster?: string;
}) {
  const [by, setBy] = useState('');
  const splitQ = useEndpointSplit(by ? {
    service: refObj.service, path: refObj.path, by, from, to,
    ...(refObj.sig ? { sig: '1' as const } : {}),
    ...(env ? { env } : {}),
    ...(cluster ? { cluster } : {}),
  } : null);
  const rows = splitQ.data?.values ?? [];
  const dt = useDataTable<EndpointSplitValue>({
    storageKey: 'endpoint-split',
    columns: SPLIT_COLS,
    rows,
    initialSort: { id: 'calls', dir: 'desc' },
  });
  return (
    <Card header={
      <PanelTitle sub="top 10 values, whitelisted dimensions">Break down by</PanelTitle>
    }>
      {/* Small fixed whitelist → plain <select> per the picker rule. */}
      <select value={by} onChange={e => setBy(e.target.value)}
        style={{ fontSize: 12, marginBottom: 8 }}
        title="Break this endpoint's RED metrics down by one attribute (top 10 values by calls)">
        <option value="">Pick an attribute…</option>
        {ENDPOINT_SPLIT_DIMS.map(d => (
          <option key={d} value={d}>{d}</option>
        ))}
      </select>
      {!by && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Pick a dimension to split this route's RED metrics — nothing is
          queried until you do.
        </div>
      )}
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
    </Card>
  );
}

// WhereTheTimeGoesSection — v0.9.311 (brief N4).
//
// The drill-down could say a route's p99 was 900ms and not where those
// 900ms went. This splits the route's time across what it calls, and —
// separately — how much of it is spent in a database at ANY depth.
//
// The two lists are NEVER summed. `downstream` holds direct children,
// which sum to the sampled entry duration; `backends` holds DB time
// found anywhere below, which is already INSIDE those children. Live
// data made that split necessary: a gateway route's 659ms had one
// direct child (account-service, 645ms) whose own child was a 623ms
// Oracle query. "645ms in account-service" is true and nearly useless.
//
// v0.9.839 — the Callers TAB is gone. This payload's caller list is
// sampled from the route's SLOWEST traces, which is the right lean for
// a latency question and the wrong one for "who calls me": it ranks the
// caller with the worst traces as if it had the most traffic.
// CallersSection below answers that question from an unbiased sample.
// Two caller lists on one page that disagree would be exactly the class
// of on-screen contradiction v0.9.306 was cut to remove.
export function WhereTheTimeGoesSection({ refObj, from, to, env, cluster }: {
  refObj: EndpointRef; from: number; to: number; env?: string; cluster?: string;
}) {
  const q = useEndpointDownstream({
    service: refObj.service, path: refObj.path, from, to,
    ...(refObj.sig ? { sig: '1' as const } : {}),
    ...(env ? { env } : {}),
    ...(cluster ? { cluster } : {}),
  });
  const d = q.data;
  const rows = d?.downstream ?? [];
  const total = d?.totalMs ?? 0;

  return (
    <Card header={
      <PanelTitle
        sub={d && d.sampledFrom > 0 ? `sample of the ${d.sampledFrom} slowest traces` : undefined}>
        Where the time goes
      </PanelTitle>
    }>
      {q.isPending && <Spinner />}
      {q.isError && (
        <div style={{ fontSize: 11, color: 'var(--err)' }}>
          Could not sample this route's traces.
        </div>
      )}
      {d && d.sampledFrom === 0 && (
        <Empty icon="◷" title="No traces for this route in the window">
          Widen the time range — the split is derived from real traces, so
          there is nothing to divide yet.
        </Empty>
      )}
      {d && d.sampledFrom > 0 && rows.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          This route calls nothing else — all of its time is its own.
        </div>
      )}

      {rows.map(e => {
        const pct = total > 0 ? (e.shareMs / total) * 100 : 0;
        const tone = e.kind === 'self' ? 'var(--text3)'
          : e.kind === 'db' ? 'var(--warn)' : 'var(--accent2)';
        return (
          <div key={`${e.kind}/${e.name}`} style={{ marginBottom: 5 }}>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, fontSize: 11.5 }}>
              <span className="mono" style={{
                color: tone, fontWeight: 600,
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={e.name}>{e.name}</span>
              <span style={{ color: 'var(--text3)', fontSize: 10 }}>{e.kind}</span>
              <span style={{ flex: 1 }} />
              {e.errors > 0 && (
                <span style={{ color: 'var(--err)' }}>{e.errors} err</span>
              )}
              <span style={{ color: 'var(--text2)', fontVariantNumeric: 'tabular-nums' }}>
                p99 {e.p99Ms.toFixed(0)}ms
              </span>
              <span style={{ color: 'var(--text)', fontWeight: 600, fontVariantNumeric: 'tabular-nums' }}>
                {pct.toFixed(0)}%
              </span>
            </div>
            <div style={{
              height: 5, borderRadius: 3, overflow: 'hidden',
              background: 'color-mix(in srgb, var(--text3) 20%, transparent)',
            }}>
              <div style={{ width: `${Math.min(100, pct)}%`, height: '100%', background: tone }} />
            </div>
          </div>
        );
      })}

      {/* Backends: DB time at ANY depth. Separate on purpose — see the
          function comment. */}
      {d && d.backends.length > 0 && (
        <div style={{ marginTop: 8, paddingTop: 6, borderTop: '1px dashed var(--border)' }}>
          <div style={{ fontSize: 10.5, color: 'var(--text3)', marginBottom: 4 }}
            title="Database and broker time found ANYWHERE beneath this route, including inside the services above. Shown separately because that time is already counted in its parent's share — adding the two lists together would count the same milliseconds twice.">
            of which, in backends (any depth)
          </div>
          {d.backends.map(b => (
            <div key={b.name} style={{
              display: 'flex', alignItems: 'baseline', gap: 6,
              fontSize: 11, marginBottom: 2,
            }}>
              <span className="mono" style={{ color: 'var(--warn)' }}>{b.name}</span>
              <span style={{ flex: 1 }} />
              <span style={{ color: 'var(--text2)', fontVariantNumeric: 'tabular-nums' }}>
                {b.calls} calls · p99 {b.p99Ms.toFixed(0)}ms
              </span>
              <span style={{ color: 'var(--text)', fontWeight: 600, fontVariantNumeric: 'tabular-nums' }}>
                {total > 0 ? ((b.shareMs / total) * 100).toFixed(0) : 0}%
              </span>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

const CALLER_COLS: DataTableColumn<EndpointCaller>[] = [
  { id: 'service',   label: 'Service', sortValue: r => r.service,   naturalDir: 'asc', width: 260 },
  { id: 'calls',     label: 'Calls',   sortValue: r => r.calls,     numeric: true, width: 80 },
  { id: 'errorRate', label: 'Err %',   sortValue: r => r.errorRate, numeric: true, width: 72 },
  { id: 'p95Ms',     label: 'P95',     sortValue: r => r.p95Ms,     numeric: true, width: 80 },
  { id: 'sharePct',  label: 'Impact',  sortValue: r => r.sharePct,  numeric: true, width: 80 },
];

// CallersSection — "Who calls this" (v0.9.839, operator ask).
//
// Same columns as the /databases caller table so the operator reads one
// shape on both surfaces. Ranked by IMPACT (share of this route's time),
// not by call count: a caller with a tenth of the traffic and ten times
// the latency is the one worth seeing first.
//
// Honesty is structural here. Three different "no caller" answers exist
// and the panel keeps them apart, because folding them would turn "we
// could not see the caller" into "this route has no caller":
//   • directEntries — no parent span at all: an entry point.
//   • unresolved    — a parent id that resolved to nothing.
//   • sampled       — the window held more spans than were read.
export function CallersSection({ refObj, from, to, env, cluster }: {
  refObj: EndpointRef; from: number; to: number; env?: string; cluster?: string;
}) {
  const q = useEndpointCallers({
    service: refObj.service, path: refObj.path, from, to,
    ...(refObj.sig ? { sig: '1' as const } : {}),
    ...(env ? { env } : {}),
    ...(cluster ? { cluster } : {}),
  });
  const rows = q.data?.callers ?? [];
  const dt = useDataTable<EndpointCaller>({
    storageKey: 'endpoint-callers',
    columns: CALLER_COLS,
    rows,
    initialSort: { id: 'sharePct', dir: 'desc' },
  });
  const d = q.data;
  return (
    <Card header={
      <PanelTitle sub={d
        ? (d.sampled
          ? `sample · ${fmtNum(d.sampledSpans)} entry spans`
          : `all ${fmtNum(d.sampledSpans)} entry spans in window`)
        : undefined}>
        Who calls this
      </PanelTitle>
    }>
      {q.isPending && <Spinner />}
      {q.isError && (
        <div style={{ fontSize: 11, color: 'var(--err)' }}>
          Caller query failed — the sections above are still live.
        </div>
      )}
      {d && rows.length === 0 && (
        <Empty icon="↘" title="No caller seen in this window">
          {d.directEntries > 0
            ? 'Every sampled call arrived without a parent span — this route is an entry point into the traced system.'
            : 'The calls have a parent, but no matching parent span is in the store: the caller is uninstrumented, sampled away, or already past retention.'}
        </Empty>
      )}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(r => {
                const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                return (
                  <tr key={r.service}>
                    <td style={{
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }} title={r.service}>
                      <Link to={`/service?name=${encodeURIComponent(r.service)}`}
                        className="mono" style={{ fontSize: 11.5 }}>
                        {r.service}
                      </Link>
                    </td>
                    <td className="num mono">{fmtNum(r.calls)}</td>
                    <td className="num mono">
                      <span className={`badge ${errCls}`} style={{ fontSize: 9 }}>
                        {r.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">{r.p95Ms.toFixed(1)} ms</td>
                    <td className="num mono">{r.sharePct.toFixed(1)}%</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {d && (rows.length > 0) && (d.directEntries > 0 || d.unresolved > 0) && (
        <div style={{ fontSize: 10.5, color: 'var(--text3)', marginTop: 8, lineHeight: 1.5 }}>
          {d.directEntries > 0 && (
            <div title="These calls carried no parent span id at all — the route was entered from outside the traced system (a browser, an external client, a scheduler).">
              {fmtNum(d.directEntries)} of {fmtNum(d.sampledSpans)} sampled calls arrived
              with no caller — direct entry.
            </div>
          )}
          {d.unresolved > 0 && (
            <div title="These calls DO name a parent span, but no such span is in the store for this window: the caller is uninstrumented, its trace was sampled away, or it aged past span retention. Not the same as having no caller.">
              {fmtNum(d.unresolved)} caller span(s) could not be resolved — counted
              in the totals, excluded from the rows above.
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
