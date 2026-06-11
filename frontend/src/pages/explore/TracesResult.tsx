import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Spinner, Empty } from '@/components/Spinner';
import { ColumnManager } from '@/components/ColumnManager';
import { fmtNum, tsLong, rowClickHandlers } from '@/lib/utils';
import type { TraceRow } from '@/lib/types';
import { TRACE_SORT_NATURAL, type TraceSortKey } from './presets';

// TracesResult — the Explore "Traces" result-mode table (the block
// that renders BELOW the query console, in the right column).
//
// Phase-1 extraction (explore-v2): moved verbatim out of Explore.tsx.
// The table is client-sorted (page is bounded — default 50, max 500,
// so no server round-trip per header click) and supports the same
// attribute-column manager as /traces (?cols=key1,key2).
// State ownership is unchanged:
//   • traces / traceTotal / traceLimit / extraCols stay in the parent
//     (Explore.tsx) — they ride the URL-write + fetch effects, and the
//     limit picker + "showing N of M" footer live in the console card.
//   • The transient sort column/direction is table-local, so it lives
//     here (it was never URL-persisted).
// Zero behaviour diff vs the inline version.
export function TracesResult({
  traces,
  traceTotal,
  extraCols,
  setExtraCols,
}: {
  traces: TraceRow[] | null | undefined;
  traceTotal: number;
  extraCols: string[];
  setExtraCols: (cols: string[]) => void;
}) {
  const navigate = useNavigate();

  // Client-side sort for the traces result table — page-size is small
  // (default 50, max 500) so we don't need a server roundtrip per click.
  const [traceSort, setTraceSort] = useState<TraceSortKey>('time');
  const [traceSortDir, setTraceSortDir] = useState<'asc' | 'desc'>('desc');

  // Sorted view of the trace results — pure client-side because the
  // page is bounded (default 50, hard max 500). Avoids a server
  // round-trip per header click.
  const sortedTraces = useMemo(() => {
    if (!traces) return traces;
    const cmp = (a: TraceRow, b: TraceRow): number => {
      switch (traceSort) {
        case 'traceId':     return a.traceId.localeCompare(b.traceId);
        case 'rootName':    return (a.rootName || '').localeCompare(b.rootName || '');
        case 'serviceName': return a.serviceName.localeCompare(b.serviceName);
        case 'duration':    return a.durationMs - b.durationMs;
        case 'spans':       return a.spanCount - b.spanCount;
        case 'time':        return a.startTime - b.startTime;
        case 'status':      return Number(a.hasError) - Number(b.hasError);
      }
    };
    const arr = [...traces].sort(cmp);
    return traceSortDir === 'desc' ? arr.reverse() : arr;
  }, [traces, traceSort, traceSortDir]);

  const toggleTraceSort = (col: TraceSortKey) => {
    if (traceSort === col) setTraceSortDir(d => d === 'desc' ? 'asc' : 'desc');
    else { setTraceSort(col); setTraceSortDir(TRACE_SORT_NATURAL[col]); }
  };

  return (
    <>
      {traces === undefined && <Spinner />}
      {traces && traces.length === 0 && (
        <Empty icon="⋮" title="No matching traces">
          Loosen your filters or widen the time range.
        </Empty>
      )}
      {traces && traces.length > 0 && (
        <>
          <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 8 }}>
            Showing <b style={{ color: 'var(--accent2)' }}>{traces.length}</b> of {fmtNum(traceTotal)} traces
            {traces.length < traceTotal && <> · raise the limit to see more</>}
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <TraceSortTh col="traceId"     label="Trace ID"  sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                  <TraceSortTh col="rootName"    label="Root"      sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                  <TraceSortTh col="serviceName" label="Service"   sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                  <TraceSortTh col="duration"    label="Duration"  sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} align="right" />
                  <TraceSortTh col="spans"       label="Spans"     sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} align="right" />
                  <TraceSortTh col="time"        label="Started"   sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                  <TraceSortTh col="status"      label="Status"    sort={traceSort} dir={traceSortDir} onSort={toggleTraceSort} />
                  {/* Same column-manager UX as /traces — adds
                      attribute columns to the result table. */}
                  {extraCols.map(k => (
                    <th key={k} style={{ position: 'relative', whiteSpace: 'nowrap' }}>
                      <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>{k}</span>
                      <button type="button" title="Remove column"
                        onClick={() => setExtraCols(extraCols.filter(c => c !== k))}
                        style={{
                          marginLeft: 6, padding: '0 4px', fontSize: 10, lineHeight: 1,
                          background: 'transparent', border: 'none', color: 'var(--text3)',
                          cursor: 'pointer',
                        }}>×</button>
                    </th>
                  ))}
                  <th style={{ width: 1, whiteSpace: 'nowrap' }}>
                    <ColumnManager
                      cols={extraCols}
                      onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
                  </th>
                </tr>
              </thead>
              <tbody>
                {(sortedTraces ?? []).map(t => (
                  <tr key={t.traceId}
                      {...rowClickHandlers(`/trace?id=${t.traceId}`,
                                           () => navigate(`/trace?id=${t.traceId}`))}
                      style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 34px' }}>
                    <td className="mono">
                      <Link to={`/trace?id=${t.traceId}`}
                            onClick={e => e.stopPropagation()}
                            style={{ fontSize: 11 }}>
                        {t.traceId.slice(0, 12)}…
                      </Link>
                    </td>
                    <td><b>{t.rootName}</b></td>
                    <td className="mono" style={{ fontSize: 12 }}>{t.serviceName}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {t.durationMs.toFixed(1)}ms
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(t.spanCount)}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{tsLong(t.startTime)}</td>
                    <td>
                      {t.hasError
                        ? <span className="badge b-err">ERROR</span>
                        : <span className="badge b-ok">OK</span>}
                    </td>
                    {extraCols.map(k => {
                      const v = t.extras?.[k] ?? '';
                      return (
                        <td key={k} className="mono" style={{ fontSize: 11, color: v ? 'var(--text2)' : 'var(--text3)', whiteSpace: 'nowrap', maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis' }} title={v || ''}>
                          {v || '—'}
                        </td>
                      );
                    })}
                    <td />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}

// Sortable header for the traces result table. Reuses the same .sortable
// CSS class as the /traces and /services tables for visual consistency.
function TraceSortTh({ col, label, sort, dir, onSort, align }: {
  col: TraceSortKey; label: string;
  sort: TraceSortKey; dir: 'asc' | 'desc';
  onSort: (c: TraceSortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
}
