import { useEffect, useState, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Modal } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { TraceDetailResponse, LogRow } from '@/lib/types';

// TracePeekDrawer — v0.5.398. Logs page side-loop drill-in.
// When the operator clicks the "👁" peek button next to a trace_id
// chip in the log table, this modal opens with:
//
//   1. Trace summary (root service.op, total duration, span count,
//      services involved) — same shape the trace detail page header
//      uses, condensed.
//   2. Mini waterfall — per-service horizontal bar showing span
//      density over the trace window. Lets the operator see "where
//      did most of this trace's time go" without leaving /logs.
//   3. All log lines for this trace_id, chronological — closes the
//      "what else was emitted around this log" loop that the
//      operator otherwise has to satisfy by filtering manually.
//
// "Open full trace →" link still navigates to /trace?id=…
// when the operator wants the proper waterfall + span detail
// surface. The drawer is a preview, not a replacement.
//
// Why a Modal and not an inline panel:
//   - Doesn't disturb the operator's existing log filter / search
//     state on the page underneath.
//   - ESC / backdrop close — standard "peek" affordance the rest
//     of the app uses (Endpoints metric modal, Service ops modal).
export function TracePeekDrawer({
  traceId, onClose,
}: { traceId: string | null; onClose: () => void }) {
  const [trace, setTrace] = useState<TraceDetailResponse | null | undefined>(undefined);
  const [logs, setLogs] = useState<LogRow[] | null | undefined>(undefined);

  useEffect(() => {
    if (!traceId) {
      setTrace(undefined); setLogs(undefined);
      return;
    }
    let cancelled = false;
    setTrace(undefined); setLogs(undefined);
    // Fire both in parallel — both are fast (single trace_id lookup,
    // filtered log list cap=500). Operator sees the modal open
    // immediately with a spinner; first response paints.
    api.trace(traceId)
      .then(r => { if (!cancelled) setTrace(r ?? null); })
      .catch(() => { if (!cancelled) setTrace(null); });
    api.logs({ traceId, limit: 500 })
      .then(r => { if (!cancelled) setLogs(r?.logs ?? []); })
      .catch(() => { if (!cancelled) setLogs(null); });
    return () => { cancelled = true; };
  }, [traceId]);

  // Derive trace-level scalars from the spans payload (root span +
  // total duration + service set). Same logic the /trace page uses
  // in its header; duplicated here to keep the drawer self-
  // contained (no extra round-trip for a summary endpoint).
  const summary = useMemo(() => {
    if (!trace || !trace.spans || trace.spans.length === 0) return null;
    const root = trace.spans.find(s => !s.parentSpanId) ?? trace.spans[0];
    const minStart = trace.spans.reduce((m, s) => Math.min(m, s.startTime), Infinity);
    const maxEnd = trace.spans.reduce((m, s) => Math.max(m, s.endTime), 0);
    const totalMs = (maxEnd - minStart) / 1e6;
    const services = Array.from(new Set(trace.spans.map(s => s.serviceName))).sort();
    const errSpans = trace.spans.filter(s => s.statusCode === 'error').length;
    // Per-service span counts for the mini waterfall — order by
    // first appearance so the visual reads top→down as the trace's
    // call sequence.
    const firstSeenByService = new Map<string, number>();
    for (const s of trace.spans) {
      if (!firstSeenByService.has(s.serviceName)) {
        firstSeenByService.set(s.serviceName, s.startTime);
      }
    }
    const orderedServices = Array.from(firstSeenByService.entries())
      .sort((a, b) => a[1] - b[1])
      .map(([n]) => n);
    return {
      root, totalMs, services, orderedServices,
      errSpans, minStart, maxEnd,
      spanCount: trace.spans.length,
    };
  }, [trace]);

  if (!traceId) return <Modal open={false} onClose={onClose} />;

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span style={{ fontSize: 13 }}>
          Trace peek
          <span className="mono" style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            {traceId.slice(0, 16)}…
          </span>
        </span>
      }
    >
      {trace === undefined && <Spinner />}
      {trace === null && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>
          Failed to load trace.
          <div style={{ marginTop: 8 }}>
            <Link to={`/trace?id=${traceId}`} style={{ color: 'var(--accent2)' }}>
              Open full trace →
            </Link>
          </div>
        </div>
      )}
      {trace && summary && (
        <>
          {/* Trace summary row */}
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)',
            gap: 10, marginBottom: 12,
          }}>
            <PeekKPI label="Root op" value={summary.root.name}
              sub={summary.root.serviceName} small />
            <PeekKPI label="Duration" value={`${summary.totalMs.toFixed(0)} ms`} />
            <PeekKPI label="Spans" value={fmtNum(summary.spanCount)}
              sub={`${summary.services.length} services`} />
            <PeekKPI label="Errors" value={fmtNum(summary.errSpans)}
              cls={summary.errSpans > 0 ? 'err' : ''} />
          </div>

          {/* Per-service mini timeline. Each row = service; bar
              shows the span window that service touched, relative
              to the trace's overall start..end. Density bar (not
              a full waterfall — that's what the dedicated /trace
              page is for). Tempo's "service timeline" panel uses
              the same compression. */}
          <div style={{
            border: '1px solid var(--border)', borderRadius: 6,
            padding: 8, marginBottom: 12, background: 'var(--bg1)',
          }}>
            <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4, marginBottom: 6 }}>
              Service timeline
            </div>
            {summary.orderedServices.map((svc, i) => {
              const svcSpans = trace.spans.filter(s => s.serviceName === svc);
              const svcStart = Math.min(...svcSpans.map(s => s.startTime));
              const svcEnd = Math.max(...svcSpans.map(s => s.endTime));
              const totalNs = summary.maxEnd - summary.minStart;
              const left = totalNs > 0 ? ((svcStart - summary.minStart) / totalNs) * 100 : 0;
              const width = totalNs > 0 ? Math.max(0.5, ((svcEnd - svcStart) / totalNs) * 100) : 100;
              const svcErrs = svcSpans.filter(s => s.statusCode === 'error').length;
              const barColor = svcErrs > 0 ? 'var(--err)' : 'var(--accent2)';
              return (
                <div key={svc} style={{
                  display: 'grid', gridTemplateColumns: '140px 1fr 80px',
                  gap: 6, alignItems: 'center', marginBottom: 3,
                  fontSize: 11,
                }}>
                  <span className="mono" style={{
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={svc}>{svc}</span>
                  <div style={{
                    position: 'relative', height: 12,
                    background: 'var(--bg2)', borderRadius: 2,
                  }}>
                    <div style={{
                      position: 'absolute', top: 0, bottom: 0,
                      left: `${left}%`, width: `${width}%`,
                      background: barColor, borderRadius: 2,
                      opacity: 0.85,
                    }} />
                  </div>
                  <span className="mono" style={{ fontSize: 10, color: 'var(--text3)', textAlign: 'right' }}>
                    {fmtNum(svcSpans.length)} sp · {((svcEnd - svcStart) / 1e6).toFixed(0)}ms
                  </span>
                </div>
              );
            })}
          </div>

          {/* Logs of this trace, chronological. Limit 500 — caps
              the modal payload for very chatty traces (a 10k-log
              trace would otherwise blow the modal up). The link
              at the bottom widens to the full filtered /logs view
              if 500 wasn't enough. */}
          <div style={{
            border: '1px solid var(--border)', borderRadius: 6,
            background: 'var(--bg1)', marginBottom: 12,
          }}>
            <div style={{
              padding: '6px 10px', borderBottom: '1px solid var(--border)',
              fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4,
              display: 'flex', justifyContent: 'space-between', alignItems: 'center',
            }}>
              <span>Logs in this trace</span>
              <span style={{ color: 'var(--text3)', textTransform: 'none', letterSpacing: 0 }}>
                {logs === undefined ? '…' :
                 logs === null ? 'failed' :
                 `${fmtNum(logs.length)}${logs.length >= 500 ? '+' : ''}`}
              </span>
            </div>
            {logs === undefined && (
              <div style={{ padding: 12 }}><Spinner /></div>
            )}
            {logs && logs.length === 0 && (
              <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
                No logs emitted during this trace's window.
              </div>
            )}
            {logs && logs.length > 0 && (
              <div style={{ maxHeight: 320, overflowY: 'auto', padding: 4 }}>
                {logs
                  .slice()
                  .sort((a, b) => a.timestamp - b.timestamp)
                  .map(l => {
                    const offsetMs = summary
                      ? (l.timestamp - summary.minStart) / 1e6
                      : 0;
                    return (
                      <div key={l.id} style={{
                        display: 'grid',
                        gridTemplateColumns: '60px 50px 110px 1fr',
                        gap: 6, padding: '2px 6px',
                        fontSize: 11, fontFamily: 'ui-monospace, monospace',
                        borderBottom: '1px solid var(--bg2)',
                        alignItems: 'baseline',
                      }}>
                        <span style={{ color: 'var(--text3)', textAlign: 'right' }}>
                          {offsetMs >= 0 ? `+${offsetMs.toFixed(0)}ms` : `${offsetMs.toFixed(0)}ms`}
                        </span>
                        <span className={sevClass(l.severityText)}
                              style={{ fontWeight: 600 }}>
                          {(l.severityText || '').toUpperCase().slice(0, 4) || '—'}
                        </span>
                        <span style={{ color: 'var(--text2)',
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }} title={l.serviceName}>
                          {l.serviceName}
                        </span>
                        <span style={{
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }} title={l.body}>{l.body}</span>
                      </div>
                    );
                  })}
              </div>
            )}
          </div>

          <div style={{ display: 'flex', gap: 12 }}>
            <Link to={`/trace?id=${traceId}`}
              style={{ fontSize: 12, color: 'var(--accent2)' }}>
              Open full trace →
            </Link>
            <Link to={`/logs?traceId=${traceId}`}
              style={{ fontSize: 12, color: 'var(--accent2)' }}>
              Filter /logs to this trace →
            </Link>
          </div>
        </>
      )}
    </Modal>
  );
}

function PeekKPI({ label, value, sub, cls, small }: {
  label: string; value: string; sub?: string; cls?: string; small?: boolean;
}) {
  return (
    <div style={{
      padding: '6px 10px', border: '1px solid var(--border)',
      borderRadius: 4, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: small ? 12 : 16, fontWeight: 600, marginTop: 2,
        color: cls === 'err' ? 'var(--err)' : 'var(--text)',
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }} title={value}>{value}</div>
      {sub && (
        <div style={{ fontSize: 10, color: 'var(--text3)',
          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }} title={sub}>{sub}</div>
      )}
    </div>
  );
}

function sevClass(s: string): string {
  switch ((s || '').toUpperCase()) {
    case 'FATAL':
    case 'ERROR':   return 'sev-err';
    case 'WARN':
    case 'WARNING': return 'sev-warn';
    case 'INFO':    return 'sev-info';
    default:        return 'sev-dim';
  }
}
