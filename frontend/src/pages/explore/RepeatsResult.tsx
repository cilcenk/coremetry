import { useMemo } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { fmtNum, tsLong } from '@/lib/utils';
import type { RepeatedSpanRow } from '@/lib/types';
import { repeatCols } from './presets';

// RepeatsResult — the Explore "Repeats" result-mode table (N+1 /
// fan-out finder; the block that renders BELOW the query console).
//
// Phase-1 extraction (explore-v2): moved verbatim out of Explore.tsx.
// Adopts the shared sortable+resizable primitive (useDataTable) with
// the same storageKey ('explore-repeats') and the same initial sort
// (count desc — the backend returns heaviest-first, so this preserves
// that paint). The "Repeated shape" column label tracks the active
// split-by, so columns are memoised on groupBy.
// State ownership is unchanged:
//   • repeats / repeatMin / groupBy stay in the parent (Explore.tsx) —
//     they ride the fetch + URL-write effects, and the preset chips +
//     Min-repeats picker live in the console card.
// Zero behaviour diff vs the inline version.
export function RepeatsResult({
  repeats,
  repeatMin,
  groupBy,
}: {
  repeats: RepeatedSpanRow[] | null | undefined;
  repeatMin: number;
  groupBy: string[];
}) {
  const navigate = useNavigate();

  // Repeats (N+1 / fan-out) table. The "Repeated shape" column
  // label tracks the active split-by, so columns are memoised on
  // groupBy. Backend returns heaviest-first; initial sort = count
  // desc preserves that paint.
  const cols = useMemo(() => repeatCols(groupBy), [groupBy]);
  const repeatsDt = useDataTable<RepeatedSpanRow>({
    storageKey: 'explore-repeats',
    columns: cols,
    rows: repeats ?? [],
    initialSort: { id: 'count', dir: 'desc' },
  });

  return (
    <>
      {repeats === undefined && <Spinner />}
      {repeats && repeats.length === 0 && (
        <Empty icon="⟳" title="No repeated span shapes found">
          No trace has the same (group-by) shape repeating ≥ {repeatMin} times in this window.
          Try lowering the threshold or switching the split-by (e.g. <code>name</code> + <code>peer.service</code> for chatty RPC, <code>http.route</code> for endpoint fan-out).
        </Empty>
      )}
      {repeats && repeats.length > 0 && (
        <>
          <div style={{ marginBottom: 6, fontSize: 12, color: 'var(--text2)' }}>
            {repeats.length} trace{repeats.length === 1 ? '' : 's'} with ≥ {repeatMin} repeats of the same span shape — heaviest at the top.
          </div>
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={repeatsDt} />
              <DataTableHead dt={repeatsDt} />
              <tbody>
                {repeatsDt.sortedRows.map((r, i) => (
                  <tr key={`${r.traceId}|${i}`}
                      onClick={() => navigate(`/trace?id=${r.traceId}`)}
                      style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 34px' }}>
                    <td>
                      <Link to={`/trace?id=${r.traceId}`}
                            onClick={e => e.stopPropagation()}
                            style={{ fontFamily: 'monospace', fontSize: 11 }}>
                        {r.traceId.slice(0, 12)}…
                      </Link>
                    </td>
                    <td style={{ fontSize: 12 }}>
                      <span style={{ fontWeight: 600 }}>{r.service || '—'}</span>
                      {r.rootName && (
                        <span style={{ color: 'var(--text3)' }}> · {r.rootName}</span>
                      )}
                    </td>
                    <td style={{
                      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      fontSize: 11, color: 'var(--text2)',
                    }} title={(r.groupValues ?? []).join(' · ')}>
                      {(r.groupValues ?? []).filter(Boolean).join(' · ') ||
                        <span style={{ color: 'var(--text3)' }}>(empty)</span>}
                    </td>
                    <td className="num mono" style={{ fontWeight: 700,
                      color: r.count >= 50 ? 'var(--err)' : r.count >= 20 ? 'var(--warn)' : 'var(--text)' }}>
                      {fmtNum(r.count)}
                    </td>
                    <td className="num mono">{r.totalDurationMs.toFixed(1)}ms</td>
                    <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {tsLong(r.startedAt)}
                    </td>
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
