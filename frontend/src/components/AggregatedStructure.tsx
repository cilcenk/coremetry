import { useMemo, useState } from 'react';
import { hashColor, fmtNum } from '@/lib/utils';
import type { AggSpanNode } from '@/lib/types';

// Grafana-Drilldown-style multi-trace path-aggregated tree. Each
// row is a unique `(parent_path → service → operation)` triple
// observed across the sampled traces. Bars are proportional to the
// average duration; an `×N` badge always shows the number of real
// spans collapsed into the row, so a tight loop or fan-out reads as
// one line carrying its multiplier.
//
// We deliberately do not reuse TraceWaterfall here — semantics are
// different. There are no real timestamps, no error dots per
// span, no resizable name column; bars are positioned using
// `avgStartMs` (mean trace-relative offset) and width by `avgMs`.

type Props = {
  roots: AggSpanNode[];
  // Tree-wide reference for bar proportions. Caller computes once at
  // the top level so every row scales against the same axis.
  totalMs?: number;
};

const INDENT_PX = 16;
const ROW_HEIGHT = 26;
const NAME_COL_WIDTH = 460;

export function AggregatedStructure({ roots, totalMs }: Props) {
  // Tree-wide reference: the largest observed (avgStartMs + avgMs)
  // across every node. Computing once, against avg+avg, keeps the
  // critical-path bar stretching to the right edge while every other
  // row stays in proportion.
  const refMs = useMemo(() => {
    if (totalMs && totalMs > 0) return totalMs;
    let m = 0;
    const walk = (n: AggSpanNode) => {
      const end = (n.avgStartMs || 0) + (n.avgMs || 0);
      if (end > m) m = end;
      n.children?.forEach(walk);
    };
    roots.forEach(walk);
    return m || 1;
  }, [roots, totalMs]);

  return (
    <div id="wf-outer">
      <div className="wf-header">
        <div className="wf-col-name" style={{ width: NAME_COL_WIDTH }}>Span (aggregated)</div>
        <div className="wf-col-bar">
          {[0, 0.25, 0.5, 0.75, 1].map(t => (
            <span key={`l${t}`}>
              <span className="wf-tick-label" style={{ left: `${t * 100}%` }}>
                {(t * refMs).toFixed(t === 0 ? 0 : 1)}ms
              </span>
              {t > 0 && <div className="wf-vline" style={{ left: `${t * 100}%` }} />}
            </span>
          ))}
        </div>
      </div>
      <div>
        {roots.map((r, i) => (
          <AggRow
            key={`root-${i}-${r.service}-${r.operation}`}
            node={r}
            depth={0}
            ancestorContinues={[]}
            isLastSibling={i === roots.length - 1}
            refMs={refMs}
            colWidth={NAME_COL_WIDTH}
          />
        ))}
      </div>
    </div>
  );
}

function AggRow({ node, depth, ancestorContinues, isLastSibling, refMs, colWidth }: {
  node: AggSpanNode;
  depth: number;
  ancestorContinues: boolean[];
  isLastSibling: boolean;
  refMs: number;
  colWidth: number;
}) {
  const [open, setOpen] = useState(true);
  const hasChildren = !!node.children && node.children.length > 0;
  const isErr = node.errorCount > 0;
  // v0.8.302 — theme token instead of dark-palette literal.
  const color = isErr ? 'var(--err)' : hashColor(node.service);

  const startPct = Math.max(0, Math.min(100, (node.avgStartMs / refMs) * 100));
  const widthPct = Math.max(0.5, Math.min(100 - startPct, (node.avgMs / refMs) * 100));
  const labelInside = widthPct > 18;

  const tip =
    `${node.operation}\n` +
    `service: ${node.service}\n` +
    `count: ×${node.count}\n` +
    `avg:   ${node.avgMs.toFixed(2)}ms\n` +
    `max:   ${node.maxMs.toFixed(2)}ms` +
    (node.errorCount > 0 ? `\nerrors: ${node.errorCount} (${((node.errorCount / node.count) * 100).toFixed(0)}%)` : '');

  return (
    <>
      <div className={`wf-row${isErr ? ' wf-err' : ''}`} style={{ minHeight: ROW_HEIGHT }}>
        <div className="wf-row-name" style={{ width: colWidth }}>
          <div className="wf-stripe" style={{ background: color }} />

          {/* Tree guides — same conventions as TraceWaterfall so the
              two views read consistently. */}
          {ancestorContinues.map((cont, i) => (
            <span key={i} className={`wf-tree-v${cont ? '' : ' wf-tree-v-empty'}`}
                  style={{ left: i * INDENT_PX + 4 }} />
          ))}
          {depth > 0 && (
            <span className={`wf-tree-elbow${isLastSibling ? ' wf-tree-elbow-last' : ''}`}
                  style={{ left: (depth - 1) * INDENT_PX + 4 }} />
          )}

          <div className="wf-row-name-inner" style={{ paddingLeft: depth * INDENT_PX + 8 }}>
            {hasChildren
              ? <button className="wf-toggle" onClick={() => setOpen(o => !o)}
                        aria-label={open ? 'Collapse' : 'Expand'}
                        title={open ? 'Collapse' : 'Expand'}>
                  {open ? '▼' : '▶'}
                </button>
              : <div className="wf-leaf" />}
            <span className="wf-svc"
                  title={`service.name: ${node.service}`}
                  style={{ borderBottomColor: color }}>
              {node.service}
            </span>
            <span className="wf-name" title={node.operation}>
              {node.operation || '(unnamed)'}
            </span>
            {node.count > 1 && (
              <span className="wf-group" title={tip}>×{fmtNum(node.count)}</span>
            )}
            {node.errorCount > 0 && (
              <span className="wf-err-dot" title={`${node.errorCount} error(s)`}>●</span>
            )}
          </div>
        </div>

        <div className="wf-resizer-row" />

        <div className="wf-row-bar">
          {[0.25, 0.5, 0.75, 1].map(t => (
            <div key={`v${t}`} className="wf-vline" style={{ left: `${t * 100}%` }} />
          ))}
          <div
            className="wf-bar"
            title={tip}
            style={{ left: `${startPct}%`, width: `${widthPct}%`, background: color }}
          >
            {labelInside && (
              <span className="wf-bar-label">{node.avgMs.toFixed(2)}ms</span>
            )}
          </div>
          {!labelInside && (
            <span className="wf-bar-label-outside"
                  style={{ left: `calc(${startPct}% + ${widthPct}% + 4px)` }}>
              {node.avgMs.toFixed(2)}ms
            </span>
          )}
        </div>
      </div>

      {open && hasChildren && node.children!.map((c, i) => (
        <AggRow
          key={`${depth + 1}-${i}-${c.service}-${c.operation}`}
          node={c}
          depth={depth + 1}
          ancestorContinues={[...ancestorContinues, !isLastSibling]}
          isLastSibling={i === node.children!.length - 1}
          refMs={refMs}
          colWidth={colWidth}
        />
      ))}
    </>
  );
}
