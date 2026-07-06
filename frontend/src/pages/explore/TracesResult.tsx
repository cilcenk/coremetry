import { useMemo } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Spinner, Empty } from '@/components/Spinner';
import { ColumnManager } from '@/components/ColumnManager';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import { fmtNum, tsLong, rowClickHandlers } from '@/lib/utils';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TraceRow } from '@/lib/types';

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
//   • Sort lives in the shared useDataTable primitive (v0.8.306 —
//     replaces the hand-rolled TraceSortTh/toggleTraceSort pair).
//     Still CLIENT sort: the header never drove a server re-fetch, so
//     client mode preserves the old behaviour exactly; accessors
//     mirror the old cmp() switch, natural directions the old
//     TRACE_SORT_NATURAL map.

// Fixed columns for the primitive. Attribute columns are appended
// per-render (resize-only, no sortValue — the backend doesn't order
// by a projected attribute; same rule as /traces), and the
// "+ Add column" manager rides the trailing <th> slot so its
// dropdown isn't clipped by the managed headers' overflow:hidden.
const TRACE_BASE_COLS: DataTableColumn<TraceRow>[] = [
  { id: 'traceId',     label: 'Trace ID', sortValue: t => t.traceId,          naturalDir: 'asc', width: 130 },
  { id: 'rootName',    label: 'Root',     sortValue: t => t.rootName || '',   naturalDir: 'asc', width: 240 },
  { id: 'serviceName', label: 'Service',  sortValue: t => t.serviceName,      naturalDir: 'asc', width: 170 },
  { id: 'duration',    label: 'Duration', sortValue: t => t.durationMs,       numeric: true, width: 100 },
  { id: 'spans',       label: 'Spans',    sortValue: t => t.spanCount,        numeric: true, width: 80 },
  { id: 'time',        label: 'Started',  sortValue: t => t.startTime,        width: 170 },
  { id: 'status',      label: 'Status',   sortValue: t => Number(t.hasError), width: 90 },
];

// Attribute-column ids are prefixed so a custom attribute named like a
// fixed column (e.g. "status") can't collide with its id in the sort
// param / persisted widths.
const ATTR_PREFIX = 'attr:';
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

  const columns = useMemo<DataTableColumn<TraceRow>[]>(() => [
    ...TRACE_BASE_COLS,
    ...extraCols.map(k => ({ id: ATTR_PREFIX + k, label: k, width: 180 })),
  ], [extraCols]);

  const dt = useDataTable<TraceRow>({
    storageKey: 'explore-traces-result',
    columns,
    rows: traces ?? [],
    initialSort: { id: 'time', dir: 'desc' },
  });

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
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} trailing={[120]} />
              {/* Same column-manager UX as /traces — attribute columns
                  carry a hover-× remove affordance via renderLabel, and
                  the "+ Add column" manager keeps its own trailing <th>. */}
              <DataTableHead dt={dt}
                renderLabel={c => c.id.startsWith(ATTR_PREFIX)
                  ? <>
                      <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 11 }}>{c.label}</span>
                      <button type="button" title="Remove column"
                        onClick={e => { e.stopPropagation(); setExtraCols(extraCols.filter(x => x !== c.label)); }}
                        style={{
                          marginLeft: 6, padding: '0 4px', fontSize: 10, lineHeight: 1,
                          background: 'transparent', border: 'none', color: 'var(--text3)',
                          cursor: 'pointer',
                        }}>×</button>
                    </>
                  : c.label}
                trailing={
                  <th style={{ whiteSpace: 'nowrap' }}>
                    <ColumnManager
                      cols={extraCols}
                      onAdd={k => { if (!extraCols.includes(k) && extraCols.length < 8) setExtraCols([...extraCols, k]); }} />
                  </th>
                } />
              <tbody>
                {dt.sortedRows.map(t => (
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
