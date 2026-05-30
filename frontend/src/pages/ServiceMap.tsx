import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceMapGraph } from '@/components/ServiceMapGraph';
import { useServiceMap, useServiceNames } from '@/lib/queries';
import { fmtNum, hashColor } from '@/lib/utils';
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
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [samples, setSamples] = useState(200);
  const [focus, setFocus] = useState<string>('');
  // Picker input has its own state separate from focus so the
  // user can type to filter the datalist without immediately
  // re-focusing the graph on a partial match. Commit to focus
  // happens on selection / blur — pre-v0.4.93 the input was
  // bound directly to focus, which made every keystroke
  // re-trigger the graph layout AND the datalist filtered to
  // an exact match (browser dropdown then only showed that
  // one option, blocking further selection).
  const [pickerText, setPickerText] = useState<string>('');
  const [pickerOpen, setPickerOpen] = useState(false);
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  const [diff, setDiff] = useState<string>('');
  const since = (PRESETS.find(p => p.key === range.preset)?.secs ?? 900) + 's';

  const mapQ = useServiceMap(since, samples, diff || undefined);
  // Picker datalist source — pre-v0.5.0 the dropdown listed
  // only services that appeared in the sampled traces. With
  // sampleCount=200 this often dropped low-volume but
  // important services (cron jobs, batch workers) from the
  // selector. Pulling the canonical /api/services list means
  // every emitting service shows in the picker, even when its
  // map node is absent from the current sample. Falls back to
  // map nodes if the API hiccups.
  const namesQ = useServiceNames();
  const allServiceNames = useMemo(() => {
    const set = new Set<string>();
    if (namesQ.data?.names) {
      for (const n of namesQ.data.names) set.add(n);
    }
    for (const n of (mapQ.data?.nodes ?? [])) if (!n.kind) set.add(n.service);
    return Array.from(set).sort();
  }, [namesQ.data, mapQ.data]);
  // Auto-pick a focused service on first load so the operator
  // lands on a useful 1-hop view instead of the full graph (which
  // can look like a hairball on large clusters). Picks one of the
  // top-3 busiest real services (by spanCount) at random — that
  // way refreshes don't pin the same demo service, but the picks
  // stay relevant. Only fires once per session per visit; manual
  // selection / Clear focus disables further auto-picks.
  const [autoFocused, setAutoFocused] = useState(false);
  useEffect(() => {
    const nodes = mapQ.data?.nodes ?? [];
    if (autoFocused || focus || !mapQ.data || nodes.length === 0) return;
    const real = nodes
      .filter(n => !n.kind)
      .sort((a, b) => b.spanCount - a.spanCount)
      .slice(0, 3);
    if (real.length > 0) {
      const pick = real[Math.floor(Math.random() * real.length)];
      setFocus(pick.service);
      setAutoFocused(true);
    }
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
          {/* Picker — datalist autocomplete fed by the current
              map's services. Plain <input> over a custom
              dropdown so keyboard nav / clear / autocomplete
              all just work. */}
          <label style={{ fontSize: 12, color: 'var(--text2)' }}>Focus</label>
          <input list="svc-map-services"
                 aria-label="Focus map on a service"
                 placeholder={focus || 'select a service…'}
                 value={pickerOpen ? pickerText : focus}
                 onChange={e => {
                   setPickerText(e.target.value);
                   // Commit immediately when the typed value
                   // matches a service exactly — picking from
                   // the datalist drops the typed string in
                   // verbatim and the operator expects the
                   // graph to re-focus right away.
                   const match = data?.nodes.find(n => !n.kind && n.service === e.target.value);
                   if (match) setFocus(match.service);
                 }}
                 onFocus={() => { setPickerText(''); setPickerOpen(true); }}
                 // Click re-opens the picker even if the input
                 // was already focused — browsers don't fire a
                 // fresh focus event in that case, so without
                 // this an operator who navigates away with the
                 // keyboard and comes back can't see the full
                 // datalist again.
                 onMouseDown={() => { setPickerText(''); setPickerOpen(true); }}
                 onBlur={() => {
                   setPickerOpen(false);
                   // Empty value on blur = "no commit yet" — keep
                   // the existing focus. Typed value that doesn't
                   // match a service = also keep existing focus so
                   // a partial entry doesn't wipe the view.
                   const match = data?.nodes.find(n => !n.kind && n.service === pickerText);
                   if (match) setFocus(match.service);
                 }}
                 style={{
                   minWidth: 240, fontSize: 13,
                   padding: '4px 8px',
                   border: '1px solid var(--border)',
                   borderRadius: 6,
                   background: 'var(--bg1)',
                   color: 'var(--text)',
                 }} />
          <datalist id="svc-map-services">
            {/* Picker only offers REAL services — synthesised
                dep nodes (db:redis, ext:stripe, …) aren't
                first-class focus targets. They show up in
                the graph as neighbours of the services that
                call them. Pulls from /api/services so every
                emitting service shows even when the map's
                trace sample didn't include it. */}
            {allServiceNames.map(n =>
              <option key={n} value={n} />)}
          </datalist>
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
          <ServiceMapGraph
            data={filtered}
            focus={focus || null}
            hoverNode={hoverNode}
            onHoverNode={setHoverNode}
            onSelectNode={setFocus}
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
