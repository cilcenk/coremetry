import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TopologyFlowGraph } from '@/components/TopologyFlowGraph';
import { ServicePicker } from '@/components/ServicePicker';
import { useServiceMap } from '@/lib/queries';
import { fmtNum, hashColor } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import type { TimeRange, ServiceMap, ServiceMapNode } from '@/lib/types';

const PRESETS: { key: TimeRange['preset']; secs: number; label: string }[] = [
  { key: '5m',  secs: 300,    label: '5m'  },
  { key: '15m', secs: 900,    label: '15m' },
  { key: '1h',  secs: 3600,   label: '1h'  },
  { key: '6h',  secs: 21600,  label: '6h'  },
  { key: '24h', secs: 86400,  label: '24h' },
];

// Service map: global topology view + a focus mode that
// narrows to a single service's 1-hop neighbourhood. The
// picker is the primary interaction — pick a service →
// the graph re-lays out radially around it (caller on the
// left, callee on the right) so the operator can read
// who depends on this service and who it depends on at
// a glance, like Datadog / Honeycomb service maps.
//
// Performance posture: the underlying CH query already caps
// work to a fixed sample of recent traces, so the network
// payload stays tiny regardless of cluster size. The focus
// filter happens client-side over that small payload — no
// extra round trip — and the radial layout is closed-form
// (no physics), so swap-in is instant.
// Baseline comparison choices. "off" = no diff (default). The other
// values mirror the rolling windows operators care about most: vs
// last hour (catches deploy-time topology drift), vs yesterday
// (canonical "is anything new today?"), vs last week (longer-term
// dependency drift).
const DIFF_PRESETS: { key: string; label: string }[] = [
  { key: '',    label: 'off' },
  { key: '1h',  label: 'vs 1h ago' },
  { key: '24h', label: 'vs yesterday' },
  { key: '168h', label: 'vs last week' },
];

