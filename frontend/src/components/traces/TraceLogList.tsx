// TraceLogList.tsx — the chronological log-line list, extracted from
// TracePeekDrawer (v0.5.398) so BOTH the trace-peek drawer and the new
// CorrelationContextDrawer (task #6) render the SAME severity-coloured log rows
// without duplicating the layout. Each row shows an offset-from-trace-start (or
// from a supplied anchor instant), the severity tag, the emitting service, and
// the body.

import type { LogRow } from '@/lib/types';

// sevClass — map an OTel severity text to the shared sev-* token class
// (globals.css). Exported so callers (e.g. a severity-bucket header) can reuse
// the SAME mapping the rows use.
export function sevClass(s: string): string {
  switch ((s || '').toUpperCase()) {
    case 'FATAL':
    case 'ERROR':
      return 'sev-err';
    case 'WARN':
    case 'WARNING':
      return 'sev-warn';
    case 'INFO':
      return 'sev-info';
    default:
      return 'sev-dim';
  }
}

// TraceLogList renders the log rows chronologically. `offsetFromNs` anchors the
// "+Nms" column: when supplied (the trace's min start), every row reads as an
// offset from the trace start — matching TracePeekDrawer's behaviour. When
// omitted the offset column is hidden (a pure chronological list, e.g. a
// service+window fuzzy join where "offset from start" is meaningless).
export function TraceLogList({
  logs,
  offsetFromNs,
  maxHeight = 320,
}: {
  logs: LogRow[];
  offsetFromNs?: number;
  maxHeight?: number;
}) {
  const showOffset = offsetFromNs != null;
  const cols = showOffset ? '60px 50px 110px 1fr' : '50px 110px 1fr';
  return (
    <div style={{ maxHeight, overflowY: 'auto', padding: 4 }}>
      {logs
        .slice()
        .sort((a, b) => a.timestamp - b.timestamp)
        .map((l) => {
          const offsetMs = showOffset ? (l.timestamp - (offsetFromNs as number)) / 1e6 : 0;
          return (
            <div
              key={l.id}
              style={{
                display: 'grid',
                gridTemplateColumns: cols,
                gap: 6,
                padding: '2px 6px',
                fontSize: 11,
                fontFamily: 'ui-monospace, monospace',
                borderBottom: '1px solid var(--bg2)',
                alignItems: 'baseline',
              }}>
              {showOffset && (
                <span style={{ color: 'var(--text3)', textAlign: 'right' }}>
                  {offsetMs >= 0 ? `+${offsetMs.toFixed(0)}ms` : `${offsetMs.toFixed(0)}ms`}
                </span>
              )}
              <span className={sevClass(l.severityText)} style={{ fontWeight: 600 }}>
                {(l.severityText || '').toUpperCase().slice(0, 4) || '—'}
              </span>
              <span
                style={{ color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                title={l.serviceName}>
                {l.serviceName}
              </span>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={l.body}>
                {l.body}
              </span>
            </div>
          );
        })}
    </div>
  );
}

// severityBuckets — counts logs into the four sev tiers the header chips show.
// Pure helper so both drawers display the SAME bucket summary.
export function severityBuckets(logs: LogRow[]): { err: number; warn: number; info: number; other: number } {
  const b = { err: 0, warn: 0, info: 0, other: 0 };
  for (const l of logs) {
    switch (sevClass(l.severityText)) {
      case 'sev-err':
        b.err++;
        break;
      case 'sev-warn':
        b.warn++;
        break;
      case 'sev-info':
        b.info++;
        break;
      default:
        b.other++;
    }
  }
  return b;
}
