import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { fmtNum, hashColor, timeRangeToNs } from '@/lib/utils';
import { api } from '@/lib/api';
import type {
  ServiceTopologyResponse, ServiceTopologyNode, ServiceTopologyEdge,
  TopologyResponse, TopologyNode,
  RootFlow,
  TimeRange,
  Problem,
} from '@/lib/types';

// /topology has three complementary views; URL ?view= picks one:
//   • service   — full backend interaction graph (v0.5.102)
//   • operation — depth-bounded op-level deep dive from one
//                 root service (v0.5.100/101)
//   • flows     — top business flows (root entry points) +
//                 click-to-focus subgraph (v0.5.103)
// Each view has its own controls; the topbar time range is
// shared across all three.
type View = 'service' | 'operation' | 'flows';

// Local view shape (v0.5.153). `local` distinguishes legacy
// localStorage entries surfaced for unauth'd sessions or pending
// migration from real server-backed views — they get a different
// icon and a no-network delete path.
type TopologyView = {
  id: string;
  name: string;
  queryString: string;
  ownerId: string;
  shared: boolean;
  local: boolean;
};

export default function TopologyPage() {
  const [params, setParams] = useSearchParams();
  const view = (params.get('view') as View) || 'service';
  // Default 24h so rarely-triggered interactions (cron jobs,
  // hourly batch flows, low-traffic endpoints called 1-2x/day)
  // still surface in the diagram. The pre-aggregated agg table
  // keeps 14 days, so widening the default is cheap. URL state
  // overrides per-bookmark.
  const preset = params.get('preset') || '24h';
  const [range, setRange] = useState<TimeRange>({ preset });

  useEffect(() => {
    if (range.preset && range.preset !== preset) {
      setParams(prev => {
        const p = new URLSearchParams(prev);
        p.set('preset', range.preset);
        return p;
      }, { replace: true });
    }
  }, [range, preset, setParams]);

  const setView = (v: View) => setParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('view', v);
    return p;
  }, { replace: true });

  // Saved views (v0.5.143 → v0.5.153). Originally per-browser via
  // localStorage; promoted to server-side so a team's "look at the
  // billing subgraph during incidents" view is one click for the
  // whole org rather than a Slack copy-paste. The backend
  // `saved_views` table already speaks the same shape (name +
  // page + queryString); admins can flip `shared=true` to publish
  // a view to the whole org (owner_id=''). Non-admin save = own
  // bucket. localStorage retained as a fallback for unauth'd
  // sessions and one-time migration on first successful fetch.
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const LEGACY_LS_KEY = 'coremetry-topology-views';
  const [saved, setSaved] = useState<TopologyView[]>([]);
  // Pull the user's + team-shared views on mount. If the user is
  // unauth'd, fall back to legacy localStorage so an offline /
  // logged-out preview still works.
  useEffect(() => {
    let cancelled = false;
    if (!user) {
      try {
        const raw = localStorage.getItem(LEGACY_LS_KEY);
        const arr: Array<{ name: string; qs: string }> = raw ? JSON.parse(raw) : [];
        if (!cancelled) setSaved(arr.map(v => ({
          id: 'ls:' + v.name, name: v.name, queryString: v.qs,
          ownerId: '', shared: false, local: true,
        })));
      } catch { /* ignore */ }
      return;
    }
    api.savedViews('topology')
      .then(rows => {
        if (cancelled) return;
        const remote: TopologyView[] = (rows ?? []).map(v => ({
          id: v.id, name: v.name, queryString: v.queryString,
          ownerId: v.ownerId, shared: v.ownerId === '', local: false,
        }));
        setSaved(remote);
        // One-time migration: if the server is empty for this
        // user AND localStorage has entries, push them up so the
        // operator's old browser bookmarks survive the upgrade.
        // Quietly — no UI prompt; if it fails the user still has
        // their localStorage copy to re-save manually.
        if (remote.length === 0) {
          try {
            const raw = localStorage.getItem(LEGACY_LS_KEY);
            const legacy: Array<{ name: string; qs: string }> = raw ? JSON.parse(raw) : [];
            if (legacy.length > 0) {
              Promise.allSettled(legacy.map(v =>
                api.createSavedView({ name: v.name, page: 'topology', queryString: v.qs })
              )).then(() => {
                // Refetch to surface the migrated rows with real IDs.
                api.savedViews('topology').then(r2 => {
                  if (cancelled) return;
                  setSaved((r2 ?? []).map(v => ({
                    id: v.id, name: v.name, queryString: v.queryString,
                    ownerId: v.ownerId, shared: v.ownerId === '', local: false,
                  })));
                  // Migration succeeded — drop the legacy blob so
                  // we don't keep re-migrating on every mount.
                  try { localStorage.removeItem(LEGACY_LS_KEY); } catch {}
                });
              });
            }
          } catch { /* ignore */ }
        }
      })
      .catch(() => { /* silent — server may be unreachable */ });
    return () => { cancelled = true; };
  }, [user]);

  const saveCurrent = async () => {
    const name = window.prompt('Save view as:', '');
    if (!name) return;
    const qs = params.toString();
    // Only admins see the "share" prompt — for everyone else the
    // view stays personal. Server enforces this regardless.
    let shared = false;
    if (isAdmin) {
      shared = window.confirm(
        `Save "${name}" as a team-shared view?\n\nOK = visible to everyone in the org\nCancel = personal only`
      );
    }
    try {
      const v = await api.createSavedView({
        name, page: 'topology', queryString: qs, shared,
      });
      setSaved(prev => [
        { id: v.id, name: v.name, queryString: v.queryString,
          ownerId: v.ownerId, shared: v.ownerId === '', local: false },
        ...prev.filter(p => p.name !== v.name || p.shared !== (v.ownerId === '')),
      ]);
    } catch (e) {
      alert('Failed to save view: ' + (e instanceof Error ? e.message : String(e)));
    }
  };
  const loadView = (qs: string) => {
    setParams(new URLSearchParams(qs), { replace: true });
  };
  const deleteView = async (id: string) => {
    if (id.startsWith('ls:')) {
      // legacy localStorage entry — strip from in-memory + LS.
      setSaved(prev => prev.filter(v => v.id !== id));
      try {
        const name = id.slice(3);
        const raw = localStorage.getItem(LEGACY_LS_KEY);
        const arr: Array<{ name: string; qs: string }> = raw ? JSON.parse(raw) : [];
        localStorage.setItem(LEGACY_LS_KEY, JSON.stringify(arr.filter(v => v.name !== name)));
      } catch { /* ignore */ }
      return;
    }
    try {
      await api.deleteSavedView(id);
      setSaved(prev => prev.filter(v => v.id !== id));
    } catch (e) {
      alert('Failed to delete view: ' + (e instanceof Error ? e.message : String(e)));
    }
  };

  return (
    <>
      <Topbar title="Topology" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 4, marginBottom: 12, alignItems: 'center' }}>
          {(['service', 'operation', 'flows'] as View[]).map(v => (
            <button key={v} type="button" onClick={() => setView(v)}
              className={view === v ? '' : 'sec'}
              style={{ fontSize: 12, padding: '5px 14px' }}>
              {v === 'service' ? 'Service topology'
                : v === 'operation' ? 'Operation deep-dive'
                : 'Business flows'}
            </button>
          ))}
          {/* Saved views — dropdown + save. Sits to the right of
              the view tabs so the operator's eye reaches it last
              (load is rarer than view-switch). */}
          <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            {saved.length > 0 && (() => {
              const sharedViews = saved.filter(v => v.shared);
              const personalViews = saved.filter(v => !v.shared);
              return (
                <select onChange={e => {
                  if (e.target.value) loadView(e.target.value);
                  e.target.value = '';
                }} defaultValue="" style={{ fontSize: 11, padding: '3px 6px' }}>
                  <option value="">★ Saved views ({saved.length})</option>
                  {sharedViews.length > 0 && (
                    <optgroup label="Shared with team">
                      {sharedViews.map(v => (
                        <option key={v.id} value={v.queryString}>◍ {v.name}</option>
                      ))}
                    </optgroup>
                  )}
                  {personalViews.length > 0 && (
                    <optgroup label="My views">
                      {personalViews.map(v => (
                        <option key={v.id} value={v.queryString}>
                          {v.local ? '⌂ ' : '★ '}{v.name}
                        </option>
                      ))}
                    </optgroup>
                  )}
                </select>
              );
            })()}
            <button type="button" className="sec" onClick={saveCurrent}
              style={{ fontSize: 11, padding: '4px 10px' }}
              title={isAdmin
                ? 'Save the current URL state — admins can publish to the team'
                : 'Save the current URL state to your account'}>
              ★ Save view
            </button>
            {saved.length > 0 && (
              <button type="button" className="sec"
                onClick={() => {
                  const name = window.prompt('Delete which saved view? (name)', saved[0].name);
                  if (!name) return;
                  const match = saved.find(v => v.name === name);
                  if (!match) {
                    alert(`No view named "${name}"`);
                    return;
                  }
                  deleteView(match.id);
                }}
                style={{ fontSize: 11, padding: '4px 8px', color: 'var(--err)' }}
                title="Remove a saved view by name (admin needed for shared views)">
                ×
              </button>
            )}
          </span>
        </div>
        {view === 'service'   && <ServiceView range={range} />}
        {view === 'operation' && <OperationView params={params} setParams={setParams} range={range} />}
        {view === 'flows'     && <FlowsView range={range} />}
      </div>
    </>
  );
}

// ── View 1: Service topology ─────────────────────────────────

