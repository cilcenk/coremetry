import { Fragment, useEffect } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { Badge, VirtualTable } from 'coremetry-ui';
import { useDataTable } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { Frame } from '../preview-lib/frame';

// VirtualTable windows a real <table> driven by useDataTable (the shared
// sort + resize header). The hook reads/writes the URL sort param through
// react-router's useSearchParams, so each cell mounts inside a MemoryRouter.
// Rows come from a seeded PRNG so captures are stable.

function rng(seed: number) {
  let t = seed >>> 0;
  return () => {
    t += 0x6D2B79F5;
    let r = Math.imul(t ^ (t >>> 15), 1 | t);
    r ^= r + Math.imul(r ^ (r >>> 7), 61 | r);
    return ((r ^ (r >>> 14)) >>> 0) / 4294967296;
  };
}
const pick = <T,>(r: () => number, xs: readonly T[]) => xs[Math.floor(r() * xs.length)];
const SERVICES = ['api-gateway', 'payments-orchestrator', 'checkout', 'identity', 'ledger', 'notifier'] as const;
const OPS = ['GET /v1/orders/{id}', 'POST /v1/payments', 'PUT /v1/carts/{id}/items', 'orders.created consume',
             'SELECT ledger.postings', 'POST /oauth/token', 'GET /v1/accounts/{id}/balance'] as const;
