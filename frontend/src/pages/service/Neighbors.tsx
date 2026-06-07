import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { healthLevel, healthColor } from '@/lib/serviceHealth';
import { Spinner } from '@/components/Spinner';
import type { TimeRange } from '@/lib/types';

// Neighbors — the flat "Upstream / downstream" card that REPLACED the in-page
// service-flow topology map on the Service Overview (prototype design-parity).
// Two columns, no center node, no SVG wires: inbound callers (left) and
// outbound dependencies (right), each a row of dot + name + kind + edge stats.
// The full graph topology lives only on the /topology page. Reads the same
// 1-hop slice of the service topology the flow map used.

interface Nbr {
  name: string;
  to?: string; // /service deep-link (services only; db/queue/external have none)
  kind: string; // service | db | queue | external
  calls: number;
  avgMs: number;
  errRate: number;
}

function fnum(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}K` : `${n}`;
}
function kindLabel(kind: string): string {
  return kind === 'db' ? 'database' : kind === 'queue' ? 'queue' : kind === 'external' ? 'external' : 'service';
}
// Strip the topology node prefix ("db:h2" → "h2", "queue:orders" → "orders").
function cleanName(raw: string): string {
  const i = raw.indexOf(':');
  return i > 0 && i < 8 ? raw.slice(i + 1) : raw;
}

function NbrRow({ d }: { d: Nbr }) {
  const inner = (
    <>
      <span className={`ov-dot ${healthLevel(d.errRate)}`} />
      <div className="nbr-id">
        <div className="nbr-nm" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{d.name}</div>
        <div className="nbr-st">{kindLabel(d.kind)}</div>
      </div>
      <div className="nbr-edge">
        <b style={{ color: 'var(--text)' }}>{fnum(d.calls)}</b><br />
        {d.avgMs.toFixed(0)} ms · <span style={{ color: healthColor(healthLevel(d.errRate)) }}>{d.errRate.toFixed(1)}%</span>
      </div>
    </>
  );
  return d.to
    ? <Link className="nbr-node" to={d.to} style={{ textDecoration: 'none', color: 'inherit' }}>{inner}</Link>
    : <div className="nbr-node">{inner}</div>;
}

export function Neighbors({ service, range, from, to }: {
  service: string; range: TimeRange; from: number; to: number;
}) {
  const topoQ = useQuery({
    queryKey: ['service-overview-neighbors', service, from, to],
    queryFn: () => api.serviceTopology({ from, to, focus: service, hops: 1 }),
    enabled: !!service,
    staleTime: 30_000,
  });

  const rangeParam = encodeRange(range);
  const { up, down } = useMemo(() => {
    const edges = topoQ.data?.edges ?? [];
    const cIn: Nbr[] = [];
    const cOut: Nbr[] = [];
    for (const e of edges) {
      if (e.childNode === service) {
        // inbound caller
        cIn.push({ name: e.parentService, to: `/service?name=${encodeURIComponent(e.parentService)}&range=${rangeParam}`, kind: 'service', calls: e.calls, avgMs: e.avgMs, errRate: e.errorRate });
      } else if (e.parentService === service) {
        // outbound dependency
        const svc = e.nodeKind === 'service';
        cOut.push({ name: cleanName(e.childNode), to: svc ? `/service?name=${encodeURIComponent(e.childNode)}&range=${rangeParam}` : undefined, kind: e.nodeKind, calls: e.calls, avgMs: e.avgMs, errRate: e.errorRate });
      }
    }
    cIn.sort((a, b) => b.calls - a.calls);
    cOut.sort((a, b) => b.calls - a.calls);
    return { up: cIn, down: cOut };
  }, [topoQ.data, service, rangeParam]);

  const total = up.length + down.length;

  return (
    <div className="card ov-mb">
      <div className="ov-card-h">
        <h3>Upstream / downstream</h3>
        <span className="ov-sub">neighbors of {service} · last {range.preset}</span>
        <span className="ov-right"><span className="badge b-gray">{total} neighbors</span></span>
      </div>
      <div className="ov-card-b">
        {topoQ.isLoading ? (
          <div style={{ display: 'grid', placeItems: 'center', padding: 24 }}><Spinner /></div>
        ) : total === 0 ? (
          <div className="nbr-empty" style={{ textAlign: 'center', padding: 16 }}>No neighbors in this window.</div>
        ) : (
          <div className="nbr-cols">
            <div className="nbr-col">
              <div className="nbr-col-lab">↘ Upstream · {up.length}</div>
              <div className="nbr-col-sub">services that call {service}</div>
              {up.length === 0
                ? <div className="nbr-empty">No upstream callers</div>
                : up.map(d => <NbrRow key={d.name} d={d} />)}
            </div>
            <div className="nbr-col">
              <div className="nbr-col-lab">↗ Downstream · {down.length}</div>
              <div className="nbr-col-sub">services {service} calls</div>
              {down.length === 0
                ? <div className="nbr-empty">No downstream dependencies</div>
                : down.map(d => <NbrRow key={d.name} d={d} />)}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