function ServiceView({ range }: { range: TimeRange }) {
  const navigate = useNavigate();
  // Stable callback so the SVG memo treats it as identity-equal
  // across renders. Inline arrows would force a re-render of every
  // node g on each parent re-render — at 200 nodes that's a
  // perceptible jank on slow laptops.
  // Click target depends on node kind: services go to their
  // detail page; infra nodes pivot to /databases or /messaging
  // so the operator can see per-instance latency / throughput
  // (the topology agg collapses by db_system/msg_system, those
  // pages keep the per-instance breakdown).
  const onNodeClick = useCallback((node: ServiceTopologyNode) => {
    switch (node.kind) {
      case 'service':
        navigate(`/service?name=${encodeURIComponent(node.name)}`);
        break;
      case 'db':
        navigate(`/databases?system=${encodeURIComponent(node.name)}`);
        break;
      case 'queue':
        navigate(`/messaging?system=${encodeURIComponent(node.name)}`);
        break;
      case 'external':
        // External APIs have no detail page (no traces past the
        // outbound boundary). Stay on the topology view.
        break;
    }
  }, [navigate]);
  const [data, setData] = useState<ServiceTopologyResponse | null | undefined>(undefined);
  const [selectedEdge, setSelectedEdge] = useState<ServiceTopologyEdge | null>(null);
  // v0.5.152 — service name whose open-problems drawer is open
  // (clicked the red ring or "!" badge). Null = drawer closed.
  const [incidentDrawerFor, setIncidentDrawerFor] = useState<string | null>(null);
  // v0.5.288 — topN / focus / focusHops / focusDir all promoted
  // from useState to URL state via useSearchParams. Lets a saved
  // view ("eu-west payment-svc downstream, 2 hops") survive page
  // reloads and become a shareable link. Same approach the
  // OperationView already uses for root/root_op/depth.
  const [params, setParams] = useSearchParams();
  const topN     = Math.max(10, Math.min(200, parseInt(params.get('top') || '30', 10) || 30));
  const focus    = params.get('focus') || '';
  const focusHops = Math.max(1, Math.min(4, parseInt(params.get('hops') || '1', 10) || 1));
  const focusDir = (params.get('dir') === 'both') ? 'both' as const : 'down' as const;
  const setURLParam = useCallback((key: string, v: string | null) => {
    setParams(prev => {
      const p = new URLSearchParams(prev);
      if (v == null || v === '') p.delete(key); else p.set(key, v);
      return p;
    }, { replace: true });
  }, [setParams]);
  const setTopN     = (v: number) => setURLParam('top',   v === 30  ? null : String(v));
  const setFocus    = (v: string) => setURLParam('focus', v);
  const setFocusHops = (v: number) => setURLParam('hops',  v === 1   ? null : String(v));
  const setFocusDir = (v: 'down' | 'both') => setURLParam('dir', v === 'down' ? null : v);
  // Esc clears the focus and pops the diagram back to the
  // top-N overview (v0.5.173). Guard against firing in editable
  // inputs (search box etc.) — global keyboard layer already
  // pauses there.
  useEffect(() => {
    if (!focus) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setFocus('');
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
    // setFocus is stable via setParams identity but is recreated
    // each render; deps on focus only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus]);
  // Substring search across nodes in the current visible subgraph.
  // Doesn't filter the diagram — it highlights matches so the
  // operator can find a service inside a 100-node graph without
  // dropping context. Empty = all nodes at normal opacity.
  const [search, setSearch] = useState('');
  // Incidents overlay — services with at least one open problem
  // get a red highlight on their node so an operator can spot
  // trouble without leaving the topology page. Refreshes on
  // range change; client-side derived from a single /api/problems
  // round-trip.
  const [incidentServices, setIncidentServices] = useState<Set<string>>(new Set());
  useEffect(() => {
    api.problems({ status: 'open', limit: 500 })
      .then(rows => {
        const s = new Set<string>();
        (rows ?? []).forEach(p => { if (p.service) s.add(p.service); });
        setIncidentServices(s);
      })
      .catch(() => setIncidentServices(new Set()));
  }, [range]);
  // Service catalog metadata (v0.5.148). Fetched once — entries
  // are operator-curated, rarely change, and a single round-trip
  // is enough for the whole diagram. Used to enrich hover tooltips
  // with owner / SRE team so the operator on a topology page
  // knows who to ping without leaving the view.
  // v0.5.155 widened to carry runbookUrl + oncallUrl so the node
  // can expose a one-click jump to the team's playbook from the
  // topology view itself.
  const [metaByService, setMetaByService] = useState<Record<string, {
    ownerTeam?: string; sreTeam?: string; chatChannel?: string;
    runbookUrl?: string; oncallUrl?: string;
  }>>({});
  useEffect(() => {
    api.servicesMetadata()
      .then(m => setMetaByService(m ?? {}))
      .catch(() => setMetaByService({}));
  }, []);
  // Noise toggle. v0.5.338 — operator preference flipped the
  // default to ON: full unfiltered topology is the landing
  // view; ?noise=hide opts into the cleaner business-flow
  // shape (self-edges, /health|/metrics infra ops, sub-0.5%
  // long tail dropped at the backend). Old shareable links
  // with `?noise=show` still resolve correctly because we
  // only special-case the explicit "hide" value.
  const noiseShow = params.get('noise') !== 'hide';
  const setNoiseShow = (v: boolean) => setURLParam('noise', v ? null : 'hide');
  // v0.5.412 — live traffic flow animation. URL-shareable
  // (?flow=on); default off so the static topology stays the
  // unsurprising default. When on, edges get a CSS-driven
  // stroke-dashoffset animation whose duration is inversely
  // proportional to log(calls) — busy edges flow visibly faster
  // than idle ones.
  const flowOn = params.get('flow') === 'on';
  const setFlowOn = (v: boolean) => setURLParam('flow', v ? 'on' : null);
  // v0.5.413 — time-shift slider. Lets the operator view the
  // topology AS OF X minutes ago without changing the underlying
  // time range. Datadog Replay / Honeycomb Now-bar pattern.
  // Value is "minutes ago" — 0 = live. Capped at 7d (10080 min)
  // to match topology_edges_5m retention.
  const shiftMin = Math.max(0, Math.min(10080, parseInt(params.get('shift') || '0', 10) || 0));
  const setShiftMin = (v: number) => setURLParam('shift', v > 0 ? String(v) : null);
  // v0.5.312 — protocol filter. Empty = show all. Selected
  // = show only those. Comma-separated in URL for sharability.
  // Protocols come from edge.protocol: http / rpc / db / kafka /
  // internal. Stored as a Set for O(1) lookup.
  const protoCSV = params.get('proto') || '';
  const protoFilter = new Set(
    protoCSV.split(',').map(s => s.trim()).filter(Boolean),
  );
  const toggleProto = (p: string) => {
    const next = new Set(protoFilter);
    if (next.has(p)) next.delete(p); else next.add(p);
    setURLParam('proto', next.size === 0 ? null : [...next].join(','));
  };
  useEffect(() => {
    setData(undefined);
    // v0.5.413 — shift window back by `shiftMin` minutes when
    // the slider is non-zero. We keep the existing range
    // DURATION (1h stays 1h) but slide the whole window back
    // so the operator sees the SAME shape of state at a past
    // point. Same idiom Datadog Replay uses on its service map.
    let { from, to } = timeRangeToNs(range);
    if (shiftMin > 0) {
      const shiftNs = BigInt(shiftMin) * 60_000_000_000n;
      from = Number(BigInt(from) - shiftNs);
      to = Number(BigInt(to) - shiftNs);
    }
    api.serviceTopology({ from, to, noise: noiseShow ? 'show' : undefined })
      // Normalise: nil slices from older backend / empty windows
      // marshal as JSON null, which crashes data.edges.forEach.
      // Belt-and-braces with the server-side fix.
      .then(d => setData({
        ...d,
        nodes: d?.nodes ?? [],
        edges: d?.edges ?? [],
      }))
      .catch(() => setData(null));
  }, [range, noiseShow, shiftMin]);

  // Compute the visible subgraph from the raw response based on
  // the two controls. All filtering is client-side because the
  // server already caps edges at 5k; computing here lets the
  // operator slide topN / pick focus without a round-trip.
  const visible = useMemo(() => {
    if (!data) return null;
    if (focus) {
      // BFS from focus up to focusHops layers. v0.5.282 — default
      // 'down' only follows outgoing edges (callees); 'both'
      // keeps the legacy bidirectional expansion for blame-style
      // walks. Edges to/from nodes outside the kept set get
      // dropped so the diagram doesn't carry phantom stubs.
      const keepNodes = new Set<string>([focus]);
      let frontier = new Set<string>([focus]);
      for (let h = 0; h < focusHops; h++) {
        const next = new Set<string>();
        data.edges.forEach(e => {
          if (frontier.has(e.parentService) && !keepNodes.has(e.childNode)) {
            next.add(e.childNode);
          }
          if (focusDir === 'both'
              && frontier.has(e.childNode) && !keepNodes.has(e.parentService)) {
            next.add(e.parentService);
          }
        });
        next.forEach(id => keepNodes.add(id));
        frontier = next;
      }
      const keepEdges = data.edges.filter(e =>
        keepNodes.has(e.parentService) && keepNodes.has(e.childNode));
      return {
        nodes: data.nodes.filter(n => keepNodes.has(n.id)),
        edges: keepEdges,
      };
    }
    // Top-N pick: rank nodes by total in+out call volume,
    // keep the heaviest N, then drop edges whose endpoints aren't
    // both in the kept set.
    const score = new Map<string, number>();
    const bump = (id: string, calls: number) => {
      score.set(id, (score.get(id) ?? 0) + calls);
    };
    data.edges.forEach(e => {
      bump(e.parentService, e.calls);
      bump(e.childNode, e.calls);
    });
    const ranked = data.nodes.slice().sort((a, b) =>
      (score.get(b.id) ?? 0) - (score.get(a.id) ?? 0));
    const keptSet = new Set(ranked.slice(0, topN).map(n => n.id));
    const keepEdges = data.edges.filter(e =>
      keptSet.has(e.parentService) && keptSet.has(e.childNode));
    return {
      nodes: data.nodes.filter(n => keptSet.has(n.id)),
      edges: keepEdges,
    };
  }, [data, topN, focus, focusHops, focusDir]);

  // v0.5.312 — protocol filter applied to the post-topN visible
  // set. Done outside the topN memo so toggling a protocol
  // doesn't force a re-rank of the heaviest-N pick.
  const visibleFiltered = useMemo(() => {
    if (!visible || protoFilter.size === 0) return visible;
    const keepEdges = visible.edges.filter(e => protoFilter.has(e.protocol));
    const keepIds = new Set<string>();
    keepEdges.forEach(e => { keepIds.add(e.parentService); keepIds.add(e.childNode); });
    return {
      nodes: visible.nodes.filter(n => keepIds.has(n.id)),
      edges: keepEdges,
    };
    // protoFilter is a Set recreated each render; we read its
    // contents via protoCSV which IS stable across renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, protoCSV]);

  const layout = useMemo(
    () => layerServices(
      visibleFiltered ? { ...data!, nodes: visibleFiltered.nodes, edges: visibleFiltered.edges } : null,
      focus || undefined,
      focusDir,
    ),
    [visibleFiltered, data, focus, focusDir]
  );

  if (data === undefined) return <Spinner />;
  if (data === null) return <Empty icon="✗" title="Failed to load topology" />;
  if (data.nodes.length === 0) {
    return <Empty icon="◇" title="No interactions in this window">Pick a wider time range or wait for traces to flow.</Empty>;
  }
  const totalNodes = data.nodes.length;
  const totalEdges = data.edges.length;
  const showingNodes = visibleFiltered?.nodes.length ?? 0;
  const showingEdges = visibleFiltered?.edges.length ?? 0;
  // Build the focus-picker options from the full node list so the
  // operator can pivot to any service even when it's not in the
  // current top-N slice.
  const focusOptions = data.nodes
    .filter(n => n.kind === 'service')
    .map(n => n.name)
    .sort();
  return (
    <>
      <div className="controls" style={{ marginBottom: 12, gap: 12, flexWrap: 'wrap' }}>
        <label style={{ fontSize: 12, color: 'var(--text2)' }}>Focus on</label>
        <select value={focus} onChange={e => setFocus(e.target.value)}
          style={{ fontSize: 12, padding: '3px 6px', minWidth: 180 }}>
          <option value="">— top services —</option>
          {focusOptions.map(s => <option key={s} value={s}>{s}</option>)}
        </select>
        {!focus && (
          <>
            <label style={{ fontSize: 12, color: 'var(--text2)' }}>Top services</label>
            <input type="range" min={10} max={Math.min(200, totalNodes)} value={topN}
              onChange={e => setTopN(parseInt(e.target.value, 10))}
              style={{ width: 140 }} />
            <span style={{ fontFamily: 'monospace', fontSize: 12, color: 'var(--text)' }}>{topN}</span>
          </>
        )}
        {focus && (
          <>
            <label style={{ fontSize: 12, color: 'var(--text2)' }}>Hops</label>
            <input type="range" min={1} max={4} value={focusHops}
              onChange={e => setFocusHops(parseInt(e.target.value, 10))}
              style={{ width: 100 }}
              title="How many hops outward from the focused service to include" />
            <span style={{ fontFamily: 'monospace', fontSize: 12, color: 'var(--text)' }}>{focusHops}</span>
            {/* v0.5.282 — direction toggle. Default 'down' so the
                anchor sits at column 0 and only callees fan out
                to the right. 'both' restores the legacy
                bidirectional view (callers on the left) for
                blame-style walks. */}
            <label style={{ fontSize: 12, color: 'var(--text2)', marginLeft: 4 }}>Direction</label>
            <select value={focusDir} onChange={e => setFocusDir(e.target.value as 'down' | 'both')}
              style={{ fontSize: 12, padding: '3px 6px' }}
              title="Downstream-only puts the focused service at column 0 and shows what it calls. Bidirectional also includes callers on the left.">
              <option value="down">↳ downstream</option>
              <option value="both">⇆ both</option>
            </select>
          </>
        )}
        <input type="search" placeholder="Search…" value={search}
          onChange={e => setSearch(e.target.value)}
          style={{ fontSize: 12, padding: '3px 8px', width: 140 }}
          title="Highlight nodes matching this text" />
        {/* v0.5.310 — noise toggle. OFF (default) = OTel-native
            business-flow view: backend drops self-loops, infra
            ops (/health, /metrics, /-/, ping), and sub-0.5%
            volume long-tail edges. ON = legacy unfiltered. */}
        <label style={{
          display: 'inline-flex', alignItems: 'center', gap: 4,
          fontSize: 12, color: 'var(--text2)',
        }} title="Show self-edges, /health probes, /metrics scrapes, and sub-0.5%-volume long-tail edges. OFF by default for a clean business-flow view.">
          <input type="checkbox" checked={noiseShow}
            onChange={e => setNoiseShow(e.target.checked)} />
          Show noise
        </label>
        {/* v0.5.412 — live-flow toggle. Off by default; when on,
            edges visually pulse along the path direction at a
            speed proportional to call volume. Datadog Live
            Topology pattern. */}
        <label style={{
          display: 'inline-flex', alignItems: 'center', gap: 4,
          fontSize: 12, color: 'var(--text2)',
        }} title="Animate edges so the dashes flow parent → child. Speed proportional to call volume.">
          <input type="checkbox" checked={flowOn}
            onChange={e => setFlowOn(e.target.checked)} />
          Live flow
        </label>
        {/* v0.5.413 — time-shift slider. Slides the topology
            query window back in time without changing range
            duration. 0 = live; 60 = topology as of 1 hour ago.
            Capped at 7d (10080 min) to match the MV retention. */}
        <div style={{
          display: 'inline-flex', alignItems: 'center', gap: 6,
          fontSize: 12, color: 'var(--text2)', minWidth: 220,
        }}>
          <span title="Shift the topology view back in time. 0 = live; up to 7 days back, snapped to 5-minute buckets.">
            ⏮
          </span>
          <input type="range" min={0} max={1440} step={5}
            value={Math.min(shiftMin, 1440)}
            onChange={e => setShiftMin(Number(e.target.value))}
            style={{ width: 140 }}
            title={shiftMin === 0
              ? 'Live (now)'
              : `As of ${formatShift(shiftMin)} ago`} />
          <span style={{
            color: shiftMin > 0 ? 'var(--warn, #facc15)' : 'var(--text3)',
            minWidth: 70, fontFamily: 'ui-monospace, monospace',
            fontSize: 11,
          }}>
            {shiftMin === 0 ? 'live' : `-${formatShift(shiftMin)}`}
          </span>
          {shiftMin > 0 && (
            <button type="button"
              onClick={() => setShiftMin(0)}
              title="Reset to live (now)"
              style={{
                all: 'unset', cursor: 'pointer',
                color: 'var(--accent2)', fontSize: 11, padding: '0 4px',
              }}>×</button>
          )}
        </div>
        {/* v0.5.312 — protocol filter chips. Empty filter = all
            visible. Click a chip to scope; click again to unscope.
            Saved-view friendly via URL ?proto=http,db. */}
        <span style={{ fontSize: 12, color: 'var(--text2)', marginLeft: 4 }}>Proto</span>
        {(['http', 'rpc', 'db', 'kafka', 'internal'] as const).map(p => {
          const picked = protoFilter.size === 0 || protoFilter.has(p);
          const palette: Record<string, string> = {
            http: '#4A90D9', rpc: '#8A6FB5', db: '#6c8ebf',
            kafka: '#d6b656', internal: '#999',
          };
          const c = palette[p];
          return (
            <button key={p} type="button"
              onClick={() => toggleProto(p)}
              title={protoFilter.size === 0
                ? `Click to show only ${p.toUpperCase()} edges`
                : picked ? `Hide ${p.toUpperCase()} edges` : `Show ${p.toUpperCase()} edges too`}
              style={{
                all: 'unset', cursor: 'pointer',
                display: 'inline-flex', alignItems: 'center', gap: 4,
                padding: '2px 8px', borderRadius: 10, fontSize: 11,
                border: `1px solid ${picked ? c : 'var(--border)'}`,
                background: picked ? `color-mix(in srgb, ${c} 18%, transparent)` : 'transparent',
                color: picked ? 'var(--text)' : 'var(--text3)',
                fontWeight: picked ? 600 : 400,
                opacity: picked ? 1 : 0.55,
              }}>
              <span style={{
                width: 7, height: 7, borderRadius: '50%',
                background: c, display: 'inline-block',
              }} />
              {p}
            </button>
          );
        })}
        <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
          {focus
            ? `Focused on ${focus}: ${showingNodes} nodes / ${showingEdges} edges`
            : `${showingNodes}/${totalNodes} nodes · ${showingEdges}/${totalEdges} edges`}
        </span>
        <a href={api.serviceTopologyDrawIOURL({ from: data.from, to: data.to })}
          className="sec"
          style={{ fontSize: 11, padding: '4px 10px', textDecoration: 'none' }}
          title="Download full service topology as draw.io">
          ↓ draw.io
        </a>
      </div>
      {data.truncated && (
        <div style={{
          background: 'rgba(212,165,55,0.12)', border: '1px solid rgba(212,165,55,0.4)',
          borderRadius: 4, padding: '6px 10px', marginBottom: 10,
          color: 'var(--text2)', fontSize: 11,
        }}>
          Edge query hit its 5k cap — heaviest strands only. Narrow the time range for full coverage.
        </div>
      )}
      {visibleFiltered && visibleFiltered.nodes.length === 0 && (
        <Empty icon="◇" title={focus ? `No interactions for ${focus} in this window` : 'No matches'} />
      )}
      {visibleFiltered && visibleFiltered.nodes.length > 0 && (
        <>
          <ServiceTopologySVG
            nodes={visibleFiltered.nodes} edges={visibleFiltered.edges} layout={layout}
            onEdgeClick={setSelectedEdge} search={search}
            incidentServices={incidentServices}
            onNodeClick={onNodeClick}
            onIncidentClick={(n) => setIncidentDrawerFor(n.name)}
            metaByService={metaByService}
            anchor={focus || undefined}
            flowOn={flowOn}
          />
          {selectedEdge && (
            <EdgeDetailPanel edge={selectedEdge} onClose={() => setSelectedEdge(null)} range={range} simplified={!!focus} />
          )}
          {incidentDrawerFor && (
            <IncidentDrawer service={incidentDrawerFor}
              onClose={() => setIncidentDrawerFor(null)} />
          )}
        </>
      )}
    </>
  );
}

// ── View 2: Operation deep-dive ──────────────────────────────

function OperationView({ params, setParams, range }: {
  params: URLSearchParams;
  setParams: (n: URLSearchParams | ((p: URLSearchParams) => URLSearchParams), opts?: { replace?: boolean }) => void;
  range: TimeRange;
}) {
  const root   = params.get('root')    || '';
  const rootOp = params.get('root_op') || '';
  const depth  = Math.max(1, Math.min(6, parseInt(params.get('depth') || '3', 10) || 3));
  const [data, setData] = useState<TopologyResponse | null | undefined>(undefined);
  const [ops, setOps]   = useState<string[]>([]);

  useEffect(() => {
    if (!root) { setData(null); return; }
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.topology({ root, root_op: rootOp || undefined, depth, from, to })
      .then(d => setData({ ...d, nodes: d?.nodes ?? [], edges: d?.edges ?? [] }))
      .catch(() => setData(null));
  }, [root, rootOp, depth, range]);

  // Load ops for the selected service so the op picker can be
  // populated. Refreshes when the service or time range changes.
  useEffect(() => {
    if (!root) { setOps([]); return; }
    const { from, to } = timeRangeToNs(range);
    api.topologyOps({ service: root, from, to })
      .then(d => setOps(d.ops ?? []))
      .catch(() => setOps([]));
  }, [root, range]);

  const setRoot = (v: string) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('root', v); else p.delete('root');
    p.delete('root_op'); // ops are service-scoped; reset on service change.
    return p;
  }, { replace: true });
  const setRootOp = (v: string) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('root_op', v); else p.delete('root_op');
    return p;
  }, { replace: true });
  const setDepth = (v: number) => setParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('depth', String(v));
    return p;
  }, { replace: true });

  const layers = useMemo(() => layerOpNodes(data, root, rootOp || undefined), [data, root, rootOp]);
  const drawioHref = data && root
    ? api.topologyDrawIOURL({ root, depth, from: data.from, to: data.to }) : '';

  // v0.5.285 — service color filter; v0.5.288 — promoted to URL
  // state (`color` param) so saved views remember "only foo-svc"
  // and a shareable link lands on the same isolated flow.
  const colorFilter = params.get('color') || null;
  const setColorFilter = (v: string | null) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('color', v); else p.delete('color');
    return p;
  }, { replace: true });
  const uniqueServices = useMemo(() => {
    if (!data) return [] as string[];
    const set = new Set<string>();
    data.nodes.forEach(n => set.add(n.service));
    return Array.from(set).sort();
  }, [data]);

  return (
    <>
      <div className="controls" style={{ marginBottom: 12, gap: 12, flexWrap: 'wrap' }}>
        <label style={{ fontSize: 12, color: 'var(--text2)' }}>Root service</label>
        <ServicePicker value={root} onChange={setRoot} placeholder="Pick a service…" width={220} />
        {root && (
          <>
            <label style={{ fontSize: 12, color: 'var(--text2)' }}>Operation</label>
            <select value={rootOp} onChange={e => setRootOp(e.target.value)}
              style={{ fontSize: 12, padding: '3px 6px', minWidth: 180 }}>
              <option value="">— all ops —</option>
              {ops.map(o => <option key={o} value={o}>{o}</option>)}
            </select>
          </>
        )}
        <label style={{ fontSize: 12, color: 'var(--text2)' }}>Depth</label>
        <input type="range" min={1} max={6} value={depth}
               onChange={e => setDepth(parseInt(e.target.value, 10))}
               style={{ width: 120 }} />
        <span style={{ fontFamily: 'monospace', fontSize: 12, color: 'var(--text)' }}>{depth}</span>
        {drawioHref && (
          <a href={drawioHref} className="sec"
             style={{ fontSize: 11, padding: '4px 10px', textDecoration: 'none', marginLeft: 'auto' }}
             title="Download as draw.io diagram">
            ↓ draw.io
          </a>
        )}
      </div>
      {!root && (
        <Empty icon="⋔" title="Pick a root service">
          Operation deep-dive expands the op-level call graph downstream from one service.
          Slide depth to widen the view.
        </Empty>
      )}
      {root && data === undefined && <Spinner />}
      {root && data === null && <Empty icon="✗" title="Failed to load topology" />}
      {root && data && data.nodes.length === 0 && (
        <Empty icon="◇" title="No outgoing calls in this window">
          Pick a wider time range or a different root service.
        </Empty>
      )}
      {root && data && data.nodes.length > 0 && (
        <>
          {data.truncated && (
            <div style={{
              background: 'rgba(212,165,55,0.12)', border: '1px solid rgba(212,165,55,0.4)',
              borderRadius: 4, padding: '6px 10px', marginBottom: 10,
              color: 'var(--text2)', fontSize: 11,
            }}>
              Edge query hit its 50k cap — view shows the heaviest edges only.
            </div>
          )}
          <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
            {data.nodes.length} nodes · {data.edges.length} edges · depth {data.depth}
          </div>
          {uniqueServices.length > 1 && (
            <div style={{
              display: 'flex', alignItems: 'center', gap: 6,
              flexWrap: 'wrap', marginBottom: 8,
              padding: '6px 0',
              borderTop: '1px dashed var(--border)',
              borderBottom: '1px dashed var(--border)',
            }}>
              <span style={{
                fontSize: 11, color: 'var(--text3)',
                textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 700,
                marginRight: 4,
              }}>Filter by service color</span>
              {uniqueServices.map(svc => {
                const isPicked = colorFilter === svc;
                const c = hashColor(svc);
                return (
                  <button key={svc} type="button"
                    onClick={() => setColorFilter(isPicked ? null : svc)}
                    title={isPicked ? `Showing ${svc} only — click to clear` : `Show only ${svc}'s ops`}
                    style={{
                      all: 'unset', cursor: 'pointer',
                      display: 'inline-flex', alignItems: 'center', gap: 5,
                      padding: '2px 8px', borderRadius: 12, fontSize: 11,
                      border: `1px solid ${isPicked ? c : 'var(--border)'}`,
                      background: isPicked
                        ? `color-mix(in srgb, ${c} 22%, transparent)`
                        : 'transparent',
                      color: isPicked ? 'var(--text)' : 'var(--text2)',
                      fontWeight: isPicked ? 700 : 500,
                      opacity: colorFilter && !isPicked ? 0.55 : 1,
                    }}>
                    <span style={{
                      width: 9, height: 9, borderRadius: '50%',
                      background: c, display: 'inline-block',
                    }} />
                    {svc}
                  </button>
                );
              })}
              {colorFilter && (
                <button type="button" onClick={() => setColorFilter(null)}
                  style={{
                    all: 'unset', cursor: 'pointer', fontSize: 11,
                    color: 'var(--text3)', marginLeft: 4,
                  }}
                  title="Clear color filter">✕ clear</button>
              )}
            </div>
          )}
          <OpTopologySVG layers={layers} edges={data.edges}
            anchor={rootOp ? `${root}|${rootOp}` : undefined}
            colorFilter={colorFilter} />
        </>
      )}
    </>
  );
}

