// ShapesView.tsx — structural-shape clustering for traces (Honeycomb BubbleUp /
// Grafana trace-shapes equivalent).
//
// Groups a sample of traces by their (rootService · rootOperation) SHAPE
// signature and reports count / err / p50 / p95 / p99 per cohort, with an
// exemplar trace per group so the operator can drill straight in. This answers
// "what are the dominant call patterns and which shape is slow/failing?" at a
// glance — the long tail the raw list buries.
//
// We sample server-side (api.traces capped) then cluster + percentile on the
// client via the Phase-0 percentiles transform. The shared useDataTable
// primitive makes every column sortable + resizable; rows render through a
// plain <table> (the group count is small — bounded by the signature cardinality
// of the sample — so virtualisation isn't needed here).

import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { percentiles } from '@/lib/perf/transforms';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange, TraceRow } from '@/lib/types';
import { SvcBadge, fmtDur } from './shared';

interface ShapeRow {
  signature: string;
  service: string;
  operation: string;
  count: number;
  errors: number;
  errorRate: number;
  p50: number;
  p95: number;
  p99: number;
  exemplar: string;    // a trace id (prefers an errored exemplar)
}

const SAMPLE_LIMIT = 1000;

export function ShapesView({ range, service }: { range: TimeRange; service?: string }) {
  const navigate = useNavigate();
  const [rows, setRows] = useState<TraceRow[] | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    setRows(undefined);
    const { from, to } = timeRangeToNs(range);
    api.traces({ from, to, service: service || undefined, limit: SAMPLE_LIMIT, sort: 'time', order: 'desc' })
      .then(d => { if (!cancelled) setRows(d?.traces ?? []); })
      .catch(() => { if (!cancelled) setRows(null); });
    return () => { cancelled = true; };
    // timeRangeToNs(range) is read inside the effect body so `now()` only
    // ticks when range actually changes — no JSX/IIFE refetch trap.
  }, [range, service]);

  const shapes = useMemo<ShapeRow[]>(() => {
    if (!rows) return [];
    const groups = new Map<string, { svc: string; op: string; durs: number[]; errs: number; exErr?: string; exAny?: string }>();
    for (const t of rows) {
      const sig = `${t.serviceName}${t.rootName}`;
      let g = groups.get(sig);
      if (!g) { g = { svc: t.serviceName, op: t.rootName, durs: [], errs: 0, exAny: undefined, exErr: undefined }; groups.set(sig, g); }
      g.durs.push(t.durationMs);
      if (t.hasError) { g.errs++; if (!g.exErr) g.exErr = t.traceId; }
      if (!g.exAny) g.exAny = t.traceId;
    }
    const out: ShapeRow[] = [];
    for (const [sig, g] of groups) {
      const [p50, p95, p99] = percentiles(g.durs, [0.5, 0.95, 0.99]);
      out.push({
        signature: sig,
        service: g.svc,
        operation: g.op,
        count: g.durs.length,
        errors: g.errs,
        errorRate: g.durs.length ? (g.errs / g.durs.length) * 100 : 0,
        p50, p95, p99,
        exemplar: g.exErr ?? g.exAny ?? '',
      });
    }
    return out;
  }, [rows]);

  const COLS: DataTableColumn<ShapeRow>[] = useMemo(() => [
    { id: 'operation', label: 'Shape (service · operation)', sortValue: r => r.operation, naturalDir: 'asc', width: 320 },
    { id: 'count', label: 'Traces', sortValue: r => r.count, numeric: true, width: 90 },
    { id: 'errorRate', label: 'Error %', sortValue: r => r.errorRate, numeric: true, width: 90 },
    { id: 'p50', label: 'P50', sortValue: r => r.p50, numeric: true, width: 90 },
    { id: 'p95', label: 'P95', sortValue: r => r.p95, numeric: true, width: 90 },
    { id: 'p99', label: 'P99', sortValue: r => r.p99, numeric: true, width: 90 },
    { id: 'exemplar', label: 'Exemplar', width: 120 },
  ], []);

  const dt = useDataTable<ShapeRow>({
    storageKey: 'trace-shapes',
    columns: COLS,
    rows: shapes,
    initialSort: { id: 'count', dir: 'desc' },
  });

  if (rows === undefined) return <Spinner label="Sampling traces to cluster by shape…" />;
  if (rows === null) return <Empty icon="⚠" title="Failed to sample traces" />;
  if (shapes.length === 0) {
    return (
      <Empty icon="◇" title="No shapes in this window">
        <div style={{ marginTop: 6, color: 'var(--text2)' }}>
          The shapes view clusters sampled traces by their (service · operation) signature.
          Widen the time range or drop the service filter.
        </div>
      </Empty>
    );
  }

  return (
    <>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(r => {
              const errCls = r.errorRate > 5 ? 'b-err' : r.errorRate > 0 ? 'b-warn' : 'b-ok';
              return (
                <tr key={r.signature}
                  onClick={() => r.exemplar && navigate(`/trace?id=${r.exemplar}`)}
                  style={{ cursor: r.exemplar ? 'pointer' : 'default' }}
                  title={r.exemplar ? 'Open an exemplar trace for this shape' : undefined}>
                  <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                      <SvcBadge name={r.service} />
                      <span title={r.operation} style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.operation || '—'}</span>
                    </div>
                  </td>
                  <td className="num mono">{r.count.toLocaleString()}</td>
                  <td className="num"><span className={`badge ${errCls}`}>{r.errorRate.toFixed(1)}%</span></td>
                  <td className="num mono">{fmtDur(r.p50)}</td>
                  <td className="num mono">{fmtDur(r.p95)}</td>
                  <td className="num mono">{fmtDur(r.p99)}</td>
                  <td className="mono" style={{ fontSize: 10.5, color: 'var(--accent2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {r.exemplar ? `${r.exemplar.slice(0, 12)}…` : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div style={{ marginTop: 10, fontSize: 12, color: 'var(--text3)' }}>
        {shapes.length} shapes · sampled {rows.length} trace{rows.length === 1 ? '' : 's'} · click a row to open an exemplar
      </div>
    </>
  );
}