export default function ServiceMapPage() {
  const [range, setRange] = useUrlRange('30m');
  const [samples, setSamples] = useState(200);
  // v0.8.219 — /topology was folded into /service-map; honour its ?focus=<svc>
  // deep-link (from Endpoints / service tabs / the redirect) by seeding focus
  // from the URL. The auto-pick effect below skips when focus is already set.
  const [searchParams, setSearchParams] = useSearchParams();
  const [focus, setFocus] = useState<string>(() => searchParams.get('focus') ?? '');
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  const [diff, setDiff] = useState<string>('');
  // Overview cap (v0.8.215): bound the rendered graph to the heaviest N services
  // so the whole-production map isn't an unreadable hairball. 0 = no cap (full
  // sampled graph). Server-side prune — the browser never receives the long tail.
  const [topN, setTopN] = useState(0);
  const since = (PRESETS.find(p => p.key === range.preset)?.secs ?? 900) + 's';

  const mapQ = useServiceMap(since, samples, diff || undefined, topN);
  // Single focus-commit path (v0.8.265, operator-reported "Focus
  // seçemiyorum"): state + ?focus= URL move together (replace:true)
  // so refresh / Copy link keep the selection — the v0.8.256
  // drawer-param class of fix. Every caller (picker, node click,
  // auto-pick) routes through here.
  const commitFocus = (v: string) => {
    setFocus(v);
    setAutoFocused(true);
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      if (v) p.set('focus', v); else p.delete('focus');
      return p;
    }, { replace: true });
  };
  // Auto-pick a focused service on first load so the operator
  // lands on a useful 1-hop view instead of the full graph (which
  // can look like a hairball on large clusters). Deterministic:
  // THE busiest real service (v0.8.265 — was random top-3; the
  // operator asked for a stable pre-selected landing). Fires once;
  // any manual pick disables it.
  const [autoFocused, setAutoFocused] = useState(false);
  useEffect(() => {
    const nodes = mapQ.data?.nodes ?? [];
    if (autoFocused || focus || !mapQ.data || nodes.length === 0) return;
    const real = nodes
      .filter(n => !n.kind)
      .sort((a, b) => b.spanCount - a.spanCount);
    if (real.length > 0) {
      commitFocus(real[0].service);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mapQ.data, autoFocused, focus]);
  // Normalise nodes/edges to arrays even when the API returns
  // them as null (older backend, empty windows). Downstream
  // iterations assume arrays — without this, the page crashed
  // with "i.nodes is not iterable" on first load against a
  // pre-v0.5.105 server that hadn't returned empty slices yet.
  const data = mapQ.isLoading
    ? undefined
    : mapQ.isError
      ? null
      : mapQ.data
        ? { ...mapQ.data, nodes: mapQ.data.nodes ?? [], edges: mapQ.data.edges ?? [] }
        : { nodes: [], edges: [], sampledFrom: 0, totalSpans: 0 };

  // Cluster filter — when non-empty, narrows the graph to
  // nodes whose enriched cluster matches. "multi" services
  // count as a match for ANY cluster pick so a frontend
  // running in eu-west AND eu-central isn't hidden from
  // both views. Empty = show everything (default).
  const [clusterPick, setClusterPick] = useState<string>('');
  const clusterOptions = useMemo(() => {
    if (!data) return [] as string[];
    const set = new Set<string>();
    for (const n of data.nodes) {
      if (n.cluster && n.cluster !== 'multi') set.add(n.cluster);
    }
    return [...set].sort();
  }, [data]);

  // Filter to the 1-hop neighbourhood of the focused service
  // (focused + every direct caller + every direct callee).
  // Then apply the cluster filter on top.
  // Edges are kept iff both endpoints survived. Memoised so
  // hover-induced re-renders don't recompute.
  const filtered = useMemo<ServiceMap | undefined>(() => {
    if (!data) return undefined;
    let nodes = data.nodes;
    let edges = data.edges;
    if (focus) {
      const keep = new Set<string>([focus]);
      for (const e of data.edges) {
        if (e.caller === focus) keep.add(e.callee);
        if (e.callee === focus) keep.add(e.caller);
      }
      nodes = nodes.filter(n => keep.has(n.service));
      edges = edges.filter(e => keep.has(e.caller) && keep.has(e.callee));
    }
    if (clusterPick) {
      const inCluster = (svc: string) => {
        const n = nodes.find(x => x.service === svc);
        if (!n) return false;
        return n.cluster === clusterPick || n.cluster === 'multi' || !n.cluster;
      };
      nodes = nodes.filter(n =>
        n.cluster === clusterPick || n.cluster === 'multi' || !n.cluster);
      edges = edges.filter(e => inCluster(e.caller) && inCluster(e.callee));
    }
    return {
      nodes,
      edges,
      sampledFrom: data.sampledFrom,
      totalSpans:  data.totalSpans,
    };
  }, [data, focus, clusterPick]);

  const focusNode: ServiceMapNode | undefined = focus && data
    ? data.nodes.find(n => n.service === focus)
    : undefined;
  const callers = data && focus
    ? data.edges.filter(e => e.callee === focus).length
    : 0;
  // Callee buckets — tell the operator at a glance how many of
  // the focused service's downstreams are services vs DBs vs
  // external deps. Reads as "calls 3 services, 2 dbs, 1 ext"
  // in the focus header.
  const calleeBuckets = useMemo(() => {
    const out = { service: 0, db: 0, queue: 0, external: 0 };
    if (!data || !focus) return out;
    const byName = new Map<string, ServiceMapNode>(
      data.nodes.map(n => [n.service, n] as const));
    for (const e of data.edges) {
      if (e.caller !== focus) continue;
      const callee = byName.get(e.callee);
      const kind = (callee?.kind || 'service') as keyof typeof out;
      if (kind in out) out[kind] += 1;
    }
    return out;
  }, [data, focus]);
  const calleesTotal = calleeBuckets.service + calleeBuckets.db + calleeBuckets.queue + calleeBuckets.external;

  return (
    <>
      <Topbar title="Service map" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{
          display: 'flex', gap: 10, alignItems: 'center',
          marginBottom: 14, flexWrap: 'wrap',
        }}>
          {/* Focus picker (v0.8.265, operator-reported "Focus
              seçemiyorum"). Was a datalist <input> whose commit
              path required the typed value to exist in the SAMPLED
              map nodes — on a 1400-service install most picks
              matched nothing and silently didn't commit, and the
              eager full-catalogue datalist is the exact picker
              anti-pattern the hard constraints ban. The shared
              server-debounced ServicePicker commits every
              selection unconditionally; a focus outside the
              current sample simply renders its empty-state note. */}
          <label style={{ fontSize: 12, color: 'var(--text2)' }}>Focus</label>
          <ServicePicker value={focus} onChange={commitFocus}
            placeholder="Focus a service…" width={240} />
          {/* Clear-focus button removed in v0.4.86 — picking a
              different service from the dropdown OR clicking a
              node in the graph already replaces the focus, so
              the separate Clear button was redundant. Operators
              who want the full hairball back can clear the
              input manually. */}
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

          {/* Topology delta toggle — surfaces "what changed?".
              When set, the backend marks new nodes / edges (in
              the current window but not the baseline) and lists
              ones that went missing. The summary strip below the
              picker row renders the delta count. */}
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>Compare</span>
          <select value={diff} onChange={e => setDiff(e.target.value)}
                  style={{ fontSize: 12 }}>
            {DIFF_PRESETS.map(p => (
              <option key={p.key} value={p.key}>{p.label}</option>
            ))}
          </select>

          <span style={{ fontSize: 12, color: 'var(--text2)' }}>Samples</span>
          <select value={samples}
                  onChange={e => setSamples(Number(e.target.value))}
                  style={{ fontSize: 12 }}>
            <option value={50}>50 traces</option>
            <option value={100}>100 traces</option>
            <option value={200}>200 traces</option>
            <option value={500}>500 traces</option>
          </select>

          {/* Overview cap (v0.8.215) — bound the graph to the heaviest N
              services so a 1000s-service prod map renders readably instead of
              a hairball. Server-side prune; "Top" = no cap (full sampled graph). */}
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>Show</span>
          <select value={topN}
                  onChange={e => setTopN(Number(e.target.value))}
                  style={{ fontSize: 12 }}
                  title="Cap the map to the heaviest N services (overview); fewer nodes = readable graph">
            <option value={0}>All services</option>
            <option value={50}>Top 50</option>
            <option value={100}>Top 100</option>
            <option value={250}>Top 250</option>
            <option value={500}>Top 500</option>
          </select>

          {/* Cluster filter — narrows the rendered graph to a
              single k8s/openshift cluster's nodes (multi-cluster
              services are kept on every view since their slice
              of traffic spans the filter target too). Hidden
              when zero clusters were enriched — single-cluster
              installs don't need the chrome. */}
          {clusterOptions.length > 0 && (
            <>
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>Cluster</span>
              <select value={clusterPick}
                      onChange={e => setClusterPick(e.target.value)}
                      style={{ fontSize: 12, maxWidth: 220 }}>
                <option value="">All clusters</option>
                {clusterOptions.map(c => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </select>
            </>
          )}
        </div>

        {/* Cluster colour legend — decodes the node rings the
            graph draws (one stable hue per cluster name via
            hashColor + a dashed grey for multi-cluster
            services). Single-cluster installs and the focus
            view skip the legend since the chrome adds no
            information there. */}
        {clusterOptions.length > 1 && !focus && (
          <div style={{
            display: 'flex', flexWrap: 'wrap', gap: 10,
            marginTop: 6, marginBottom: 8,
            fontSize: 11, color: 'var(--text2)',
          }}>
            <span style={{ color: 'var(--text3)' }}>Cluster ring:</span>
            {clusterOptions.map(c => (
              <span key={c} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                <span style={{
                  display: 'inline-block', width: 12, height: 12,
                  borderRadius: '50%', border: `1.5px solid ${hashColor(c)}`,
                  background: 'transparent',
                }} />
                {c}
              </span>
            ))}
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              <span style={{
                display: 'inline-block', width: 12, height: 12,
                borderRadius: '50%', border: '1.5px dashed var(--text3)',
                background: 'transparent',
              }} />
              multi-cluster
            </span>
          </div>
        )}

        {/* Overview cap indicator (v0.8.215) — the map is pruned to the
            heaviest N services; tell the operator it's not the whole truth. */}
        {data && (data.shownNodes ?? 0) < (data.totalNodes ?? 0) && (
          <div style={{ fontSize: 12, color: 'var(--text2)', margin: '4px 0 8px' }}>
            Showing the <strong>{data.shownNodes}</strong> heaviest of{' '}
            <strong>{data.totalNodes}</strong> services. Raise “Show” or use the
            service picker to focus a specific area.
          </div>
        )}

        {/* Topology change summary — visible only when comparison
            mode is on. Lists net delta + a small inline list of
            the new / removed services so an operator scanning
            the map sees "what's different" before reading the
            graph itself. */}
        {data && diff && (
          <TopologyDeltaStrip data={data} baselineLabel={
            DIFF_PRESETS.find(p => p.key === diff)?.label ?? diff
          } />
        )}

        {/* Focus header — when a service is selected, surface
            its KPIs above the graph so the operator doesn't
            have to navigate away to read them. */}
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
            <span className={`badge b-${focusNode.errorRate > 0.05 ? 'err' : focusNode.errorRate > 0.01 ? 'warn' : 'ok'}`}>
              {(focusNode.errorRate * 100).toFixed(2)}% error
            </span>
            <Chip label="Spans"   value={fmtNum(focusNode.spanCount)} />
            <Chip label="Callers" value={`${callers}`} />
            <Chip label="Callees" value={
              calleesTotal === 0
                ? '0'
                : [
                    calleeBuckets.service  > 0 && `${calleeBuckets.service} svc`,
                    calleeBuckets.db       > 0 && `${calleeBuckets.db} db`,
                    calleeBuckets.queue    > 0 && `${calleeBuckets.queue} queue`,
                    calleeBuckets.external > 0 && `${calleeBuckets.external} ext`,
                  ].filter(Boolean).join(' · ')
            } />
            <span style={{ flex: 1 }} />
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              showing {focus}'s 1-hop neighbourhood — {filtered?.nodes.length ?? 0} services
            </span>
          </div>
        )}

        {data === undefined && <Spinner />}
        {data === null && (
          <Empty icon="!" title="Failed to load service map">
            Check that ClickHouse is reachable and the spans table has recent data.
          </Empty>
        )}
        {data && data.nodes.length === 0 && (
          <Empty icon="◯" title="No services in this window">
            Try widening the time range or check whether OTLP ingest is flowing
            (System → ClickHouse stats).
          </Empty>
        )}
        {filtered && filtered.nodes.length > 0 && (
          <TopologyFlowGraph
            data={filtered}
            focus={focus || null}
            hoverNode={hoverNode}
            onHoverNode={setHoverNode}
            onSelectNode={commitFocus}
          />
        )}

        <div style={{ marginTop: 8, fontSize: 11, color: 'var(--text3)' }}>
          {focus
            ? 'Click any node to switch focus · auto-refresh 30 s'
            : 'Click a node to focus on its 1-hop neighbourhood · auto-refresh 30 s'}
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

// TopologyDeltaStrip surfaces topology changes between the current
// window and the chosen baseline. Renders one chip per category
// (new services, new dependencies, removed services, removed
// dependencies) plus an inline preview of the names so the operator
// gets the "what" without expanding anything. Filters out synthetic
// dep nodes (kind="db"/"queue"/"external") — those churn naturally
// as request paths shift, and surfacing every "we hit a new redis"
// row drowns the real topology changes.
function TopologyDeltaStrip({ data, baselineLabel }: { data: ServiceMap; baselineLabel: string }) {
  const newSvcs = (data.nodes ?? []).filter(n => n.isNew && !n.kind);
  const newDeps = (data.edges ?? []).filter(e => e.isNew);
  const gone = (data.removedNodes ?? []).filter(n => !n.kind);
  const goneEdges = data.removedEdges ?? [];
  const total = newSvcs.length + newDeps.length + gone.length + goneEdges.length;
  if (total === 0) {
    return (
      <div style={{
        marginBottom: 12, padding: '8px 12px', borderRadius: 6,
        background: 'var(--bg1)', border: '1px solid var(--border)',
        fontSize: 12, color: 'var(--text2)',
      }}>
        ✓ No topology changes {baselineLabel}.
      </div>
    );
  }
  const sample = (xs: string[], n = 4) => xs.slice(0, n).join(', ') + (xs.length > n ? `, +${xs.length - n}` : '');
  return (
    <div style={{
      marginBottom: 12, padding: '8px 12px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
      display: 'flex', flexWrap: 'wrap', gap: 14, alignItems: 'center', fontSize: 12,
    }}>
      <span style={{ fontWeight: 600 }}>Δ topology {baselineLabel}:</span>
      {newSvcs.length > 0 && (
        <span title={newSvcs.map(s => s.service).join('\n')}>
          <span className="badge b-ok" style={{ marginRight: 6 }}>+{newSvcs.length} svc</span>
          <span style={{ color: 'var(--text3)', fontFamily: 'monospace', fontSize: 11 }}>
            {sample(newSvcs.map(s => s.service))}
          </span>
        </span>
      )}
      {newDeps.length > 0 && (
        <span title={newDeps.map(e => `${e.caller} → ${e.callee}`).join('\n')}>
          <span className="badge b-info" style={{ marginRight: 6 }}>+{newDeps.length} edge</span>
          <span style={{ color: 'var(--text3)', fontFamily: 'monospace', fontSize: 11 }}>
            {sample(newDeps.map(e => `${e.caller}→${e.callee}`))}
          </span>
        </span>
      )}
      {gone.length > 0 && (
        <span title={gone.map(s => s.service).join('\n')}>
          <span className="badge b-warn" style={{ marginRight: 6 }}>−{gone.length} svc</span>
          <span style={{ color: 'var(--text3)', fontFamily: 'monospace', fontSize: 11 }}>
            {sample(gone.map(s => s.service))}
          </span>
        </span>
      )}
      {goneEdges.length > 0 && (
        <span title={goneEdges.map(e => `${e.caller} → ${e.callee}`).join('\n')}>
          <span className="badge b-err" style={{ marginRight: 6 }}>−{goneEdges.length} edge</span>
          <span style={{ color: 'var(--text3)', fontFamily: 'monospace', fontSize: 11 }}>
            {sample(goneEdges.map(e => `${e.caller}→${e.callee}`))}
          </span>
        </span>
      )}
    </div>
  );
}
