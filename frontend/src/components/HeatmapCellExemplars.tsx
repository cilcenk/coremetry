import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Spinner } from './Spinner';
import { api } from '@/lib/api';
import { fmtSmart } from '@/lib/chartFmt';
import { tsLong } from '@/lib/utils';
import type { TraceRow, FilterExpr } from '@/lib/types';

// HeatmapCellExemplars — v0.5.260. Honeycomb-classic "click the
// slow band → see what traces ran there" workflow. Modal opens
// when the operator clicks a non-empty cell on LatencyHeatmap;
// shows up to 20 traces matching the cell's (time bucket,
// latency band) under the same filter the heatmap was rendered
// with, so the result is exactly the cohort the operator wanted
// to drill into.
//
// Why a modal vs. inline expansion: the heatmap is dense (60×28
// cells); inline would push the rest of the page around with
// every click. Modal stays out of the way + is closable with
// Esc / backdrop click.

export interface HeatmapCellRef {
  timeNs:    number;  // bucket centre time
  lowDurMs:  number;  // duration band lower bound (exclusive)
  highDurMs: number;  // duration band upper bound (inclusive)
  count:     number;  // for the modal header
}

interface Props {
  cell: HeatmapCellRef;
  // Half-width of one heatmap time bucket in ns. The cell's
  // [time-half, time+half] window is the trace lookup range.
  bucketWidthNs: number;
  // Same filter set the heatmap was rendered with — applied to
  // the trace lookup so the modal stays consistent with the
  // surface the operator clicked through from.
  filters: FilterExpr[];
  // Optional advanced-DSL string when the operator is in DSL
  // mode. Passed through verbatim; backend AND-joins with
  // the FilterExpr[].
  dsl?: string;
  onClose: () => void;
}

export function HeatmapCellExemplars({ cell, bucketWidthNs, filters, dsl, onClose }: Props) {
  const [traces, setTraces] = useState<TraceRow[] | null | undefined>(undefined);

  useEffect(() => {
    // Esc closes the modal — standard chrome on every modal in
    // this app.
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  useEffect(() => {
    const half = Math.max(bucketWidthNs / 2, 30 * 1e9); // 30s floor
    const from = cell.timeNs - half;
    const to = cell.timeNs + half;
    setTraces(undefined);
    api.traces({
      from, to,
      // The heatmap bins are upper-bound; the cell's actual
      // duration window is (low, high]. minMs is inclusive on the
      // backend, so we add 0.001 to the low bound to keep the
      // semantics tight.
      minMs: cell.lowDurMs > 0 ? cell.lowDurMs + 0.001 : 0,
      maxMs: cell.highDurMs,
      filters: filters.length > 0 ? JSON.stringify(filters) : undefined,
      dsl: dsl,
      sort: 'duration',
      order: 'desc',
      limit: 20,
    })
      .then(r => setTraces(r.traces ?? []))
      .catch(() => setTraces(null));
  }, [cell.timeNs, cell.lowDurMs, cell.highDurMs, bucketWidthNs, filters, dsl]);

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
      display: 'grid', placeItems: 'center', zIndex: 200,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 720, maxHeight: '80vh', overflow: 'auto',
        padding: 20, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 4 }}>
          <span style={{ fontSize: 15, fontWeight: 700 }}>
            Trace exemplars
          </span>
          <span style={{ fontSize: 12, color: 'var(--text3)', flex: 1 }}>
            {fmtSmart(cell.lowDurMs, 'ms')} – {fmtSmart(cell.highDurMs, 'ms')} ·{' '}
            {new Date(cell.timeNs / 1e6).toLocaleTimeString()} ·{' '}
            {cell.count.toLocaleString()} spans in cell
          </span>
          <button className="sec" onClick={onClose} style={{ fontSize: 12 }}>✕</button>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
          Top 20 traces ordered by duration desc, applying the heatmap's
          current filter set. Click a row to open the waterfall.
        </div>

        {traces === undefined && <Spinner />}
        {traces === null && (
          <div style={{ color: 'var(--err)', fontSize: 12 }}>
            Failed to load traces — try widening filters or check the time range.
          </div>
        )}
        {traces && traces.length === 0 && (
          <div style={{ color: 'var(--text3)', fontSize: 12, padding: '12px 0' }}>
            No traces matched. The heatmap's count reflects raw spans; this lookup
            joins by trace id. If the heatmap is sampled, exemplars may be missing
            (the cell still represents real spans, just not all reachable as
            trace rows here).
          </div>
        )}
        {traces && traces.length > 0 && (
          <table style={{ width: '100%', fontSize: 12 }}>
            <thead>
              <tr>
                <th style={{ textAlign: 'left' }}>Trace</th>
                <th style={{ textAlign: 'left' }}>Service</th>
                <th style={{ textAlign: 'right' }}>Duration</th>
                <th style={{ textAlign: 'right' }}>Spans</th>
                <th>Started</th>
                <th style={{ width: 24 }}></th>
              </tr>
            </thead>
            <tbody>
              {traces.map(t => (
                <tr key={t.traceId}>
                  <td>
                    <Link to={`/trace?id=${t.traceId}`}
                      onClick={onClose}
                      className="mono"
                      style={{ color: 'var(--accent2)', textDecoration: 'none' }}>
                      {t.traceId.slice(0, 16)}…
                    </Link>
                  </td>
                  <td>
                    {t.serviceName || <span style={{ color: 'var(--text3)' }}>—</span>}
                    {t.rootName && (
                      <span style={{ color: 'var(--text3)', marginLeft: 6, fontSize: 11 }}>
                        / {t.rootName}
                      </span>
                    )}
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    {fmtSmart(t.durationMs ?? 0, 'ms')}
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    {(t.spanCount ?? 0).toLocaleString()}
                  </td>
                  <td className="mono" style={{ color: 'var(--text3)', fontSize: 11 }}>
                    {tsLong(t.startTime)}
                  </td>
                  <td>
                    {t.hasError && (
                      <span title="root span errored"
                        style={{ color: 'var(--err)', fontWeight: 700 }}>!</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
