import { useState } from 'react';
import { Link } from 'react-router-dom';
import { CopyButton } from './CopyButton';
import { tsShort, sevName, sevClass } from '@/lib/utils';
import type { LogRow } from '@/lib/types';

// Middle-truncate so long pod names like `payment-api-7d6f9b54c5-xkv2m`
// stay scannable in a column (keeps the deployment prefix + the
// random suffix, drops the middle hash).
// KvRow renders one (key, value) row in the expanded log's
// attribute table with Kibana-style "click to filter" affordances
// — small ⊕ + ⊖ icons that show on hover. ⊕ adds `key:value`
// to the parent's filter, ⊖ adds `NOT key:value`. The callbacks
// are optional so surfaces without a filter state (trace detail
// Logs tab) silently omit the buttons.
function KvRow({ k, v, onAdd, onExclude }: {
  k: string; v: string;
  onAdd?: (key: string, value: string) => void;
  onExclude?: (key: string, value: string) => void;
}) {
  // The buttons only render at all when a callback was provided.
  // CSS hover state (kv-actions visible only on tr:hover) keeps
  // the table tidy when the operator is just reading.
  const canFilter = !!(onAdd || onExclude);
  return (
    <tr className={canFilter ? 'kv-filterable' : ''}>
      <td title={k}>{k}</td>
      <td>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <span>{v}</span>
          {canFilter && (
            <span className="kv-actions" style={{
              display: 'inline-flex', gap: 2,
            }}>
              {onAdd && (
                <button type="button"
                  onClick={(e) => { e.stopPropagation(); onAdd(k, v); }}
                  title={`Filter for ${k}: ${v}`}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    padding: '0 5px', borderRadius: 3,
                    fontSize: 11, lineHeight: '14px',
                    color: 'var(--accent2)',
                    background: 'rgba(56,139,253,0.10)',
                    border: '1px solid rgba(56,139,253,0.30)',
                  }}>⊕</button>
              )}
              {onExclude && (
                <button type="button"
                  onClick={(e) => { e.stopPropagation(); onExclude(k, v); }}
                  title={`Filter out ${k}: ${v}`}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    padding: '0 5px', borderRadius: 3,
                    fontSize: 11, lineHeight: '14px',
                    color: 'var(--err)',
                    background: 'rgba(239,68,68,0.10)',
                    border: '1px solid rgba(239,68,68,0.30)',
                  }}>⊖</button>
              )}
            </span>
          )}
        </span>
      </td>
    </tr>
  );
}

function truncMid(s: string, max: number): string {
  if (s.length <= max) return s;
  const half = Math.floor((max - 1) / 2);
  return s.slice(0, half) + '…' + s.slice(s.length - half);
}

// firstNonEmpty picks the first argument that is a non-empty
// string. Stricter than ??-chains because some shippers emit
// "" / null for canonical OTel attrs while the snake_case
// alternative carries the real value; we want to keep walking
// the chain past those.
function firstNonEmpty(...vals: Array<string | undefined | null>): string {
  for (const v of vals) {
    if (typeof v === 'string' && v.length > 0) return v;
  }
  return '';
}

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
  onFilterAdd,
  onFilterExclude,
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
  // Kibana-style "click any field value to filter" (v0.5.229).
  // When set, the expanded row's kv-table renders per-field
  // ⊕ / ⊖ buttons that fold the key:value into the parent's
  // filter state. Omitted on surfaces where the parent has no
  // filter to mutate (e.g. trace detail Logs tab).
  onFilterAdd?: (key: string, value: string) => void;
  onFilterExclude?: (key: string, value: string) => void;
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
  // Pod + Cluster columns show on both surfaces. v0.5.212 split
  // them out from the hideTraceColumn flag (which still gates the
  // Trace deep-link column) so the trace detail Logs tab also
  // shows where each log came from — operators were expanding
  // every row just to read the resource attrs.
  const cols = hideTraceColumn ? 6 : 7;
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Sev</th>
            <th>Service</th>
            <th>Pod</th>
            <th>Cluster</th>
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
                onFilterAdd={onFilterAdd}
                onFilterExclude={onFilterExclude}
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
  onFilterAdd, onFilterExclude,
}: {
  l: LogRow;
  idx: number;
  cols: number;
  hideTraceColumn: boolean;
  selected: boolean;
  expanded: boolean;
  onClick: () => void;
  extraExpanded?: (l: LogRow) => React.ReactNode;
  onFilterAdd?: (key: string, value: string) => void;
  onFilterExclude?: (key: string, value: string) => void;
}) {
  const attrs = Object.entries(l.attributes ?? {});
  const res = Object.entries(l.resourceAttributes ?? {});
  // k8s pod + cluster columns. v0.5.224 promoted the operator's
  // actual fields to the front of each chain (kubernetes.pod_name,
  // openshift.labels.cluster) — earlier order had k8s.pod.name
  // first which short-circuited at "" on pipelines that emit
  // BOTH an empty canonical OTel attr AND the real snake_case
  // one. firstNonEmpty also treats "" as missing so an
  // empty-but-present attr doesn't block the fallback.
  const ra = l.resourceAttributes ?? {};
  const pod = firstNonEmpty(
    ra['kubernetes.pod_name'],
    ra['k8s.pod.name'],
    ra['kubernetes.pod.name'],
    ra['pod_name'],
  );
  const cluster = firstNonEmpty(
    ra['openshift.labels.cluster'],
    ra['openshift.cluster.name'],
    ra['k8s.cluster.name'],
    ra['kubernetes.cluster_name'],
  );
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
        <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}
            title={pod || 'no k8s.pod.name / kubernetes.pod_name resource attr'}>
          {pod ? truncMid(pod, 22) : '—'}
        </td>
        <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}
            title={cluster || 'no openshift.labels.cluster / openshift.cluster.name / k8s.cluster.name resource attr'}>
          {cluster || '—'}
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
                  <KvRow key={k} k={k} v={String(v)}
                    onAdd={onFilterAdd} onExclude={onFilterExclude} />
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
                    <KvRow key={k} k={k} v={String(v)}
                      onAdd={onFilterAdd} onExclude={onFilterExclude} />
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