const cell = { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const;
const mono = { fontFamily: 'ui-monospace, monospace', fontSize: 11.5 } as const;

// ── Traces ────────────────────────────────────────────────────────────
type TraceRow = { traceId: string; service: string; operation: string; durationMs: number; spans: number; hasError: boolean; started: string };
const BASE = Date.parse('2026-08-29T09:14:03Z');
const TRACES: TraceRow[] = (() => {
  const r = rng(3);
  const hex = (n: number) => Array.from({ length: n }, () => Math.floor(r() * 16).toString(16)).join('');
  return Array.from({ length: 3000 }, (_, i) => {
    const durationMs = Math.round(Math.exp(3 + r() * 4.2));
    // Slow traces fail more often (timeouts), so errors cluster at the top of a duration sort.
    return { traceId: hex(16), service: pick(r, SERVICES), operation: pick(r, OPS), durationMs,
             spans: 3 + Math.floor(r() * 60), hasError: r() < (durationMs > 800 ? 0.35 : 0.06),
             started: new Date(BASE - i * 2900).toISOString().slice(11, 19) };
  });
})();
const TRACE_COLS: DataTableColumn<TraceRow>[] = [
  { id: 'traceId',    label: 'Trace',     width: 130, sortValue: t => t.traceId, naturalDir: 'asc' },
  { id: 'service',    label: 'Service',   width: 150, sortValue: t => t.service, naturalDir: 'asc' },
  { id: 'operation',  label: 'Operation', flex: true, minWidth: 120, sortValue: t => t.operation, naturalDir: 'asc' },
  { id: 'durationMs', label: 'Duration',  width: 104,  numeric: true, sortValue: t => t.durationMs },
  { id: 'spans',      label: 'Spans',     width: 78,  numeric: true, sortValue: t => t.spans },
  { id: 'status',     label: 'Status',    width: 86,  sortValue: t => (t.hasError ? 1 : 0) },
  { id: 'started',    label: 'Started',   width: 98,  sortValue: t => t.started },
];
const fmtMs = (ms: number) => ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms} ms`;

function TracesTable({ rows, height, emptyMessage }: { rows: TraceRow[]; height: number; emptyMessage?: string }) {
  const dt = useDataTable<TraceRow>({ storageKey: 'ds-traces', columns: TRACE_COLS, rows,
                                      initialSort: { id: 'durationMs', dir: 'desc' } });
  return (
    <VirtualTable<TraceRow> dt={dt} height={height} rowHeight={36} leading={[30]}
      leadingHead={<th style={{ width: 30 }} />} getRowKey={t => t.traceId} emptyMessage={emptyMessage}
      renderRow={t => {
        const tint = t.hasError ? 'color-mix(in srgb, var(--err) 8%, transparent)' : undefined;
        return (
          <Fragment>
            <td style={{ padding: '9px 0', textAlign: 'center', color: 'var(--text3)', userSelect: 'none', background: tint }} title="Preview spans">▸</td>
            <td style={{ ...cell, ...mono, background: tint }}>{t.traceId}</td>
            <td style={{ ...cell, background: tint }}>{t.service}</td>
            <td style={{ ...cell, background: tint }} title={t.operation}>{t.operation}</td>
            <td className="num" style={{ background: tint }}>{fmtMs(t.durationMs)}</td>
            <td className="num" style={{ background: tint }}>{t.spans}</td>
            <td style={{ background: tint }}><Badge tone={t.hasError ? 'danger' : 'success'}>{t.hasError ? 'error' : 'ok'}</Badge></td>
            <td style={{ ...cell, ...mono, color: 'var(--text2)', background: tint }}>{t.started}</td>
          </Fragment>
        );
      }} />
  );
}

// 3 000 traces sorted by duration (header arrow on Duration), error rows
// tinted, leading chevron column — the /traces list shape.
export function TraceRows() {
  return (
    <MemoryRouter>
      <Frame>
        <TracesTable rows={TRACES} height={320} />
      </Frame>
    </MemoryRouter>
  );
}

// No rows: the table keeps its header and shows the .vt-empty cell.
export function Empty() {
  return (
    <MemoryRouter>
      <Frame>
        <TracesTable rows={[]} height={180}
          emptyMessage="No traces in the last 15 min for payments-orchestrator with status = error" />
      </Frame>
    </MemoryRouter>
  );
}

// ── Problems (Inbox) ──────────────────────────────────────────────────
type Prio = 'P1' | 'P2' | 'P3';
type Problem = { id: string; prio: Prio; service: string; kind: string; reason: string; openMin: number; count: number };
const REASONS: Record<Prio, string[]> = {
  P1: ['error rate 4.8% ≥ 2× baseline 2.1% for 18 min', 'exception storm: new group in 4 services in one window',
       'open 5h 12m > 4h auto-P1', 'p99 2 340 ms vs 900 ms baseline, critical service'],
  P2: ['p95 up 1.6× vs last week, same hour', 'SLO checkout-availability burn 3.1× (1h window)',
       'CrashLoopBackOff on 2 of 6 pods'],
  P3: ['chronic: 3 occurrences / 24h, no growth', 'new exception group, 1 occurrence', 'deploy marker without RED change'],
};
const PROBLEMS: Problem[] = (() => {
  const r = rng(5);
  return Array.from({ length: 400 }, (_, i) => {
    const x = r();
    const prio: Prio = x < 0.12 ? 'P1' : x < 0.45 ? 'P2' : 'P3';
    return { id: `prb-${(1000 + i).toString(36)}`, prio, service: pick(r, SERVICES),
             kind: pick(r, ['Problem', 'Exception', 'Anomali', 'SLO'] as const), reason: pick(r, REASONS[prio]),
             openMin: Math.floor(r() * 900), count: 1 + Math.floor(Math.exp(r() * 7)) };
  });
})();
const PRIO_RANK: Record<Prio, number> = { P1: 3, P2: 2, P3: 1 };
const PRIO_TONE: Record<Prio, 'danger' | 'warning' | 'neutral'> = { P1: 'danger', P2: 'warning', P3: 'neutral' };
const fmtOpen = (m: number) => m >= 60 ? `${Math.floor(m / 60)}h ${(m % 60).toString().padStart(2, '0')}m` : `${m}m`;
const PROBLEM_COLS: DataTableColumn<Problem>[] = [
  { id: 'prio',    label: 'Priority', width: 100,  sortValue: p => PRIO_RANK[p.prio] },
  { id: 'service', label: 'Service',  width: 160, sortValue: p => p.service, naturalDir: 'asc' },
  { id: 'kind',    label: 'Kind',     width: 90,  sortValue: p => p.kind, naturalDir: 'asc' },
  { id: 'reason',  label: 'Reason',   flex: true, minWidth: 160, sortValue: p => p.reason, naturalDir: 'asc' },
  { id: 'open',    label: 'Open',     width: 80,  numeric: true, sortValue: p => p.openMin },
  { id: 'count',   label: 'Count',    width: 82,  numeric: true, sortValue: p => p.count },
];

function ProblemsTable() {
  const dt = useDataTable<Problem>({ storageKey: 'ds-problems', columns: PROBLEM_COLS, rows: PROBLEMS,
                                     initialSort: { id: 'prio', dir: 'desc' }, onOpen: () => {} });
  // Keyboard-nav selection (j/k) on the third row — rowProps paints .row-selected.
  const select = dt.nav.setSelected;
  useEffect(() => { select(2); }, [select]);
  return (
    <VirtualTable<Problem> dt={dt} height={300} rowHeight={36} getRowKey={p => p.id}
      renderRow={p => (
        <Fragment>
          <td><Badge tone={PRIO_TONE[p.prio]}>{p.prio}</Badge></td>
          <td style={cell}>{p.service}</td>
          <td style={{ ...cell, color: 'var(--text2)' }}>{p.kind}</td>
          <td style={cell} title={p.reason}>{p.reason}</td>
          <td className="num" style={{ color: p.openMin > 240 ? 'var(--warn)' : undefined }}>{fmtOpen(p.openMin)}</td>
          <td className="num">{p.count.toLocaleString('fr-FR')}</td>
        </Fragment>
      )} />
  );
}

// Inbox problems: no leading column, Badge priorities, one row selected by
// the table's keyboard nav (accent tint + left bar).
export function ProblemsSelected() {
  return (
    <MemoryRouter>
      <Frame>
        <ProblemsTable />
      </Frame>
    </MemoryRouter>
  );
}