// ── View 3: Business flows ───────────────────────────────────

function FlowsView({ range }: { range: TimeRange }) {
  const [flows, setFlows] = useState<RootFlow[] | null | undefined>(undefined);
  const [picked, setPicked] = useState<RootFlow | null>(null);
  const [pickedData, setPickedData] = useState<ServiceTopologyResponse | null | undefined>(undefined);
  const [selectedEdge, setSelectedEdge] = useState<ServiceTopologyEdge | null>(null);
  // Operator-controllable cap on the flow list. Default 100
  // covers most installs; slider goes up to 200 (server-side
  // hard cap) for the rare deployment with hundreds of distinct
  // root endpoints. Pre-aggregated table, so widening is cheap.
  const [top, setTop] = useState(100);
  const [search, setSearch] = useState('');
  // v0.5.290 — when a flow is picked and its subgraph spans
  // multiple services, the operator can isolate one downstream
  // chain by clicking its hashColor chip. Same plumbing as the
  // OperationView color filter (v0.5.285), reused via
  // ServiceTopologySVG's new colorFilter prop. Cleared on
  // pick change so picking a fresh flow starts unfiltered.
  const [flowColorFilter, setFlowColorFilter] = useState<string | null>(null);
  useEffect(() => { setFlowColorFilter(null); }, [picked]);

  // v0.5.272 — memoised time-range tuple for the per-flow
  // draw.io export <a href={…}> below. Without it, the bare
  // timeRangeToNs(range) inside the IIFE re-computed now() on
  // every render, producing a fresh URL each pass (link
  // flicker + downstream perf trap). Memo gates on range
  // identity.
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    setFlows(undefined);
    setPicked(null);
    const { from, to } = timeRangeToNs(range);
    api.topologyFlows({ top, from, to }).then(d => setFlows(d.flows ?? []))
      .catch(() => setFlows(null));
  }, [range, top]);

  useEffect(() => {
    if (!picked) { setPickedData(undefined); return; }
    setPickedData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.topologyFlow({
      root_service: picked.rootService, root_op: picked.rootOp, from, to,
    })
      .then(d => setPickedData({ ...d, nodes: d?.nodes ?? [], edges: d?.edges ?? [] }))
      .catch(() => setPickedData(null));
  }, [picked, range]);

  if (flows === undefined) return <Spinner />;
  if (flows === null) return <Empty icon="✗" title="Failed to load flows" />;
  if (flows.length === 0) {
    return <Empty icon="◇" title="No root flows in this window">
      Pick a wider time range — flows are anchored on root spans which need at least one trace.
    </Empty>;
  }

  // Client-side substring filter so an operator can narrow a
  // 200-flow list to "show me everything POST /payment".
  const term = search.trim().toLowerCase();
  const visibleFlows = !term
    ? flows
    : flows.filter(f =>
        f.rootOp.toLowerCase().includes(term)
        || f.rootService.toLowerCase().includes(term));

  return (
    <>
      {!picked && (
        <>
          <div className="controls" style={{ marginBottom: 12, gap: 12, flexWrap: 'wrap' }}>
            <input type="search" placeholder="Search flows…"
              value={search} onChange={e => setSearch(e.target.value)}
              style={{ fontSize: 12, padding: '3px 8px', width: 220 }}
              title="Substring match on root op or root service" />
            <label style={{ fontSize: 12, color: 'var(--text2)' }}>Show top</label>
            <input type="range" min={20} max={200} step={10} value={top}
              onChange={e => setTop(parseInt(e.target.value, 10))}
              style={{ width: 140 }}
              title="How many root flows to fetch — backend cap is 200" />
            <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{top}</span>
            <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
              {visibleFlows.length}/{flows.length} flows
            </span>
          </div>
          <FlowsByService flows={visibleFlows} hasSearch={!!term} onPick={setPicked} />
        </>
      )}
      {picked && (
        <>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 12 }}>
            <button type="button" className="sec"
              onClick={() => setPicked(null)}
              style={{ fontSize: 11, padding: '3px 10px' }}>
              ← All flows
            </button>
            <span style={{ fontWeight: 700, fontSize: 13 }}>{picked.rootOp}</span>
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              {picked.rootService} · {fmtNum(picked.traceCount)} traces
              {picked.p99Ns !== undefined && picked.p99Ns > 0 && (() => {
                const p99Ms = picked.p99Ns / 1e6;
                const label = p99Ms >= 1000
                  ? `${(p99Ms / 1000).toFixed(2)} s`
                  : `${p99Ms.toFixed(0)} ms`;
                return <> · p99 {label}</>;
              })()}
            </span>
            {/* Per-flow draw.io export (v0.5.145). Same window as
                the inline view; backend reuses the service-level
                XML builder restricted to this flow's traces. */}
            <a href={api.flowTopologyDrawIOURL({
                 root_service: picked.rootService,
                 root_op:      picked.rootOp,
                 from: rangeNs.from, to: rangeNs.to,
               })}
               className="sec"
               style={{
                 marginLeft: 'auto', fontSize: 11, padding: '4px 10px',
                 textDecoration: 'none',
               }}
               title="Download this flow as draw.io">
              ↓ draw.io
            </a>
          </div>
          {pickedData === undefined && <Spinner />}
          {pickedData === null && <Empty icon="✗" title="Failed to load flow" />}
          {pickedData && pickedData.nodes.length === 0 && (
            <Empty icon="◇" title="Single-service flow — no outgoing calls" />
          )}
          {pickedData && pickedData.nodes.length > 0 && (
            <>
              <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
                {pickedData.nodes.length} nodes · {pickedData.edges.length} edges
              </div>
              {pickedData.nodes.length > 2 && (
                <FlowColorFilterStrip
                  nodes={pickedData.nodes}
                  value={flowColorFilter}
                  onChange={setFlowColorFilter} />
              )}
              <ServiceTopologySVG
                nodes={pickedData.nodes} edges={pickedData.edges}
                layout={layerServices(pickedData)}
                onEdgeClick={setSelectedEdge}
                colorFilter={flowColorFilter}
              />
              {selectedEdge && (
                <EdgeDetailPanel edge={selectedEdge} onClose={() => setSelectedEdge(null)} range={range} />
              )}
            </>
          )}
        </>
      )}
    </>
  );
}

