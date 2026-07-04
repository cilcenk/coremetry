import { useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { ServiceGraph } from '@/components/ServiceGraph';
import { ServicePicker } from '@/components/ServicePicker';
import { Button } from '@/components/ui/Button';
import { useServiceGraph } from '@/lib/queries';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import type { NodeSizeMode, NodeSizeMetric } from '@/lib/topologyNodes';
import type { GraphNodeKind } from '@/lib/types';

// Service map (v0.8.277 — the topology promotion, T1 of the Datadog-bar plan).
// The page now renders the canonical dagre+canvas ServiceGraph — zoom/pan/Fit,
// node-size encoding, per-edge RED labels, click→inspector — fed ENTIRELY by
// the MV-backed /api/servicegraph (topology_edges_5m: full coverage, per-edge
// rate/error-rate/avg/p99). This finishes the stranded Stage-3 swap: the old
// SVG TopologyFlowGraph + the trace-SAMPLED /api/service-map (no latency, no
// edge error rate, rare edges missed) are retired from this surface, and the
// lossy serviceGraphToMap adapter is deleted outright.
//
// URL is the source of truth for every control (house rule): ?focus= (the
// neighborhood scope), ?top= (overview cap, default 100), ?brokers=hide,
// ?size=/&metric= (node-size encoding, lifted from the old preview route).
// All writes replace:true and copy prev so foreign params survive.
//
// Landing = the GLOBAL top-100 map (Datadog-style). The pre-v0.8.277
// auto-focus-busiest-service landing existed because the old global view was
// an unreadable hairball; the server topN prune + canvas culling remove the
// premise. ?focus= deep links (Endpoints, service tabs, /topology redirect)
// keep working unchanged.
//
// Compare/delta + the cluster ring/filter are OFF this release — they lived on
// the sampled endpoint; T2 ports them to the MV path (diff= param + cluster
// enrichment) and they return.

const TOP_DEFAULT = 100;
const HIDE_BROKERS: GraphNodeKind[] = ['queue'];

export default function ServiceMapPage() {
  const [range, setRange] = useUrlRange('30m');
  const nav = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  // Every control derives straight from the URL — no state double-source, so
  // the v0.8.253 sig-guard class of bug can't exist here.
  const focus = searchParams.get('focus') ?? '';
  // v0.8.278 — the global map is NEVER uncapped (server clamps too): 0 / old
  // "All services" links / garbage all mean the 500-node render budget. At a
  // 1000+-service install an unbounded graph stalls dagre and reads as a
  // hairball; the full estate is browsed via grouping (T3), not one big draw.
  const topParam = searchParams.get('top');
  const parsedTop = Number(topParam);
  const topN = topParam === null
    ? TOP_DEFAULT
    : (Number.isFinite(parsedTop) && parsedTop > 0 ? Math.min(parsedTop, 500) : 500);
  const hideBrokers = searchParams.get('brokers') === 'hide';
  const nodeSizeMode: NodeSizeMode = searchParams.get('size') === 'incoming' ? 'incoming' : 'outgoing';
  const nodeSizeMetric: NodeSizeMetric = searchParams.get('metric') === 'duration' ? 'duration' : 'rate';

  const setParam = (k: string, v: string | null) => {
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      if (v === null || v === '') p.delete(k); else p.set(k, v);
      return p;
    }, { replace: true });
  };
  // Single focus-commit path (v0.8.265 discipline): picker, the inspector's
  // "Focus →" button and the global-map escape all route through here.
  const commitFocus = (v: string) => setParam('focus', v || null);
  const onNodeSizeChange = (mode: NodeSizeMode, metric: NodeSizeMetric) => {
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      // Defaults stay out of the URL so a shared link is clean.
      if (mode === 'outgoing') p.delete('size'); else p.set('size', mode);
      if (metric === 'rate') p.delete('metric'); else p.set('metric', metric);
      return p;
    }, { replace: true });
  };

  const scope = focus ? 'neighborhood' as const : 'global' as const;
  const effTopN = focus ? 0 : topN; // a neighborhood is already scoped — never prune it

  // Same hook + args as the ServiceGraph canvas below → React Query dedupes
  // into ONE fetch; the page reads the payload for the header/strip only.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const graphQ = useServiceGraph(scope, focus || undefined, from, to, effTopN);
  const g = graphQ.data;

  const focusNode = focus && g ? g.nodes.find(n => n.id === focus) : undefined;
  const callers = focus && g ? g.edges.filter(e => e.target === focus).length : 0;
  // Callee buckets — "calls 3 svc, 2 db, 1 queue" at a glance in the header.
  const calleeBuckets = useMemo(() => {
    const out = { service: 0, database: 0, queue: 0, external: 0, internal: 0 };
    if (!g || !focus) return out;
    const byId = new Map(g.nodes.map(n => [n.id, n] as const));
    for (const e of g.edges) {
      if (e.source !== focus) continue;
      const kind = byId.get(e.target)?.kind ?? 'service';
      out[kind] += 1;
    }
    return out;
  }, [g, focus]);
  const calleesTotal = calleeBuckets.service + calleeBuckets.database
    + calleeBuckets.queue + calleeBuckets.external + calleeBuckets.internal;

  return (
    <>
      <Topbar title="Service map" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{
          display: 'flex', gap: 10, alignItems: 'center',
          marginBottom: 14, flexWrap: 'wrap',
        }}>
          {focus && (
            <Button variant="ghost" size="sm" onClick={() => commitFocus('')}
              title="Back to the global map">
              ← Global map
            </Button>
          )}
          <label style={{ fontSize: 12, color: 'var(--text2)' }}>Focus</label>
          <ServicePicker value={focus} onChange={commitFocus}
            placeholder="Focus a service…" width={240} />
          {focus && focusNode && (
            <Link to={`/service?name=${encodeURIComponent(focus)}`}
                  className="sec"
                  style={{
                    fontSize: 12, padding: '3px 10px',
                    textDecoration: 'none',
                    color: 'var(--text)',
                    border: '1px solid var(--border)',
                    borderRadius: 6,
                  }}>
              View {focus} detail →
            </Link>
          )}

          <span style={{ flex: 1 }} />

          {/* Overview cap (v0.8.215 contract on the MV path) — global only;
              a focused neighborhood is already scoped. */}
          {!focus && (
            <>
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>Show</span>
              <select value={String(topN)}
                      onChange={e => setParam('top', e.target.value === String(TOP_DEFAULT) ? null : e.target.value)}
                      style={{ fontSize: 12 }}
                      title="Cap the map to the heaviest N services; fewer nodes = readable graph">
                <option value="50">Top 50</option>
                <option value="100">Top 100</option>
                <option value="250">Top 250</option>
                <option value="500">Top 500 (max)</option>
              </select>
              {/* Brokers are first-class nodes on the MV path (the old sampled
                  view dropped them); the toggle hides the kafka/rabbit chrome
                  when the operator only cares about service→service flow. */}
              <label style={{ fontSize: 12, color: 'var(--text2)', display: 'inline-flex', gap: 5, alignItems: 'center', cursor: 'pointer' }}>
                <input type="checkbox" checked={hideBrokers}
                  onChange={e => setParam('brokers', e.target.checked ? 'hide' : null)} />
                Hide brokers
              </label>
            </>
          )}
        </div>

        {/* Overview cap indicator — the map is pruned to the heaviest N
            services; tell the operator it's not the whole truth. */}
        {!focus && g && g.shownNodes < g.totalNodes && (
          <div style={{ fontSize: 12, color: 'var(--text2)', margin: '4px 0 8px' }}>
            Showing the <strong>{g.shownNodes}</strong> heaviest of{' '}
            <strong>{g.totalNodes}</strong> services.{' '}
            {topN >= 500
              ? 'That’s the render budget — focus a service to explore the rest.'
              : 'Raise “Show” or focus a service to see a specific area.'}
          </div>
        )}

        {/* Focus header — the focused service's KPIs above the graph, read
            from the SAME MV payload the canvas renders. */}
        {focus && focusNode && (
          <div style={{
            display: 'flex', gap: 14, alignItems: 'center',
            padding: '10px 14px', marginBottom: 12,
            background: 'var(--bg1)',
            border: '1px solid var(--border)',
            borderRadius: 8,
            flexWrap: 'wrap',
          }}>
            <span style={{ fontSize: 14, fontWeight: 600 }}>{focus}</span>
            <span className={`badge b-${focusNode.errorRate >= 5 ? 'err' : focusNode.errorRate >= 1 ? 'warn' : 'ok'}`}>
              {focusNode.errorRate.toFixed(2)}% error
            </span>
            <Chip label="Calls" value={fmtNum(focusNode.calls)} />
            <Chip label="Rate" value={`${fmtNum(Math.round(focusNode.rate))}/min`} />
            <Chip label="Callers" value={`${callers}`} />
            <Chip label="Callees" value={
              calleesTotal === 0
                ? '0'
                : [
                    calleeBuckets.service  > 0 && `${calleeBuckets.service} svc`,
                    calleeBuckets.database > 0 && `${calleeBuckets.database} db`,
                    calleeBuckets.queue    > 0 && `${calleeBuckets.queue} queue`,
                    calleeBuckets.external > 0 && `${calleeBuckets.external} ext`,
                    calleeBuckets.internal > 0 && `${calleeBuckets.internal} int`,
                  ].filter(Boolean).join(' · ')
            } />
            <span style={{ flex: 1 }} />
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              showing {focus}'s neighborhood — {g?.nodes.length ?? 0} nodes
            </span>
          </div>
        )}

        <ServiceGraph
          scope={scope}
          focus={focus || undefined}
          range={range}
          height={640}
          topN={effTopN}
          hideKinds={!focus && hideBrokers ? HIDE_BROKERS : undefined}
          onSelectService={svc => nav(`/service?name=${encodeURIComponent(svc)}`)}
          onFocusService={commitFocus}
          nodeSizeMode={nodeSizeMode}
          nodeSizeMetric={nodeSizeMetric}
          onNodeSizeChange={onNodeSizeChange}
        />

        <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
          Click a node or edge to inspect · double-click a service to open it ·
          scroll to zoom, drag to pan
        </div>
      </div>
    </>
  );
}

function Chip({ label, value }: { label: string; value: string }) {
  return (
    <span style={{
      fontSize: 11, color: 'var(--text2)',
      display: 'inline-flex', gap: 6, alignItems: 'baseline',
    }}>
      <span style={{ color: 'var(--text3)' }}>{label}</span>
      <span style={{ fontFamily: 'monospace', color: 'var(--text)' }}>{value}</span>
    </span>
  );
}
