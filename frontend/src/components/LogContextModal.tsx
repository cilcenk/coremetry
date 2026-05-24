import { useEffect, useState, useMemo } from 'react';
import { Modal } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { LogRow } from '@/lib/types';

// LogContextModal — v0.5.402. Datadog "Context" tab for /logs.
// Operator clicks "≡ ±50" on an expanded log row → this modal
// fetches the 50 logs immediately BEFORE and 50 AFTER the pivot
// timestamp, scoped to the same service. Pivot row gets a yellow
// border so it stands out in the chronological scroll.
//
// Why: investigating a single error log is rarely enough. The
// surrounding lines almost always reveal what state the service
// was in just before failing + what it tried to do after.
// Datadog's "View in Context" is one of their most-used affordances;
// porting it here closes the same loop.
//
// Server returns: { before: LogRow[] (DESC), after: LogRow[] (ASC),
// pivotTs: number }. We re-sort the union ascending so the operator
// scrolls top→bottom in chronological order. Pivot row is found by
// (timestamp == pivotTs AND id == pivotId) — relying on ts alone
// can collide on busy services where two logs hit the same ns
// bucket.
export function LogContextModal({
  pivot, onClose, onTracePeek,
}: {
  pivot: LogRow | null;
  onClose: () => void;
  onTracePeek?: (traceId: string) => void;
}) {
  const [before, setBefore] = useState<LogRow[] | null | undefined>(undefined);
  const [after,  setAfter]  = useState<LogRow[] | null | undefined>(undefined);

  useEffect(() => {
    if (!pivot) {
      setBefore(undefined); setAfter(undefined);
      return;
    }
    let cancelled = false;
    setBefore(undefined); setAfter(undefined);
    api.logsContext({
      ts: pivot.timestamp,
      service: pivot.serviceName || undefined,
      n: 50,
    })
      .then(r => {
        if (cancelled) return;
        setBefore(r?.before ?? []);
        setAfter(r?.after ?? []);
      })
      .catch(() => {
        if (cancelled) return;
        setBefore(null); setAfter(null);
      });
    return () => { cancelled = true; };
  }, [pivot]);

  // Unified chronological list with pivot inserted between the two
  // halves. Both halves arrive sorted (before DESC, after ASC) so
  // we reverse `before` then concat.
  const rows = useMemo<{ pivotKey: number; rows: LogRow[] } | null>(() => {
    if (!pivot) return null;
    const b = (before ?? []).slice().sort((a, c) => a.timestamp - c.timestamp);
    const a = (after  ?? []).slice().sort((x, y) => x.timestamp - y.timestamp);
    // Pivot might already appear in `before` or `after` since the
    // window is inclusive at both ends. Dedupe by log id so we
    // don't render it twice.
    const seen = new Set<number>([pivot.id]);
    const dedupedBefore = b.filter(l => { if (seen.has(l.id)) return false; seen.add(l.id); return true; });
    const dedupedAfter  = a.filter(l => { if (seen.has(l.id)) return false; seen.add(l.id); return true; });
    return {
      pivotKey: pivot.id,
      rows: [...dedupedBefore, pivot, ...dedupedAfter],
    };
  }, [pivot, before, after]);

  if (!pivot) return <Modal open={false} onClose={onClose} />;

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span style={{ fontSize: 13 }}>
          Context · ±50
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            {pivot.serviceName || '(no service)'} · {new Date(pivot.timestamp / 1e6).toLocaleString()}
          </span>
        </span>
      }
    >
      {(before === undefined || after === undefined) && <Spinner />}
      {(before === null || after === null) && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>
          Failed to load surrounding context.
        </div>
      )}
      {rows && (
        <>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
            {fmtNum(before?.length ?? 0)} log{(before?.length ?? 0) === 1 ? '' : 's'} before
            {' · '}
            {fmtNum(after?.length ?? 0)} after
            {' · '}30-min symmetric window, scoped to same service
          </div>
          <div style={{
            border: '1px solid var(--border)', borderRadius: 6,
            background: 'var(--bg1)',
            maxHeight: 480, overflowY: 'auto',
          }}>
            {rows.rows.map(l => {
              const isPivot = l.id === rows.pivotKey;
              const offsetMs = (l.timestamp - pivot.timestamp) / 1e6;
              const sev = (l.severityText || '').toUpperCase();
              return (
                <div key={l.id} style={{
                  display: 'grid',
                  gridTemplateColumns: '60px 50px 110px 1fr 70px',
                  gap: 6, padding: '3px 8px',
                  fontSize: 11, fontFamily: 'ui-monospace, monospace',
                  borderBottom: '1px solid var(--bg2)',
                  alignItems: 'baseline',
                  background: isPivot ? 'rgba(250,204,21,0.10)' : 'transparent',
                  borderLeft: isPivot ? '3px solid var(--warn, #facc15)' : '3px solid transparent',
                }}>
                  <span style={{ color: 'var(--text3)', textAlign: 'right' }}>
                    {offsetMs >= 0 ? `+${offsetMs.toFixed(0)}ms` : `${offsetMs.toFixed(0)}ms`}
                  </span>
                  <span className={sevClass(sev)} style={{ fontWeight: 600 }}>
                    {sev.slice(0, 4) || '—'}
                  </span>
                  <span style={{ color: 'var(--text2)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={l.serviceName}>
                    {l.serviceName}
                  </span>
                  <span style={{
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={l.body}>
                    {l.body}
                  </span>
                  <span style={{ textAlign: 'right' }}>
                    {l.traceId && onTracePeek && (
                      <button type="button"
                        onClick={() => onTracePeek(l.traceId)}
                        title={`Peek trace ${l.traceId.slice(0, 12)}…`}
                        style={{
                          all: 'unset', cursor: 'pointer',
                          padding: '0 4px', color: 'var(--accent2)',
                          fontSize: 11,
                        }}>👁</button>
                    )}
                  </span>
                </div>
              );
            })}
          </div>
        </>
      )}
    </Modal>
  );
}

function sevClass(s: string): string {
  switch (s) {
    case 'FATAL':
    case 'ERROR':   return 'sev-err';
    case 'WARN':
    case 'WARNING': return 'sev-warn';
    case 'INFO':    return 'sev-info';
    default:        return 'sev-dim';
  }
}