// FlowColorFilterStrip — v0.5.290. Per-service color chip
// strip rendered above the picked-flow SVG, mirroring the
// OperationView strip (v0.5.285). Only service-kind nodes
// become chips (db / queue / external have fixed kind colors
// rather than hashColor-by-name, so filtering by them isn't
// meaningful in this surface).
function FlowColorFilterStrip({ nodes, value, onChange }: {
  nodes: ServiceTopologyNode[];
  value: string | null;
  onChange: (v: string | null) => void;
}) {
  const services = nodes.filter(n => n.kind === 'service');
  if (services.length <= 1) return null;
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 6,
      flexWrap: 'wrap', marginBottom: 8,
      padding: '6px 0',
      borderTop: '1px dashed var(--border)',
      borderBottom: '1px dashed var(--border)',
    }}>
      <span style={{
        fontSize: 11, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 700,
        marginRight: 4,
      }}>Filter by service color</span>
      {services.map(n => {
        const isPicked = value === n.id;
        const c = hashColor(n.name);
        return (
          <button key={n.id} type="button"
            onClick={() => onChange(isPicked ? null : n.id)}
            title={isPicked ? `Showing ${n.name} only — click to clear` : `Show only ${n.name}'s edges`}
            style={{
              all: 'unset', cursor: 'pointer',
              display: 'inline-flex', alignItems: 'center', gap: 5,
              padding: '2px 8px', borderRadius: 12, fontSize: 11,
              border: `1px solid ${isPicked ? c : 'var(--border)'}`,
              background: isPicked
                ? `color-mix(in srgb, ${c} 22%, transparent)`
                : 'transparent',
              color: isPicked ? 'var(--text)' : 'var(--text2)',
              fontWeight: isPicked ? 700 : 500,
              opacity: value && !isPicked ? 0.55 : 1,
            }}>
            <span style={{
              width: 9, height: 9, borderRadius: '50%',
              background: c, display: 'inline-block',
            }} />
            {n.name}
          </button>
        );
      })}
      {value && (
        <button type="button" onClick={() => onChange(null)}
          style={{
            all: 'unset', cursor: 'pointer', fontSize: 11,
            color: 'var(--text3)', marginLeft: 4,
          }}
          title="Clear color filter">✕ clear</button>
      )}
    </div>
  );
}

