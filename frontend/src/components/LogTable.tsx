import { useState } from 'react';
import { Link } from 'react-router-dom';
import { CopyButton } from './CopyButton';
import { tsShort, sevName, sevClass } from '@/lib/utils';
import type { LogRow } from '@/lib/types';

// LogTable — the shared rendering for log lists used by:
//
//   • /logs (Logs.tsx) — full-feature table with the trace
//     column visible so an operator scanning logs can jump
//     to the originating trace in one click.
//   • /trace?id=… Logs tab (Trace.tsx) — same component with
//     hideTraceColumn=true, since every row in that view
//     belongs to the trace the operator is already on.
//
// Keeping these unified means severity colouring, expand-
// row anatomy (body + attributes + resource attrs),
// keyboard nav hookups, and any future log feature land
// consistently in both places — the operator's eye builds
// the same scan pattern across the app.
//
// `nav` is the optional useTableNav wiring from the parent.
// When omitted the rows lose keyboard selection but still
// click-to-expand. Trace detail Logs tab passes nothing;
// /logs passes its useTableNav handle.
//
// `extraExpanded` lets a parent render extra slots inside
// the expanded row (e.g. /logs uses it for the "View in
// trace" deep link). Trace detail tab leaves it default.
export function LogTable({
  logs,
  hideTraceColumn = false,
  nav,
  expandedIds,
  onToggleExpand,
  extraExpanded,
}: {
  logs: LogRow[];
  hideTraceColumn?: boolean;
  nav?: {
    selected: number;
    setSelected: (n: number) => void;
  };
  // Controlled mode: parent owns the expanded set + receives
  // a toggle callback. Used by /logs so the j/k useTableNav
  // hook's Enter handler can drive row expansion. Trace
  // detail Logs tab leaves these undefined and the local
  // state takes over.
  expandedIds?: Set<number>;
  onToggleExpand?: (id: number) => void;
  extraExpanded?: (l: LogRow) => React.ReactNode;
}) {
  const [localExpanded, setLocalExpanded] = useState<Set<number>>(new Set());
  const expanded = expandedIds ?? localExpanded;
  const toggle = (id: number) => {
    if (onToggleExpand) {
      onToggleExpand(id);
      return;
    }
    setLocalExpanded(prev => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  };
  const cols = hideTraceColumn ? 4 : 5;
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Sev</th>
            <th>Service</th>
            <th>Message</th>
            {!hideTraceColumn && <th>Trace</th>}
          </tr>
        </thead>
        <tbody>
          {logs.map((l, idx) => {
            const isExpanded = expanded.has(l.id);
            const isSelected = nav?.selected === idx;
            return (
              <LogRow
                key={l.id}
                l={l}
                idx={idx}
                cols={cols}
                hideTraceColumn={hideTraceColumn}
                selected={isSelected}
                expanded={isExpanded}
                onClick={() => {
                  nav?.setSelected(idx);
                  toggle(l.id);
                }}
                extraExpanded={extraExpanded}
              />
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function LogRow({
  l, idx, cols, hideTraceColumn, selected, expanded, onClick, extraExpanded,
}: {
  l: LogRow;
  idx: number;
  cols: number;
  hideTraceColumn: boolean;
  selected: boolean;
  expanded: boolean;
  onClick: () => void;
  extraExpanded?: (l: LogRow) => React.ReactNode;
}) {
  const attrs = Object.entries(l.attributes ?? {});
  const res = Object.entries(l.resourceAttributes ?? {});
  return (
    <>
      <tr onClick={onClick}
          data-row-idx={idx}
          className={selected ? 'row-selected' : ''}
          style={{ cursor: 'pointer' }}>
        <td className="mono">{tsShort(l.timestamp)}</td>
        <td>
          <span className={sevClass(l.severity)}>
            {l.severityText || sevName(l.severity)}
          </span>
        </td>
        <td>
          <span style={{
            fontSize: 11, padding: '1px 6px',
            background: 'var(--bg3)', borderRadius: 3,
            fontFamily: 'monospace',
          }}>
            {l.serviceName || '—'}
          </span>
        </td>
        <td style={{ maxWidth: 480 }} title={l.body}>{l.body}</td>
        {!hideTraceColumn && (
          <td className="mono">
            {l.traceId ? (
              <>
                <Link to={`/trace?id=${l.traceId}`} onClick={e => e.stopPropagation()}>
                  {l.traceId.slice(0, 8)}…
                </Link>
                <CopyButton value={l.traceId} title="Copy trace ID" />
              </>
            ) : '—'}
          </td>
        )}
      </tr>
      {expanded && (
        <tr>
          <td colSpan={cols} style={{ background: 'var(--bg0)', padding: '10px 20px' }}>
            <pre style={{
              fontSize: 12, whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere', color: 'var(--text)',
              marginBottom: attrs.length ? 8 : 0,
            }}>
              {l.body}
            </pre>
            {attrs.length > 0 && (
              <table className="kv-table"><tbody>
                {attrs.map(([k, v]) => (
                  <tr key={k}><td title={k}>{k}</td><td>{String(v)}</td></tr>
                ))}
              </tbody></table>
            )}
            {res.length > 0 && (
              <details style={{ marginTop: 6 }}>
                <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--text2)' }}>
                  Resource ({res.length})
                </summary>
                <table className="kv-table"><tbody>
                  {res.map(([k, v]) => (
                    <tr key={k}><td title={k}>{k}</td><td>{String(v)}</td></tr>
                  ))}
                </tbody></table>
              </details>
            )}
            {extraExpanded && extraExpanded(l)}
          </td>
        </tr>
      )}
    </>
  );
}
