import { Badge, VirtualList } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// VirtualList windows a flat row collection (Logs live-tail, SQL console
// result grid). Data below is generated with a seeded PRNG so the card is
// stable between captures.

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
type Level = 'INFO' | 'WARN' | 'ERROR';
const MSG: Record<Level, string[]> = {
  INFO: ['GET /v1/orders/{id} 200 in 42ms', 'POST /v1/payments 201 in 118ms',
         'consumer lag 0 on orders.created', 'redis cache hit ratio 0.97', 'health probe ok'],
  WARN: ['retrying ledger.Post after 503 (attempt 2/3)', 'ClickHouse async_insert queue at 78%',
         'slow query 1 240ms on db_summary_5m', 'upstream identity p99 812ms > budget 500ms'],
  ERROR: ['PaymentDeclinedException: issuer timeout after 3 000ms', 'dial tcp ledger:8443: connection refused',
          'context deadline exceeded on span export', 'NullPointerException at OrderMapper.map:212'],
};
type Line = { id: number; ts: string; level: Level; service: string; msg: string };
const BASE = Date.parse('2026-08-29T09:14:03.120Z');
const LINES: Line[] = (() => {
  const r = rng(7);
  return Array.from({ length: 5000 }, (_, i) => {
    const x = r();
    const level: Level = x < 0.12 ? 'ERROR' : x < 0.30 ? 'WARN' : 'INFO';
    return { id: i, ts: new Date(BASE - i * 137).toISOString().slice(11, 23), level,
             service: pick(r, SERVICES), msg: pick(r, MSG[level]) };
  });
})();
const TONE: Record<Level, 'info' | 'warning' | 'danger'> = { INFO: 'info', WARN: 'warning', ERROR: 'danger' };
const ell = { overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' } as const;

// Logs live-tail: 5 000 lines, 28px rows, only the visible window in the DOM.
export function LogTail() {
  return (
    <Frame>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6 }}>
        Logs · live tail · {LINES.length.toLocaleString('fr-FR')} lines · newest first
      </div>
      <VirtualList items={LINES} rowHeight={28} height={280} className="vt-scroll" getKey={l => l.id}
        renderRow={l => (
          <div style={{ display: 'grid', gridTemplateColumns: '96px 62px 180px minmax(0,1fr)', alignItems: 'center',
                        gap: 8, height: 28, padding: '0 10px', borderBottom: '1px solid var(--border)', fontSize: 12,
                        background: l.level === 'ERROR' ? 'color-mix(in srgb, var(--err) 7%, transparent)' : undefined }}>
            <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11, color: 'var(--text3)' }}>{l.ts}</span>
            <Badge tone={TONE[l.level]}>{l.level}</Badge>
            <span style={{ color: 'var(--accent2)', ...ell }}>{l.service}</span>
            <span style={ell} title={l.msg}>{l.msg}</span>
          </div>
        )} />
    </Frame>
  );
}

type Pod = { name: string; ns: string; status: 'Running' | 'CrashLoopBackOff' | 'Pending'; restarts: number; cpu: number; mem: number };
const PODS: Pod[] = (() => {
  const r = rng(11);
  const hex = () => Math.floor(r() * 0xfffff).toString(16).padStart(5, '0');
  return Array.from({ length: 1200 }, () => {
    const x = r();
    const status: Pod['status'] = x < 0.04 ? 'CrashLoopBackOff' : x < 0.07 ? 'Pending' : 'Running';
    const svc = pick(r, SERVICES);
    return { name: `${svc}-${hex()}-${hex().slice(0, 5)}`, ns: pick(r, ['payments', 'core', 'edge', 'identity'] as const),
             status, restarts: status === 'CrashLoopBackOff' ? 12 + Math.floor(r() * 40) : Math.floor(r() * 3),
             cpu: Math.round(20 + r() * 900), mem: Math.round(128 + r() * 1400) };
  });
})();
const POD_TONE: Record<Pod['status'], 'success' | 'danger' | 'warning'> = { Running: 'success', CrashLoopBackOff: 'danger', Pending: 'warning' };
const num = { textAlign: 'right', fontVariantNumeric: 'tabular-nums' } as const;

// Pods of one cluster: 1 200 rows at the house 36px row height.
export function PodRows() {
  return (
    <Frame>
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 90px 150px 64px 72px 72px', gap: 8, padding: '0 10px 6px',
                    fontSize: 10, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '.06em', color: 'var(--text3)' }}>
        <span>Pod</span><span>Namespace</span><span>Status</span><span style={num}>Restarts</span><span style={num}>CPU m</span><span style={num}>Mem Mi</span>
      </div>
      <VirtualList items={PODS} rowHeight={36} height={300} className="vt-scroll" getKey={p => p.name}
        renderRow={p => (
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 90px 150px 64px 72px 72px', alignItems: 'center',
                        gap: 8, height: 36, padding: '0 10px', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
            <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11.5, ...ell }} title={p.name}>{p.name}</span>
            <span style={{ color: 'var(--text2)', ...ell }}>{p.ns}</span>
            <span><Badge tone={POD_TONE[p.status]}>{p.status}</Badge></span>
            <span style={{ ...num, color: p.restarts > 5 ? 'var(--err)' : undefined }}>{p.restarts}</span>
            <span style={num}>{p.cpu}</span>
            <span style={num}>{p.mem}</span>
          </div>
        )} />
    </Frame>
  );
}

type Cluster = { name: string; provider: string; nodes: number; health: 'healthy' | 'degraded' | 'unreachable'; sync: string };
const CLUSTERS: Cluster[] = [
  { name: 'prod-eu-west', provider: 'OpenShift 4.16', nodes: 48, health: 'healthy',     sync: '12 s ago' },
  { name: 'prod-us-east', provider: 'OpenShift 4.16', nodes: 36, health: 'degraded',    sync: '15 s ago' },
  { name: 'staging',      provider: 'EKS 1.30',       nodes: 9,  health: 'healthy',     sync: '9 s ago' },
  { name: 'fake-ocp',     provider: 'minikube',       nodes: 1,  health: 'unreachable', sync: '6 min ago' },
];
const HEALTH_TONE: Record<Cluster['health'], 'success' | 'warning' | 'danger'> = { healthy: 'success', degraded: 'warning', unreachable: 'danger' };

// Fewer rows than the viewport: no scrollbar, rows keep their height, the
// scroller does not stretch them to fill.
export function Sparse() {
  return (
    <Frame>
      <VirtualList items={CLUSTERS} rowHeight={40} height={220} className="vt-scroll" getKey={c => c.name}
        renderRow={c => (
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 130px 70px 110px 90px', alignItems: 'center',
                        gap: 8, height: 40, padding: '0 12px', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
            <span style={{ fontWeight: 600, ...ell }}>{c.name}</span>
            <span style={{ color: 'var(--text2)', ...ell }}>{c.provider}</span>
            <span style={num}>{c.nodes} nodes</span>
            <span><Badge tone={HEALTH_TONE[c.health]}>{c.health}</Badge></span>
            <span style={{ color: 'var(--text3)', fontSize: 11, textAlign: 'right' }}>{c.sync}</span>
          </div>
        )} />
    </Frame>
  );
}