// FlowsByService — v0.5.178. Groups the flat business-flows
// list by root service so a 200-flow install isn't an
// undifferentiated wall of cards. Default-collapsed for groups
// with >5 flows so the page loads scannable; default-expanded
// when the operator narrowed via search (they're already
// looking at a small subset). The group header surfaces
// aggregate stats (total trace count + worst-flow p99) so the
// operator can spot the "billing-api has 12 flows and one is
// 3s p99" pattern without expanding the group.
function FlowsByService({ flows, hasSearch, onPick }: {
  flows: RootFlow[];
  hasSearch: boolean;
  onPick: (f: RootFlow) => void;
}) {
  const groups = useMemo(() => {
    const byService = new Map<string, RootFlow[]>();
    for (const f of flows) {
      const arr = byService.get(f.rootService) ?? [];
      arr.push(f);
      byService.set(f.rootService, arr);
    }
    const out: Array<{
      service: string;
      flows: RootFlow[];
      totalTraces: number;
      maxP99Ns: number;
    }> = [];
    byService.forEach((fs, service) => {
      let totalTraces = 0;
      let maxP99Ns = 0;
      for (const f of fs) {
        totalTraces += f.traceCount;
        if (f.p99Ns && f.p99Ns > maxP99Ns) maxP99Ns = f.p99Ns;
      }
      fs.sort((a, b) => b.traceCount - a.traceCount);
      out.push({ service, flows: fs, totalTraces, maxP99Ns });
    });
    out.sort((a, b) => b.totalTraces - a.totalTraces);
    return out;
  }, [flows]);

  // `overrides` flips a group's expanded state against its
  // size-based default. Kept as a Set of service names rather
  // than a Map<svc, bool> so the "expand all" / "collapse all"
  // resets stay one-line.
  const [overrides, setOverrides] = useState<Set<string>>(new Set());
  const isExpanded = (g: typeof groups[number]) => {
    const defaultExpanded = hasSearch || g.flows.length <= 5;
    return overrides.has(g.service) ? !defaultExpanded : defaultExpanded;
  };
  const toggle = (svc: string) => {
    setOverrides(prev => {
      const next = new Set(prev);
      if (next.has(svc)) next.delete(svc); else next.add(svc);
      return next;
    });
  };
  const expandAll = () => {
    const next = new Set<string>();
    for (const g of groups) {
      const defaultExpanded = hasSearch || g.flows.length <= 5;
      if (!defaultExpanded) next.add(g.service);
    }
    setOverrides(next);
  };
  const collapseAll = () => {
    const next = new Set<string>();
    for (const g of groups) {
      const defaultExpanded = hasSearch || g.flows.length <= 5;
      if (defaultExpanded) next.add(g.service);
    }
    setOverrides(next);
  };

  if (groups.length === 0) {
    return <Empty icon="◇" title="No flows match the filter" />;
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', gap: 8, fontSize: 11, color: 'var(--text3)' }}>
        <span>{groups.length} service{groups.length === 1 ? '' : 's'}</span>
        <span style={{ flex: 1 }} />
        <button type="button" className="sec" onClick={expandAll}
          style={{ fontSize: 11, padding: '3px 8px' }}>Expand all</button>
        <button type="button" className="sec" onClick={collapseAll}
          style={{ fontSize: 11, padding: '3px 8px' }}>Collapse all</button>
      </div>
      {groups.map(g => {
        const open = isExpanded(g);
        const worstP99Ms = g.maxP99Ns / 1e6;
        const p99Color = worstP99Ms > 5000 ? 'var(--err)'
          : worstP99Ms > 1000 ? 'var(--warn)'
          : 'var(--text3)';
        return (
          <div key={g.service} style={{
            border: '1px solid var(--border)', borderRadius: 6,
            background: 'var(--bg2)', overflow: 'hidden',
          }}>
            <button type="button" onClick={() => toggle(g.service)}
              style={{
                width: '100%', textAlign: 'left',
                display: 'flex', alignItems: 'baseline', gap: 10,
                padding: '10px 12px',
                background: 'transparent', border: 'none',
                cursor: 'pointer', color: 'var(--text)',
              }}>
              <span style={{
                fontSize: 11, color: 'var(--text3)',
                fontFamily: 'ui-monospace, monospace', width: 14,
              }}>{open ? '▼' : '▶'}</span>
              <span style={{
                fontWeight: 700, fontSize: 13,
                fontFamily: 'ui-monospace, monospace',
              }}>{g.service}</span>
              <span style={{
                fontSize: 10, padding: '1px 6px', borderRadius: 8,
                background: 'var(--bg3)', color: 'var(--text2)',
                fontFamily: 'ui-monospace, monospace',
              }}>
                {g.flows.length} flow{g.flows.length === 1 ? '' : 's'}
              </span>
              <span style={{ flex: 1 }} />
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                {fmtNum(g.totalTraces)} traces
              </span>
              {g.maxP99Ns > 0 && (
                <span style={{
                  fontSize: 11, color: p99Color,
                  fontFamily: 'ui-monospace, monospace',
                }} title="Worst p99 root-span duration across this service's flows">
                  worst p99 {worstP99Ms >= 1000
                    ? `${(worstP99Ms / 1000).toFixed(2)} s`
                    : `${worstP99Ms.toFixed(0)} ms`}
                </span>
              )}
            </button>
            {open && (
              <div style={{
                borderTop: '1px solid var(--border)',
                display: 'flex', flexDirection: 'column',
              }}>
                {g.flows.map((f, i) => (
                  <button key={i} type="button" onClick={() => onPick(f)}
                    style={{
                      display: 'flex', flexDirection: 'column',
                      gap: 4, textAlign: 'left',
                      padding: '10px 12px 10px 36px',
                      background: 'transparent', border: 'none',
                      borderTop: i > 0 ? '1px solid var(--border)' : 'none',
                      cursor: 'pointer', color: 'var(--text)',
                    }}>
                    <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                      <span style={{ fontWeight: 600, fontSize: 12 }}>{f.rootOp}</span>
                      <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
                        {fmtNum(f.traceCount)} traces
                      </span>
                      {f.p99Ns !== undefined && f.p99Ns > 0 && (() => {
                        const p99Ms = f.p99Ns / 1e6;
                        const color = p99Ms > 5000 ? 'var(--err)'
                          : p99Ms > 1000 ? 'var(--warn)'
                          : 'var(--text3)';
                        const label = p99Ms >= 1000
                          ? `${(p99Ms / 1000).toFixed(2)} s`
                          : `${p99Ms.toFixed(0)} ms`;
                        return (
                          <span style={{
                            fontSize: 11, color,
                            fontFamily: 'ui-monospace, monospace',
                          }}>p99 {label}</span>
                        );
                      })()}
                    </div>
                    {f.services.length > 1 && (
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                        {f.services.slice(0, 10).map(s => (
                          <span key={s} style={{
                            fontSize: 10, padding: '1px 6px', borderRadius: 3,
                            background: 'var(--bg3)', color: 'var(--text3)',
                            fontFamily: 'monospace',
                          }}>{s}</span>
                        ))}
                        {f.services.length > 10 && (
                          <span style={{ fontSize: 10, color: 'var(--text3)' }}>
                            +{f.services.length - 10}
                          </span>
                        )}
                      </div>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

// ── Layout helpers + SVG renderers ────────────────────────────

// layerServices assigns a column index to every visible node so
// the SVG renderer can lay things out left-to-right. Two modes:
//
//   anchor unset (overview / top-N) — classic Sugiyama-ish layout:
//     "roots" are zero-incoming-degree services placed at column 0,
//     callees BFS forward from there. This is the long-standing
//     behaviour and fits an overview where call direction matters.
//
//   anchor set (focus mode, v0.5.159) — the focused service goes
//     to column 0 and the diagram fans out on both sides: callees
//     to positive columns, callers to negative ones (then the
//     whole thing is shifted so the smallest column is 0). This
//     fixes the long-standing "in prod every focus looks like it
//     comes from api-gateway" symptom: previously the visual root
//     was always the topmost upstream caller of the focused node,
//     because layerServices's incoming-degree pick favoured it.
//     Now the operator sees the focused service as the anchor it
//     deserves to be.
function layerServices(
  data: ServiceTopologyResponse | null | undefined,
  anchor?: string,
  // v0.5.282 — 'down' (default) renders only callees of the
  // anchor so the anchor sits at column 0 and the diagram reads
  // strictly left-to-right. 'both' restores the legacy
  // signed-column layout (callers in negative columns shifted
  // to the left of the anchor).
  focusDir: 'down' | 'both' = 'down',
): Map<string, number> {
  const layer = new Map<string, number>();
  if (!data) return layer;

  if (anchor && data.nodes.some(n => n.id === anchor)) {
    // Bidirectional BFS hop-distance from the anchor. Build
    // outgoing + incoming adjacency once so each BFS step is O(1).
    const outAdj = new Map<string, string[]>();
    const inAdj = new Map<string, string[]>();
    data.edges.forEach(e => {
      if (!outAdj.has(e.parentService)) outAdj.set(e.parentService, []);
      outAdj.get(e.parentService)!.push(e.childNode);
      if (!inAdj.has(e.childNode)) inAdj.set(e.childNode, []);
      inAdj.get(e.childNode)!.push(e.parentService);
    });
    const fwd = new Map<string, number>([[anchor, 0]]);
    const bwd = new Map<string, number>([[anchor, 0]]);
    // Forward BFS — follow outgoing edges → callees.
    let frontier: string[] = [anchor];
    while (frontier.length > 0) {
      const next: string[] = [];
      frontier.forEach(id => {
        const h = fwd.get(id)!;
        (outAdj.get(id) || []).forEach(c => {
          if (!fwd.has(c)) { fwd.set(c, h + 1); next.push(c); }
        });
      });
      frontier = next;
    }
    // Backward BFS — follow incoming edges → callers. Skipped
    // in 'down' mode so the anchor stays at column 0 and the
    // diagram reads strictly left→right; restored for 'both'
    // (caller context for blame walks).
    if (focusDir === 'both') {
      frontier = [anchor];
      while (frontier.length > 0) {
        const next: string[] = [];
        frontier.forEach(id => {
          const h = bwd.get(id)!;
          (inAdj.get(id) || []).forEach(p => {
            if (!bwd.has(p)) { bwd.set(p, h + 1); next.push(p); }
          });
        });
        frontier = next;
      }
    }
    // Signed column: callees positive, callers negative, anchor 0.
    // A node reachable BOTH ways (cycles, e.g. mutual call) picks
    // whichever direction is closer — keeps the layout from
    // exploding when a graph has loops.
    const signed = new Map<string, number>();
    data.nodes.forEach(n => {
      const f = fwd.get(n.id);
      const b = bwd.get(n.id);
      if (n.id === anchor) { signed.set(n.id, 0); return; }
      if (f !== undefined && b !== undefined) {
        signed.set(n.id, f <= b ? f : -b);
      } else if (f !== undefined) {
        signed.set(n.id, f);
      } else if (b !== undefined) {
        signed.set(n.id, -b);
      }
      // Unreachable nodes (shouldn't happen — the visible-subset
      // BFS in ServiceView already keeps only reachable ones)
      // fall through to the post-loop fallback.
    });
    // Shift so the smallest column is 0 — render only handles
    // non-negative column indices.
    let minC = 0;
    signed.forEach(v => { if (v < minC) minC = v; });
    signed.forEach((v, k) => layer.set(k, v - minC));
    // Stragglers — orphan nodes not reached in either direction.
    let maxH = 0;
    layer.forEach(v => { if (v > maxH) maxH = v; });
    data.nodes.forEach(n => { if (!layer.has(n.id)) layer.set(n.id, maxH + 1); });
    return layer;
  }

  const incoming = new Map<string, number>();
  data.nodes.forEach(n => incoming.set(n.id, 0));
  data.edges.forEach(e => incoming.set(e.childNode, (incoming.get(e.childNode) ?? 0) + 1));

  const roots = data.nodes.filter(n => (incoming.get(n.id) ?? 0) === 0 && n.kind === 'service');
  let queue: string[];
  if (roots.length > 0) queue = roots.map(r => r.id);
  else {
    const out = new Map<string, number>();
    data.edges.forEach(e => out.set(e.parentService, (out.get(e.parentService) ?? 0) + 1));
    let max = -1, pick = data.nodes[0]?.id;
    out.forEach((v, k) => { if (v > max) { max = v; pick = k; } });
    queue = pick ? [pick] : [];
  }
  queue.forEach(id => layer.set(id, 0));
  while (queue.length > 0) {
    const id = queue.shift()!;
    const h = layer.get(id)!;
    data.edges.filter(e => e.parentService === id).forEach(e => {
      if (!layer.has(e.childNode)) {
        layer.set(e.childNode, h + 1);
        queue.push(e.childNode);
      }
    });
  }
  let maxH = 0;
  layer.forEach(v => { if (v > maxH) maxH = v; });
  data.nodes.forEach(n => { if (!layer.has(n.id)) layer.set(n.id, maxH + 1); });
  return layer;
}

// layerOpNodes lays out the operation deep-dive view. When
// rootOp is given (v0.5.160) only that specific {service|op}
// becomes the column-0 anchor — previously every operation on
// the root service got dropped into column 0 even when the
// operator had narrowed the picker to a single op, which caused
// the same "every focus looks the same" visual symptom we just
// fixed on the service view. Without rootOp the legacy
// behaviour stands: every root-service op anchors column 0.
function layerOpNodes(
  data: TopologyResponse | null | undefined,
  root: string,
  rootOp?: string,
): TopologyNode[][] {
  if (!data || !root) return [];
  const outgoing = new Map<string, string[]>();
  data.edges.forEach(e => {
    const src = `${e.parentService}|${e.parentOp}`;
    const dst = `${e.childService}|${e.childOp}`;
    if (!outgoing.has(src)) outgoing.set(src, []);
    outgoing.get(src)!.push(dst);
  });
  const hop = new Map<string, number>();
  if (rootOp) {
    const anchorID = `${root}|${rootOp}`;
    if (data.nodes.some(n => n.id === anchorID)) {
      hop.set(anchorID, 0);
    }
  }
  if (hop.size === 0) {
    data.edges.forEach(e => {
      if (e.parentService === root) {
        const id = `${e.parentService}|${e.parentOp}`;
        if (!hop.has(id)) hop.set(id, 0);
      }
    });
  }
  if (hop.size === 0) data.nodes.forEach(n => hop.set(n.id, 0));
  let frontier = new Set(hop.keys());
  while (frontier.size > 0) {
    const next = new Set<string>();
    frontier.forEach(id => {
      const h = hop.get(id)!;
      (outgoing.get(id) || []).forEach(childID => {
        if (!hop.has(childID)) {
          hop.set(childID, h + 1);
          next.add(childID);
        }
      });
    });
    frontier = next;
  }
  // v0.5.247 — orphan nodes (in data.nodes but never reached
  // by BFS) used to fall through to column 0, mixing them with
  // the root seeds. Service topology's layerServices puts
  // them at maxH+1 (rightmost) so they stay visible without
  // distorting the root column. Op view now matches.
  let maxH = 0;
  hop.forEach(v => { if (v > maxH) maxH = v; });
  data.nodes.forEach(n => { if (!hop.has(n.id)) hop.set(n.id, maxH + 1); });
  const finalMaxHop = Math.max(...Array.from(hop.values()));
  const layers: TopologyNode[][] = Array.from({ length: finalMaxHop + 1 }, () => []);
  data.nodes.forEach(n => layers[hop.get(n.id) ?? 0].push(n));
  // Sort within each column: anchor goes first (so the
  // operator's focal point pins to the top of its column),
  // then by service + op alphabetically for stability across
  // refreshes.
  const anchorID = rootOp ? `${root}|${rootOp}` : '';
  layers.forEach(layer => layer.sort((a, b) => {
    if (a.id === anchorID) return -1;
    if (b.id === anchorID) return 1;
    return a.service.localeCompare(b.service) || a.op.localeCompare(b.op);
  }));
  return layers;
}

const NODE_W = 200, NODE_H = 56, COL_W = 280, ROW_H = 80;

function nodeColors(node: ServiceTopologyNode): { fill: string; stroke: string } {
  switch (node.kind) {
    case 'db':       return { fill: '#3b5a73', stroke: '#6c8ebf' };
    case 'queue':    return { fill: '#7a5e1d', stroke: '#d6b656' };
    case 'external': return { fill: '#6a3a3a', stroke: '#b85450' };
    default: {
      const c = hashColor(node.name);
      return { fill: c, stroke: c };
    }
  }
}
function protoColor(proto: string): string {
  switch (proto) {
    case 'http':  return '#4A90D9';
    case 'rpc':   return '#8A6FB5';
    case 'db':    return '#6c8ebf';
    case 'kafka': return '#d6b656';
    default:      return '#888';
  }
}

function ServiceTopologySVG({ nodes, edges, layout, onEdgeClick, search, incidentServices, onNodeClick, onIncidentClick, metaByService, anchor, colorFilter, flowOn }: {
  nodes: ServiceTopologyNode[];
  edges: ServiceTopologyEdge[];
  layout: Map<string, number>;
  onEdgeClick: (e: ServiceTopologyEdge) => void;
  search?: string;
  incidentServices?: Set<string>;
  onNodeClick?: (node: ServiceTopologyNode) => void;
  // v0.5.152 — click the red ring/badge to open the problems
  // drawer instead of navigating to the service page. Keeps the
  // operator on the topology view for triage.
  onIncidentClick?: (node: ServiceTopologyNode) => void;
  metaByService?: Record<string, {
    ownerTeam?: string; sreTeam?: string; chatChannel?: string;
    runbookUrl?: string; oncallUrl?: string;
  }>;
  // v0.5.159 — id of the focused service (the BFS anchor). Drawn
  // with a thicker accent border so the operator's eye lands on
  // it immediately.
  anchor?: string;
  // v0.5.290 — service node id; when set, only that service +
  // edges touching it stay fully opaque, everything else fades
  // to 18% (same dim level as the search-highlight). Same idea
  // as OpTopologySVG's colorFilter (v0.5.285) — gives FlowsView
  // a one-click "isolate one downstream chain" affordance.
  colorFilter?: string | null;
  // v0.5.412 — live-flow toggle. When true, every visible edge's
  // path gets the .topo-edge-flow class + an inline animation-
  // duration inversely proportional to log(calls), so busy edges
  // visibly flow faster than idle ones. Off keeps the prior
  // static stroke behaviour.
  flowOn?: boolean;
}) {
  // maxCalls is computed below near the layout block; we reuse it
  // for the live-flow per-edge animation-duration mapping.
  const isNodeInColorFilter = (n: ServiceTopologyNode) =>
    !colorFilter || n.id === colorFilter;
  const isEdgeInColorFilter = (e: ServiceTopologyEdge) =>
    !colorFilter || e.parentService === colorFilter || e.childNode === colorFilter;
  // Search highlighting: a node "matches" when its name includes
  // the (case-insensitive) substring. Edges match when EITHER end
  // matches. Non-matching geometry fades to 0.18 opacity so the
  // operator can still see the graph context. Empty search ==
  // everything matches.
  const term = (search ?? '').trim().toLowerCase();
  const isNodeMatch = (n: ServiceTopologyNode) =>
    !term || n.name.toLowerCase().includes(term);
  const isEdgeMatch = (e: ServiceTopologyEdge) => {
    if (!term) return true;
    const p = nodes.find(n => n.id === e.parentService);
    const c = nodes.find(n => n.id === e.childNode);
    return (p && isNodeMatch(p)) || (c && isNodeMatch(c));
  };
  const layered: ServiceTopologyNode[][] = [];
  nodes.forEach(n => {
    const h = layout.get(n.id) ?? 0;
    while (layered.length <= h) layered.push([]);
    layered[h].push(n);
  });
  layered.forEach(col => col.sort((a, b) => a.name.localeCompare(b.name)));
  const pos = new Map<string, { x: number; y: number }>();
  layered.forEach((col, hop) => col.forEach((n, i) => pos.set(n.id, { x: hop * COL_W, y: i * ROW_H })));
  const maxRows = Math.max(1, ...layered.map(c => c.length));
  const width = Math.max(1, layered.length) * COL_W;
  const height = maxRows * ROW_H + 40;
  const maxCalls = Math.max(1, ...edges.map(e => Number(e.calls) || 0));
  const truncate = (s: string, n: number) => s.length > n ? s.slice(0, n - 1) + '…' : s;
  const sorted = [...edges].map(e => e.calls).sort((a, b) => b - a);
  const callThreshold = sorted[Math.floor(sorted.length / 3)] ?? 0;
  return (
    <div style={{
      overflow: 'auto', maxHeight: '65vh',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg2)', padding: 12, marginBottom: 16,
    }}>
      {/* v0.5.282 — width/height MUST match the viewBox area
          (width+40 × height+40, since the viewBox starts at
          -10,-10 to allow a 10px halo around the leftmost +
          topmost boxes). Earlier `width={width}` paired with
          `width+40` viewBox produced a `width/(width+40)` scale
          factor, so a 1-column diagram (width=280) rendered
          boxes at ~87% intrinsic size while a 6-column diagram
          (width=1680) rendered at ~98% — operator saw "some
          service boxes smaller than others". Now 1 viewBox
          unit = 1 CSS pixel regardless of column count. */}
      <svg width={width + 40} height={height + 40}
        viewBox={`-10 -10 ${width + 40} ${height + 40}`}
        xmlns="http://www.w3.org/2000/svg" style={{ display: 'block' }}>
        <defs>
          {(['http', 'rpc', 'db', 'kafka', 'internal'] as const).map(p => (
            <marker key={p} id={`arrow-${p}`} viewBox="0 0 10 10" refX="9" refY="5"
              markerWidth="7" markerHeight="7" orient="auto">
              <path d="M 0 0 L 10 5 L 0 10 z" fill={protoColor(p)} />
            </marker>
          ))}
        </defs>
        {/* v0.5.312 — namespace soft-cluster outlines, drawn
            BEFORE edges + nodes so they sit behind. Groups
            services by n.namespace; ignores nodes without one
            (most infra nodes — db/queue/external — don't carry
            a namespace, which is correct). Bounding box
            inflated by 12px so nodes don't kiss the border. */}
        {(() => {
          type Group = { ns: string; xs: number[]; ys: number[] };
          const byNs = new Map<string, Group>();
          for (const n of nodes) {
            if (!n.namespace || n.kind !== 'service') continue;
            const p = pos.get(n.id);
            if (!p) continue;
            let g = byNs.get(n.namespace);
            if (!g) { g = { ns: n.namespace, xs: [], ys: [] }; byNs.set(n.namespace, g); }
            g.xs.push(p.x, p.x + NODE_W);
            g.ys.push(p.y, p.y + NODE_H);
          }
          const out: React.ReactNode[] = [];
          byNs.forEach((g, ns) => {
            if (g.xs.length < 4) return; // need >=2 nodes to cluster
            const x0 = Math.min(...g.xs) - 12;
            const x1 = Math.max(...g.xs) + 12;
            const y0 = Math.min(...g.ys) - 24;
            const y1 = Math.max(...g.ys) + 12;
            const color = hashColor(ns);
            out.push(
              <g key={`ns-${ns}`} style={{ pointerEvents: 'none' }}>
                <rect x={x0} y={y0} width={x1 - x0} height={y1 - y0}
                  rx={10} ry={10} fill={color} fillOpacity={0.05}
                  stroke={color} strokeOpacity={0.45}
                  strokeWidth={1} strokeDasharray="4 4" />
                <text x={x0 + 8} y={y0 + 14} fontSize={10}
                  fill={color} fillOpacity={0.85}
                  fontFamily="ui-monospace, SFMono-Regular, monospace"
                  fontWeight={600}
                  style={{ textTransform: 'uppercase', letterSpacing: 0.4 }}>
                  {ns}
                </text>
              </g>
            );
          });
          return out;
        })()}
        {edges.map((e, i) => {
          const src = pos.get(e.parentService);
          const dst = pos.get(e.childNode);
          if (!src || !dst) return null;
          const x1 = src.x + NODE_W, y1 = src.y + NODE_H / 2;
          const x2 = dst.x,          y2 = dst.y + NODE_H / 2;
          const mx = (x1 + x2) / 2;
          const sw = 1 + (Number(e.calls) / maxCalls) * 3;
          const color = protoColor(e.protocol);
          const showLabel = e.calls >= callThreshold;
          const proto = e.protocol.toUpperCase();
          const top = e.topLabels[0] || '';
          const more = e.distinctLabels > 1 ? ` (+${e.distinctLabels - 1})` : '';
          // Focus mode (anchor set) drops endpoint/operation
          // labels so the operator's chosen service shows as
          // pure service-to-service. Operation-level detail
          // still lives on the Operation deep-dive tab.
          const focused = !!anchor;
          const match = isEdgeMatch(e);
          // v0.5.393 — error-aware tint dominates the latency tint.
          // Operator-facing rule: an edge that's BREAKING (≥1%
          // error rate) is more urgent than a slow-but-healthy
          // one. Bucketed: ≥5% errs = red, ≥1% errs = amber, else
          // fall back to the prior latency-based bucketing
          // (<250ms = base, 250-1000ms = amber, >1000ms = red).
          let strokeOverride = color;
          const errRate = e.errorRate ?? 0;
          if (errRate >= 5) strokeOverride = '#dc2626';
          else if (errRate >= 1) strokeOverride = '#d97706';
          else if (e.p99Ms > 1000) strokeOverride = '#dc2626';
          else if (e.p99Ms > 250) strokeOverride = '#d97706';
          const p99 = e.p99Ms > 0 ? ` · p99 ${e.p99Ms.toFixed(0)}ms` : '';
          const errSuffix = (e.errors ?? 0) > 0
            ? ` · ${fmtNum(e.errors)} err${errRate >= 0.01 ? ` (${errRate.toFixed(1)}%)` : ''}`
            : '';
          const inColor = isEdgeInColorFilter(e);
          return (
            <g key={i} style={{
                 cursor: 'pointer',
                 opacity: match && inColor ? 1 : 0.18,
               }}
               onClick={() => onEdgeClick(e)}>
              {/* v0.5.411 — async messaging edges (Kafka /
                  RabbitMQ / SQS via msg_system) render dashed
                  so the operator's eye distinguishes
                  fire-and-forget producer→queue→consumer
                  chains from synchronous HTTP/RPC strands.
                  Datadog / Honeycomb use the same convention. */}
              {/* v0.5.412 — live-flow path. When flowOn the class
                  + animation-duration override the static
                  strokeDasharray (CSS rule sets a dash for the
                  flow animation; .async kicks in for kafka so
                  the existing async cue stays distinct). */}
              <path d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                stroke={strokeOverride} strokeWidth={sw} fill="none"
                markerEnd={`url(#arrow-${e.protocol})`} opacity={0.7}
                className={flowOn
                  ? 'topo-edge-flow' + (e.protocol === 'kafka' ? ' async' : '')
                  : undefined}
                style={flowOn ? {
                  // Duration: 6s for the busiest edge, scaling up
                  // to 30s for idle ones. log() compresses the
                  // dynamic range so a 1000x traffic gap still
                  // reads as "faster", not "blur vs static".
                  animationDuration: `${
                    Math.max(0.6, Math.min(30,
                      30 / (Math.log10((Number(e.calls) || 1) + 1) /
                            Math.log10((maxCalls || 1) + 1) * 10 + 0.5)
                    )).toFixed(2)
                  }s`,
                } : undefined}
                strokeDasharray={flowOn ? undefined : (e.protocol === 'kafka' ? '6 4' : undefined)}>
                <title>{`${e.parentService} → ${e.childNode}\n${proto} · ${fmtNum(e.calls)} calls${errSuffix} · avg ${e.avgMs.toFixed(1)}ms · p99 ${e.p99Ms.toFixed(0)}ms · ${e.distinctLabels} endpoint(s)\n\n${e.topLabels.join('\n')}`}</title>
              </path>
              {showLabel && (
                <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4}
                  fontSize={10} fill={strokeOverride} textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {focused
                    ? `${proto} · ${fmtNum(Number(e.calls))}`
                    : `${proto} ${truncate(top, 28)}${more}`}
                </text>
              )}
              {showLabel && (
                <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 + 9}
                  fontSize={9} fill="var(--text3)" textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {fmtNum(e.calls)} calls{p99}
                </text>
              )}
              {showLabel && errRate >= 1 && (
                <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 + 20}
                  fontSize={9} fill={strokeOverride} textAnchor="middle"
                  style={{ pointerEvents: 'none', fontWeight: 600 }}>
                  {fmtNum(e.errors)} err ({errRate.toFixed(1)}%)
                </text>
              )}
            </g>
          );
        })}
        {nodes.map(n => {
          const p = pos.get(n.id);
          if (!p) return null;
          const { fill, stroke } = nodeColors(n);
          const kindIcon = n.kind === 'db' ? '⛁' : n.kind === 'queue' ? '⌬' : n.kind === 'external' ? '↗' : '';
          const match = isNodeMatch(n);
          const hasIncident = !!incidentServices && incidentServices.has(n.id);
          // Every kind except 'external' has a destination page —
          // services to /service, db to /databases, queue to
          // /messaging. External nodes stay non-clickable since
          // there's nothing past the outbound boundary.
          const clickable = !!onNodeClick && n.kind !== 'external';
          const inColor = isNodeInColorFilter(n);
          return (
            <g key={n.id} transform={`translate(${p.x}, ${p.y})`}
               style={{
                 opacity: match && inColor ? 1 : 0.18,
                 cursor: clickable ? 'pointer' : 'default',
               }}
               onClick={clickable ? () => onNodeClick(n) : undefined}>
              {/* v0.5.312 — three-state health ring (red /
                  yellow / faint green). Replaces the binary
                  "hasIncident" red dash ring; reads n.health
                  populated by the backend's open-problem
                  enrichment. Red = open critical, Yellow =
                  open warning, Green = clean (subtle, doesn't
                  scream).  Falls back to hasIncident when the
                  backend hasn't enriched yet. */}
              {(() => {
                const h = n.health ?? (hasIncident ? 'red' : '');
                if (!h || h === 'green') return null;
                const isRed = h === 'red';
                const color = isRed ? '#dc2626' : '#d97706';
                return (
                  <rect x={-3} y={-3} width={NODE_W + 6} height={NODE_H + 6}
                    rx={10} ry={10} fill="none"
                    stroke={color} strokeWidth={2.4}
                    strokeDasharray={isRed ? '4 3' : '5 4'}
                    style={{ cursor: onIncidentClick ? 'pointer' : 'inherit' }}
                    onClick={onIncidentClick ? (e) => {
                      e.stopPropagation();
                      onIncidentClick(n);
                    } : undefined}>
                    <title>{n.healthReason || 'Click to view open problems on this service'}</title>
                  </rect>
                );
              })()}
              {(() => {
                const md = metaByService?.[n.name];
                const team = md?.ownerTeam || md?.sreTeam || '';
                const isAnchor = !!anchor && n.id === anchor;
                const tip = `${n.name} (${n.kind})`
                  + (isAnchor ? ' · focused' : '')
                  + (hasIncident ? ' · open problem' : '')
                  + (md?.ownerTeam ? `\nowner: ${md.ownerTeam}` : '')
                  + (md?.sreTeam   ? `\nSRE:   ${md.sreTeam}`   : '')
                  + (md?.chatChannel ? `\nchat:  ${md.chatChannel}` : '')
                  + (md?.runbookUrl ? `\nrunbook: ${md.runbookUrl}` : '')
                  + (md?.oncallUrl  ? `\noncall:  ${md.oncallUrl}`  : '');
                const strokeColor = isAnchor ? 'var(--accent2)' : stroke;
                const strokeW = isAnchor ? 3 : (term && match ? 2.8 : 1.6);
                return (
                  <>
                    <rect width={NODE_W} height={NODE_H} rx={8} ry={8}
                      fill={fill} fillOpacity={isAnchor ? 0.28 : 0.18} stroke={strokeColor}
                      strokeWidth={strokeW}>
                      <title>{tip}</title>
                    </rect>
                    <text x={10} y={22} fontSize={13} fontWeight={600} fill="var(--text)">
                      {/* v0.5.409 — external nodes recognised as
                          known SaaS / cloud vendors show their
                          display name ("Stripe") instead of the
                          raw hostname. Unrecognised externals
                          keep the original truncated name. */}
                      {n.kind === 'external' && n.extDisplay
                        ? truncate(n.extDisplay, 24)
                        : truncate(n.name, 24)}
                    </text>
                    <text x={10} y={40} fontSize={10} fill="var(--text3)">
                      {n.kind === 'external' && n.extKind
                        ? n.extKind.toUpperCase()
                        : n.kind.toUpperCase()}
                      {/* v0.5.410 — env chip ("prod" / "stage" /
                          "k8s-ns") right after the kind label.
                          Only renders when the backend resolved
                          one from resource attrs. */}
                      {n.env && (
                        <tspan dx={6} fill="var(--accent2)" fontSize={9}>· {truncate(n.env, 14)}</tspan>
                      )}
                      {team && n.kind === 'service' && (
                        <tspan dx={6} fill="var(--text2)">· {truncate(team, 18)}</tspan>
                      )}
                      {n.kind === 'external' && n.extDisplay && (
                        <tspan dx={6} fill="var(--text3)" fontSize={9}>· {truncate(n.name, 22)}</tspan>
                      )}
                    </text>
                  </>
                );
              })()}
              {hasIncident && (
                <g style={{ cursor: onIncidentClick ? 'pointer' : 'inherit' }}
                  onClick={onIncidentClick ? (e) => {
                    e.stopPropagation();
                    onIncidentClick(n);
                  } : undefined}>
                  <circle cx={NODE_W - 30} cy={36} r={8}
                    fill="#dc2626" fillOpacity={0.18} stroke="#dc2626" strokeWidth={1.2} />
                  <text x={NODE_W - 30} y={40} fontSize={10} fontWeight={700}
                    fill="#dc2626" textAnchor="middle">!</text>
                </g>
              )}
              {/* Runbook quick-jump (v0.5.155). Only services have
                  catalog metadata; positioned at the bottom-left
                  of the node so it doesn't collide with the
                  incident "!" badge (bottom-right) or kindIcon
                  (top-right). Opens in a new tab — operator's
                  topology context isn't lost. */}
              {(() => {
                const md = metaByService?.[n.name];
                if (!md?.runbookUrl) return null;
                return (
                  <g style={{ cursor: 'pointer' }}
                    onClick={(e) => {
                      e.stopPropagation();
                      window.open(md.runbookUrl, '_blank', 'noopener,noreferrer');
                    }}>
                    <circle cx={NODE_W - 50} cy={36} r={8}
                      fill="var(--bg)" stroke="var(--accent2)" strokeWidth={1.2} />
                    <text x={NODE_W - 50} y={40} fontSize={10} fontWeight={700}
                      fill="var(--accent2)" textAnchor="middle">📖
                      <title>Open runbook — {md.runbookUrl}</title>
                    </text>
                  </g>
                );
              })()}
              {kindIcon && (
                <text x={NODE_W - 18} y={22} fontSize={14} fill={stroke}>{kindIcon}</text>
              )}
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function OpTopologySVG({ layers, edges, anchor, colorFilter }: {
  layers: TopologyNode[][];
  edges: TopologyResponse['edges'];
  // v0.5.160 — `{service}|{op}` id of the anchored root. Drawn
  // with an accent border so the operator's eye lands on it.
  anchor?: string;
  // v0.5.285 — when set, nodes from other services fade to 18%
  // opacity and edges that don't touch the filtered service also
  // fade. Matches the search-highlight pattern in ServiceView so
  // the operator can isolate one colored flow at high depth.
  colorFilter?: string | null;
}) {
  const isNodeInFilter = (n: TopologyNode) =>
    !colorFilter || n.service === colorFilter;
  const isEdgeInFilter = (e: TopologyResponse['edges'][number]) =>
    !colorFilter
      || e.parentService === colorFilter
      || e.childService  === colorFilter;
  // v0.5.246 — node + layout dimensions now reuse the SAME
  // constants as the Service topology view (NODE_W / NODE_H /
  // COL_W / ROW_H). Visual parity = the operator can switch
  // tabs without their eye recalibrating "small boxes here,
  // big boxes there". Bigger boxes also leave room for the
  // service + op two-line label + edge metadata.
  const maxRows = Math.max(1, ...layers.map(l => l.length));
  const totalHeight = maxRows * ROW_H;
  // Vertical centring per column — when a column has fewer
  // rows than the max, push it down so the diagram doesn't
  // read as "top-heavy on column 0". Same trick the
  // AggregateTopology service-detail panel uses (v0.5.222).
  const colYOffset = layers.map(col =>
    Math.floor((maxRows - col.length) * ROW_H / 2));
  const pos = new Map<string, { x: number; y: number }>();
  layers.forEach((layer, hop) => layer.forEach((n, i) =>
    pos.set(n.id, { x: hop * COL_W, y: colYOffset[hop] + i * ROW_H })));
  const width = Math.max(1, layers.length) * COL_W;
  const height = totalHeight + 20;
  const maxCalls = Math.max(1, ...edges.map(e => Number(e.calls) || 0));
  const truncate = (s: string, n: number) => s.length > n ? s.slice(0, n - 1) + '…' : s;

  // Threshold for showing inline call-count labels on edges:
  // top third by call volume only, so a busy graph isn't
  // overlaid with hundreds of tiny labels.
  const sortedCalls = [...edges].map(e => Number(e.calls) || 0).sort((a, b) => b - a);
  const callThreshold = sortedCalls[Math.floor(sortedCalls.length / 3)] ?? 0;

  return (
    <div style={{
      overflow: 'auto', maxHeight: '70vh',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg2)', padding: 12, marginBottom: 16,
    }}>
      <svg width={width + 40} height={height + 40}
        viewBox={`-20 -20 ${width + 40} ${height + 40}`}
        xmlns="http://www.w3.org/2000/svg" style={{ display: 'block' }}>
        <defs>
          <marker id="op-arrow" viewBox="0 0 10 10" refX="9" refY="5"
            markerWidth="7" markerHeight="7" orient="auto">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--text3)" />
          </marker>
        </defs>
        {edges.map((e, i) => {
          const src = pos.get(`${e.parentService}|${e.parentOp}`);
          const dst = pos.get(`${e.childService}|${e.childOp}`);
          if (!src || !dst) return null;
          const x1 = src.x + NODE_W, y1 = src.y + NODE_H / 2;
          const x2 = dst.x,          y2 = dst.y + NODE_H / 2;
          const mx = (x1 + x2) / 2;
          // 1-4px stroke width scaled to call volume. Same
          // scale as the Service topology so the two views
          // read identically.
          const sw = 1 + (Number(e.calls) / maxCalls) * 3;
          const showLabel = Number(e.calls) >= callThreshold;
          // Heuristic protocol tag from the parent op shape:
          // HTTP-ish ops contain "/" or upper-case HTTP method
          // prefix; others read as RPC. Cheap inference so we
          // can colour-code edges without changing the
          // backend TopologyEdge shape. Misses are visually
          // harmless (fall through to neutral grey).
          const proto = inferOpProtocol(e.parentOp, e.childOp);
          const strokeColor = proto === 'http' ? '#4A90D9'
                            : proto === 'rpc'  ? '#8A6FB5'
                            : 'var(--text3)';
          const inFilter = isEdgeInFilter(e);
          return (
            <g key={i} opacity={inFilter ? 1 : 0.18}>
              <path
                d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                stroke={strokeColor} strokeWidth={sw} fill="none"
                markerEnd="url(#op-arrow)" opacity={0.65}>
                <title>{`${e.parentService}.${e.parentOp} → ${e.childService}.${e.childOp}\n${fmtNum(e.calls)} calls in window`}</title>
              </path>
              {showLabel && (
                <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4}
                  fontSize={10} fill={strokeColor} textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {fmtNum(Number(e.calls))}
                </text>
              )}
            </g>
          );
        })}
        {layers.flatMap(layer => layer.map(n => {
          const p = pos.get(n.id)!;
          const color = hashColor(n.service);
          const isAnchor = !!anchor && n.id === anchor;
          const strokeColor = isAnchor ? 'var(--accent2)' : color;
          const strokeW = isAnchor ? 3 : 1.5;
          const inFilter = isNodeInFilter(n);
          return (
            <g key={n.id} transform={`translate(${p.x}, ${p.y})`}
               opacity={inFilter ? 1 : 0.18}>
              {/* Anchor halo — same visual treatment the
                  Service topology uses for the focused node so
                  the operator's chosen root pops at a glance. */}
              {isAnchor && (
                <rect x={-4} y={-4} width={NODE_W + 8} height={NODE_H + 8}
                  rx={10} ry={10} fill="none"
                  stroke="var(--accent2)" strokeWidth={1}
                  strokeDasharray="4 4" opacity={0.6} />
              )}
              <rect width={NODE_W} height={NODE_H} rx={8} ry={8}
                fill={color} fillOpacity={isAnchor ? 0.28 : 0.16}
                stroke={strokeColor} strokeWidth={strokeW}>
                <title>{`${n.service}.${n.op}${isAnchor ? ' · focused' : ''}`}</title>
              </rect>
              <text x={12} y={22} fontSize={13} fontWeight={700} fill="var(--text)">
                {truncate(n.service, 28)}
              </text>
              <text x={12} y={42} fontSize={11} fill="var(--text3)"
                fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace">
                {truncate(n.op, 32)}
              </text>
            </g>
          );
        }))}
      </svg>
    </div>
  );
}

// inferOpProtocol — best-effort protocol tag from operation
// names. HTTP ops typically contain a "/" (path) or start
// with an HTTP method like "GET ", "POST "; everything else
// reads as RPC / internal. Used to colour-code edges in the
// operation view since the TopologyEdge shape doesn't carry
// an explicit protocol field. False matches just fall back
// to neutral grey — harmless.
function inferOpProtocol(parentOp: string, childOp: string): 'http' | 'rpc' | 'unknown' {
  const o = (childOp || parentOp).trim();
  if (/^(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s/i.test(o)) return 'http';
  if (o.includes('/')) return 'http';
  if (o.includes('.') && /^[a-z]+\.[A-Z]/.test(o)) return 'rpc'; // pkg.Method style
  return 'unknown';
}

function EdgeDetailPanel({ edge, onClose, range, simplified }: {
  edge: ServiceTopologyEdge;
  onClose: () => void;
  range?: TimeRange;
  // simplified — focus mode collapses the endpoint list so the
  // panel reads as a clean service-to-service summary. Operation
  // detail still lives on the Operation deep-dive tab.
  simplified?: boolean;
}) {
  // Per-instance breakdown for infra edges (v0.5.142). Lazy-
  // fetched the first time the panel renders for a db/queue
  // edge; cached client-side via the api layer so re-opening
  // the same edge is instant. Empty for service-service edges.
  const isInfra = edge.nodeKind === 'db' || edge.nodeKind === 'queue';
  const [instances, setInstances] = useState<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'ok'; rows: Array<{ instance: string; calls: number; avgMs: number; p99Ms: number }> }
    | { kind: 'err'; msg: string }
  >({ kind: 'idle' });
  useEffect(() => {
    if (!isInfra || !range) return;
    // childNode = "db:postgresql" / "queue:kafka" — strip prefix
    // to get the system column value the backend filters on.
    const colonAt = edge.childNode.indexOf(':');
    if (colonAt < 0) return;
    const system = edge.childNode.slice(colonAt + 1);
    setInstances({ kind: 'loading' });
    const { from, to } = timeRangeToNs(range);
    api.topologyEdgeInstances({
      parent: edge.parentService,
      system,
      kind: edge.nodeKind as 'db' | 'queue',
      from, to,
    })
      .then(d => setInstances({ kind: 'ok', rows: d.instances ?? [] }))
      .catch(err => setInstances({
        kind: 'err',
        msg: err instanceof Error ? err.message : 'Failed to load instances',
      }));
  }, [edge, isInfra, range]);
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 12, marginTop: 12,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
        <div style={{ fontSize: 13, fontWeight: 700 }}>
          {edge.parentService} → {edge.childNode}
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          {edge.protocol.toUpperCase()} · {fmtNum(edge.calls)} calls
          {!simplified && ` · ${edge.distinctLabels} endpoint${edge.distinctLabels === 1 ? '' : 's'}`}
        </div>
        <button type="button" onClick={onClose} className="sec"
          style={{ marginLeft: 'auto', fontSize: 11, padding: '2px 8px' }}>
          Close
        </button>
      </div>
      {/* v0.5.393 — RED tile row. Surfaces the error count + rate
          plus avg / p99 latency for the edge so the operator
          doesn't have to hover the SVG path to read the numbers. */}
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 8, marginBottom: 10,
      }}>
        <EdgeStat label="Errors"
          value={fmtNum(edge.errors ?? 0)}
          sub={edge.calls > 0 ? `${(edge.errorRate ?? 0).toFixed(2)}%` : ''}
          cls={(edge.errorRate ?? 0) >= 5 ? 'err' : (edge.errorRate ?? 0) >= 1 ? 'warn' : ''} />
        <EdgeStat label="Avg latency" value={`${edge.avgMs.toFixed(1)} ms`} />
        <EdgeStat label="P99 latency" value={`${edge.p99Ms.toFixed(0)} ms`}
          cls={edge.p99Ms > 1000 ? 'err' : edge.p99Ms > 250 ? 'warn' : ''} />
      </div>
      {!simplified && (
        <>
          <ul style={{ margin: 0, padding: '0 0 0 16px', fontSize: 12, lineHeight: 1.6, fontFamily: 'monospace' }}>
            {edge.topLabels.map((label, i) => <li key={i}>{label}</li>)}
          </ul>
          {edge.distinctLabels > edge.topLabels.length && (
            <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text3)' }}>
              Showing top {edge.topLabels.length} of {edge.distinctLabels} distinct endpoints.
            </div>
          )}
        </>
      )}
      {simplified && (
        <div style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
          Focus mode — service-to-service summary. Switch to the
          Operation deep-dive tab for per-endpoint breakdowns.
        </div>
      )}
      {isInfra && (
        <div style={{ marginTop: 10, paddingTop: 8, borderTop: '1px dashed var(--border)' }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)', marginBottom: 6 }}>
            Per-instance breakdown
          </div>
          {instances.kind === 'loading' && (
            <div style={{ fontSize: 11, color: 'var(--text3)' }}>Loading…</div>
          )}
          {instances.kind === 'err' && (
            <div style={{ fontSize: 11, color: 'var(--err)' }}>{instances.msg}</div>
          )}
          {instances.kind === 'ok' && instances.rows.length === 0 && (
            <div style={{ fontSize: 11, color: 'var(--text3)' }}>
              No peer_service tagged for this edge in the current window.
            </div>
          )}
          {instances.kind === 'ok' && instances.rows.length > 0 && (
            <table style={{
              width: '100%', fontSize: 11,
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
            }}>
              <thead>
                <tr style={{ color: 'var(--text3)', textAlign: 'left' }}>
                  <th>Instance</th>
                  <th style={{ textAlign: 'right' }}>Calls</th>
                  <th style={{ textAlign: 'right' }}>Avg ms</th>
                  <th style={{ textAlign: 'right' }}>P99 ms</th>
                </tr>
              </thead>
              <tbody>
                {instances.rows.map(r => (
                  <tr key={r.instance}>
                    <td>{r.instance}</td>
                    <td style={{ textAlign: 'right' }}>{fmtNum(r.calls)}</td>
                    <td style={{ textAlign: 'right' }}>{r.avgMs.toFixed(1)}</td>
                    <td style={{ textAlign: 'right',
                      color: r.p99Ms > 1000 ? '#dc2626'
                        : r.p99Ms > 250 ? '#d97706'
                        : undefined }}>
                      {r.p99Ms.toFixed(0)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}

// IncidentDrawer (v0.5.152) — right-side slide-in showing the
// open problems for the service whose incident ring/badge the
// operator just clicked. Keeps the operator on the topology view
// for triage instead of jumping to /problems and losing the
// neighbourhood context. Each row links to the full Problem view
// for deep triage; the drawer itself is intentionally read-only.
// EdgeStat — compact KPI cell for the EdgeDetailPanel header row.
// Tiny variant of the KPI pattern used across the app; keeps the
// drawer dense so the operator can scan errors / latency without
// scrolling.
// v0.5.413 — format minutes-ago for the time-shift slider label.
// Tight output: "15m" / "1h 30m" / "3d 2h" / "7d". Keeps the
// slider header readable at fixed width.
function formatShift(min: number): string {
  if (min < 60) return `${min}m`;
  if (min < 1440) {
    const h = Math.floor(min / 60);
    const m = min % 60;
    return m === 0 ? `${h}h` : `${h}h ${m}m`;
  }
  const d = Math.floor(min / 1440);
  const h = Math.floor((min % 1440) / 60);
  return h === 0 ? `${d}d` : `${d}d ${h}h`;
}

function EdgeStat({ label, value, sub, cls }: {
  label: string; value: string; sub?: string; cls?: string;
}) {
  return (
    <div style={{
      padding: '6px 10px', border: '1px solid var(--border)',
      borderRadius: 4, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: 14, fontWeight: 600, marginTop: 2,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 10, color: 'var(--text3)' }}>{sub}</div>
      )}
    </div>
  );
}

function IncidentDrawer({ service, onClose }: {
  service: string;
  onClose: () => void;
}) {
  const [state, setState] = useState<
    | { kind: 'loading' }
    | { kind: 'ok'; rows: Problem[] }
    | { kind: 'err'; msg: string }
  >({ kind: 'loading' });

  useEffect(() => {
    setState({ kind: 'loading' });
    api.problems({ service, status: 'open', limit: 100 })
      .then(rows => setState({ kind: 'ok', rows: rows ?? [] }))
      .catch(e => setState({
        kind: 'err',
        msg: e instanceof Error ? e.message : 'Failed to load problems',
      }));
  }, [service]);

  // Esc closes — standard incident-triage muscle memory shared
  // with the AnomaliesPage TriageDrawer.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const fmtAgo = (ns: number) => {
    const secs = Math.max(1, Math.floor((Date.now() - ns / 1e6) / 1000));
    if (secs < 60)        return `${secs}s ago`;
    if (secs < 3600)      return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400)     return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  };
  const sevClass = (s: string) =>
    s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';

  return (
    <>
      <div onClick={onClose}
        style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
          zIndex: 30,
        }} />
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: 'min(480px, 100vw)',
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
        zIndex: 31, overflowY: 'auto',
      }}>
        <div style={{
          padding: '14px 18px', borderBottom: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 10,
        }}>
          <span style={{ color: '#dc2626', fontSize: 16 }}>●</span>
          <div style={{ fontSize: 14, fontWeight: 700 }}>Open problems</div>
          <Link to={`/service?name=${encodeURIComponent(service)}`}
            style={{ fontSize: 12, color: 'var(--text2)' }}>
            {service} →
          </Link>
          <span style={{ flex: 1 }} />
          <button onClick={onClose} className="sec"
            title="Close (Esc)"
            style={{ fontSize: 14, padding: '2px 10px' }}>×</button>
        </div>
        <div style={{ padding: '14px 18px' }}>
          {state.kind === 'loading' && <Spinner />}
          {state.kind === 'err' && (
            <div style={{ color: 'var(--err)', fontSize: 12 }}>{state.msg}</div>
          )}
          {state.kind === 'ok' && state.rows.length === 0 && (
            <Empty icon="◇" title="No open problems">
              The ring may be stale — the overlay refreshes on time-range change.
            </Empty>
          )}
          {state.kind === 'ok' && state.rows.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {state.rows.map(p => (
                <Link key={p.id} to={`/problems?id=${encodeURIComponent(p.id)}`}
                  style={{
                    display: 'block',
                    background: 'var(--bg2)', border: '1px solid var(--border)',
                    borderRadius: 6, padding: 10, textDecoration: 'none',
                    color: 'inherit',
                  }}>
                  <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                    <span className={`badge ${sevClass(p.severity)}`}>{p.severity}</span>
                    <span style={{ fontWeight: 600, fontSize: 13 }}>{p.ruleName}</span>
                    <span style={{ flex: 1 }} />
                    <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {fmtAgo(p.startedAt)}
                    </span>
                  </div>
                  <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text2)', fontFamily: 'ui-monospace, monospace' }}>
                    {p.metric} {p.value.toFixed(2)} vs threshold {p.threshold.toFixed(2)}
                  </div>
                  {p.description && (
                    <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text3)' }}>
                      {p.description}
                    </div>
                  )}
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  );
}
