import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { DrillButton } from '@/components/DrillButton';
import { Button } from '@/components/ui/Button';
import { recordServiceVisit, isServicePinned, toggleServicePin } from '@/lib/recentServices';
import { useUrlRange } from '@/lib/useUrlRange';
import { ServiceOverview } from './service/Overview';
import { ServiceTracesTab, ServiceLogsTab, ServiceTopologyTab } from './service/ServiceSignalTabs';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceStructure } from '@/components/ServiceStructure';
import { ServiceCharts } from '@/components/ServiceCharts';
import { LazyMount } from '@/components/LazyMount';
import { LatencyHeatmap } from '@/components/LatencyHeatmap';
import { ServiceCatalogPill } from '@/components/ServiceCatalogPill';
import { DBQueriesPanel } from '@/components/DBQueriesPanel';
import { DeployHistoryPanel } from '@/components/DeployHistoryPanel';
import { ServiceNeighbors } from '@/components/ServiceNeighbors';
import { ServiceInfra } from '@/components/ServiceInfra';
import { Sparkline } from '@/components/Sparkline';
import { MultiLineChart } from '@/components/MultiLineChart';
import { Modal } from '@/components/ui';
import { ServiceProfilingPanel } from '@/components/ServiceProfilingPanel';
import { ServiceAttrsPanel } from '@/components/ServiceAttrsPanel';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import { encodeFilters, encodeRange, buildQuery } from '@/lib/urlState';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ServiceRuntimeBadge } from '@/components/ServiceRuntimeBadge';
import { keys } from '@/lib/queries/keys';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { Service, Problem, TimeRange, OperationSummary, SpanMetricSeries, SLORow, SpanAgg } from '@/lib/types';

const SINCE_MAP: Record<string, string> = {
  '5m': '5m', '10m': '10m', '15m': '15m', '30m': '30m',
  '1h': '1h', '3h': '3h', '6h': '6h', '12h': '12h',
  '24h': '24h', '2d': '48h', '7d': '168h', '30d': '720h',
};

type ServiceTab = 'overview' | 'operations' | 'details' | 'traces' | 'logs' | 'topology';

function ServiceDetailInner() {
  const [searchParams, setSearchParams] = useSearchParams();
  // Canonical key is `name` (every in-app link uses /service?name=…); also accept
  // `service` so a hand-typed /service?service=X resolves instead of showing
  // "Missing service name" (operator-reported, v0.8.219).
  const svc = searchParams.get('name') ?? searchParams.get('service') ?? '';
  // Runtime fingerprint (e.g. "Java OpenJDK 21") for the service-header badge.
  const runtimeQ = useQuery({ queryKey: ['svc-runtime', svc], queryFn: () => api.serviceRuntime(svc), enabled: !!svc, staleTime: 300_000 });

  const queryClient = useQueryClient();
  // Global time window (UX#2) — URL-persisted + carried across pages.
  const [range, setRange] = useUrlRange('30m');
  const [pinned, setPinned] = useState(false);
  // v0.7.89 — record this service in the recently-viewed MRU (powers
  // the Cmd-K pivot rotation) and reflect its pinned state for the
  // header toggle. Fires whenever the viewed service changes.
  useEffect(() => {
    if (!svc) return;
    recordServiceVisit(svc);
    setPinned(isServicePinned(svc));
  }, [svc]);
  const [info, setInfo] = useState<Service | null>(null);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [operations, setOperations] = useState<OperationSummary[]>([]);
  // group_id rel C — Raw ⇄ Normalized toggle for the Operations table.
  // Default RAW (forward-only: old windows have no op_group yet). When
  // ON, operations are grouped by their normalized shape (GET /users/:id)
  // instead of raw name. The toggle is opt-in; viewer SEES it (read-only
  // data, no gating). State is local — not URL-persisted — so a shared
  // link lands on the familiar raw view.
  const [normalized, setNormalized] = useState(false);
  // v0.6.51 — this service's SLOs, surfaced as a compact health
  // strip so the service detail page unifies RED + problems +
  // operations + deploys + SLO without bouncing to /slos. listSLOs
  // already pre-computes status (sli/budget/burn), so we filter
  // client-side by service — the list is tens of rows, not worth a
  // dedicated endpoint.
  const [slos, setSlos] = useState<SLORow[]>([]);
  const [loading, setLoading] = useState(true);

  // Memoize the absolute window so JSX-level reads (passed as
  // fromNs/toNs props to child fetchers) don't change identity
  // on every render — without this, a relative range like
  // { preset: '30m' } evaluates a fresh now() each paint and
  // the children's useEffect([fromNs, toNs, …]) deps thrash
  // into an infinite refetch.
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);

  // v0.5.292 — tab in URL so a refresh / shareable link lands
  // on the same sub-view. Default = Operations (the operator's
  // daily entry point).
  // v0.7.97 — Overview is now the DEFAULT landing tab (the at-a-glance
  // health view). Operations / Details are opt-in via ?tab=.
  const tabParam = searchParams.get('tab');
  const tab: ServiceTab = tabParam === 'operations' ? 'operations'
    : tabParam === 'details' ? 'details'
    : tabParam === 'traces' ? 'traces'
    : tabParam === 'logs' ? 'logs'
    : tabParam === 'topology' ? 'topology'
    : 'overview';
  // v0.5.307 — scroll to a hash anchor (#deploys, etc.) once
  // the Details tab body actually exists in the DOM. Browser
  // doesn't auto-scroll because the target node is rendered
  // AFTER the initial paint (bundle fetch + tab gate). The
  // ?tab=details&#deploys link from /deploys depends on this.
  useEffect(() => {
    if (loading) return;
    const hash = window.location.hash;
    if (!hash) return;
    const id = hash.replace(/^#/, '');
    if (!id) return;
    // Wait one frame so the conditional <div id="..."> has
    // landed in the DOM before we try to scroll.
    requestAnimationFrame(() => {
      const el = document.getElementById(id);
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    });
  }, [loading, tab]);
  const setTab = (next: ServiceTab) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (next === 'overview') p.delete('tab'); else p.set('tab', next);
    return p;
  }, { replace: true });

  useEffect(() => {
    if (!svc) return;
    setLoading(true);
    const r = timeRangeToNs(range);
    // Single bundled fetch — the backend fans out KPI lookup,
    // problems list, operations table, and deploy markers to
    // CH in parallel goroutines and ships one JSON response,
    // cached 60s with SWR. At billion-span scale: 4 network
    // round trips + 4 serial CH cold-cache costs collapse
    // into 1 round trip and 1 parallel CH window. Heatmap /
    // RED charts / cluster breakdown stay separate; they
    // own their own time-window semantics (compare period,
    // lazy mount) the bundle can't bake in without coupling.
    //
    // Bundle's deploys slot is hand-seeded into the React
    // Query cache under the deploys-forService key, so the
    // ServiceCharts component's useServiceDeploys hook
    // resolves instantly (cache HIT) instead of firing its
    // own /api/services/.../deploys round trip below the
    // fold. Same window the bundle is for.
    let cancelled = false;
    const applyBundle = (b: Awaited<ReturnType<typeof api.serviceBundle>>) => {
      if (cancelled) return;
      setInfo(b?.service ?? null);
      setProblems(b?.problems ?? []);
      setOperations(b?.operations ?? []);
      if (b?.deploys) {
        queryClient.setQueryData(
          keys.deploys.forService(svc, r.from ?? 0, r.to ?? 0),
          b.deploys,
        );
      }
    };
    api.serviceBundle(svc, r)
      .then(async (b) => {
        applyBundle(b);
        // v0.5.300 — Operator-reported: at scale (test env) the
        // bundle occasionally returned operations=[] even when
        // the service summary itself shows spans > 0. Backend
        // now has an MV→raw-spans fallback (chstore repo), but
        // a stale Redis cache from BEFORE the backend fix might
        // still serve the empty array. Once. Auto-refresh
        // (?refresh=1 bypasses the cache + recomputes) when we
        // detect that signature: service has traffic AND
        // operations came up empty AND the bundle wasn't already
        // forced. Cached afterward so this is a one-shot rescue.
        if (!cancelled
            && b
            && b.service && b.service.spanCount > 0
            && (!b.operations || b.operations.length === 0)) {
          const refreshed = await api.serviceBundle(svc, r, { refresh: true })
            .catch(() => null);
          if (refreshed) applyBundle(refreshed);
        }
      })
      .catch(() => {
        if (cancelled) return;
        setInfo(null); setProblems([]); setOperations([]);
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [svc, range, queryClient]);

  // v0.6.51 — SLO strip. Separate from the bundle because SLO
  // status moves slowly (window_days horizon) and is service-
  // independent of the RED range picker, so it doesn't refetch on
  // every range change. Filter the (small) full list to this
  // service's SLOs.
  useEffect(() => {
    if (!svc) return;
    let cancelled = false;
    api.listSLOs()
      .then(rows => { if (!cancelled) setSlos((rows ?? []).filter(s => s.service === svc)); })
      .catch(() => { if (!cancelled) setSlos([]); });
    return () => { cancelled = true; };
  }, [svc]);

  // group_id rel C — normalized (op_group) operations are fetched
  // lazily and only when the Raw ⇄ Normalized toggle is ON. The raw
  // table already arrives in the bundle, so we don't double-fetch it
  // here; flipping the toggle re-fetches the op_group shape from the
  // same /operations endpoint with normalized=1. The query key carries
  // the `normalized` flag (and svc + window) so the two views cache as
  // separate entries — no raw/normalized cross-poisoning. uses the
  // memoized rangeNs window so it doesn't tick now() each render.
  const normOpsQ = useQuery({
    queryKey: keys.services.operations(svc, { from: rangeNs.from ?? 0, to: rangeNs.to ?? 0 }, true),
    queryFn: () => api.serviceOperations(svc, { from: rangeNs.from ?? 0, to: rangeNs.to ?? 0 }, true),
    enabled: !!svc && normalized,
    staleTime: 60_000,
  });
  // The table's data source flips with the toggle: bundle ops when raw,
  // the op_group query when normalized. Everything downstream (row
  // renderer, useDataTable sort, sparkline) is unchanged — only `rows`.
  const displayedOps = normalized ? (normOpsQ.data ?? []) : operations;
  const opsLoading = normalized && normOpsQ.isLoading;

  if (!svc) {
    return (
      <>
        <Topbar title="Service" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing service name" /></div>
      </>
    );
  }

  const openProbs = problems.filter(p => p.status === 'open');

  return (
    <>
      <Topbar title={`Service · ${svc}`} range={range} onRangeChange={setRange} />
      <div id="content">
        {/* Service identity header (design handoff app.jsx .svc-head): big
            status dot + bare service name + runtime badge + health pill. */}
        {info && (
          <div className="svc-head">
            <div className="svc-title">
              <span className={`ov-dot ${info.errorRate > 5 ? 'red' : info.errorRate > 1 ? 'amber' : 'green'}`} style={{ width: 12, height: 12 }} />
              <h1>{svc}</h1>
              {runtimeQ.data && <ServiceRuntimeBadge rt={runtimeQ.data} compact />}
              <span className={`badge ${info.errorRate > 5 ? 'b-err' : info.errorRate > 1 ? 'b-warn' : 'b-ok'}`}>
                {info.errorRate > 5 ? 'CRITICAL' : info.errorRate > 1 ? 'WARNING' : 'HEALTHY'}
              </span>
            </div>
          </div>
        )}
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          <Link to="/services" className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← All services</Link>
          {info && (
            <>
              <KPI label="Spans" value={fmtNum(info.spanCount)} />
              <KPI label="Errors" value={`${info.errorRate.toFixed(2)}%`}
                cls={info.errorRate > 5 ? 'err' : info.errorRate > 0 ? 'warn' : 'ok'} />
              <KPI label="Avg" value={`${info.avgDurationMs.toFixed(1)}ms`} />
              <KPI label="P99" value={`${info.p99DurationMs.toFixed(1)}ms`} />
            </>
          )}
          {/* Drill chips (v0.5.463) — DrillButton standardises the
              "view in X" cross-page navigation pattern; service +
              range propagate so the destination starts where the
              operator left off. Backtrace, traces, logs, problems,
              anomalies, profiles. */}
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <Button variant="secondary" size="sm"
              title={pinned ? 'Unpin — remove from Cmd-K quick access' : 'Pin — keep this service one keystroke away in Cmd-K'}
              onClick={() => setPinned(toggleServicePin(svc))}>
              {pinned ? '★ Pinned' : '☆ Pin'}
            </Button>
            <DrillButton to="/service/backtrace" params={{ name: svc }}
              title="Inbound callers — service / pod / IP backtrace"
              label="↩ Backtrace" />
            <DrillButton to="/traces" params={{ service: svc }} range={range}
              title="Raw traces filtered to this service"
              label="⋮ Traces" />
            <DrillButton to="/logs" params={{ service: svc }} range={range}
              title="Logs filtered to this service"
              label="≡ Logs" />
            <DrillButton to="/problems" params={{ service: svc }}
              title="Open problems for this service"
              label="⚠ Problems" />
            <DrillButton to="/anomalies" params={{ service: svc }}
              title="Anomaly events for this service"
              label="∿ Anomalies" />
          </div>
        </div>
        {/* Service catalog metadata — owner team / oncall /
            runbook / repo. Operator-curated; falls back to a
            single "+ Add catalog metadata" CTA when empty.
            Editor+ role can edit inline. */}
        <div style={{ marginBottom: 12 }}>
          <ServiceCatalogPill service={svc} />
        </div>

        {/* v0.6.51 — SLO health strip. Unifies SLO status into the
            service detail page (was /slos-only). One chip per SLO:
            target, current SLI, budget bar, burn-rate badge. Click
            jumps to /slos. Hidden when the service has no SLOs. */}
        {slos.length > 0 && (
          <div style={{
            border: '1px solid var(--border)', background: 'var(--bg1)',
            borderRadius: 6, padding: 12, marginBottom: 14,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
              <span style={{ fontWeight: 600, fontSize: 13 }}>
                ◉ SLOs ({slos.length})
              </span>
              <span style={{ flex: 1 }} />
              <Link to="/slos" style={{ fontSize: 11 }}>Manage in SLOs →</Link>
            </div>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: 8,
            }}>
              {slos.map(o => (
                <ServiceSLOChip key={o.id} slo={o} />
              ))}
            </div>
          </div>
        )}

        {openProbs.length > 0 && (
          // Red PROBLEM CALLOUT (design handoff app.jsx .prob-callout) —
          // token-only: a soft red-tinted panel (color-mix keeps it derived
          // from --err, no raw hex) with a 3px red left accent. One card per
          // open problem: severity badge + rule name + the anomaly metric
          // line + description + since-stamp, and a "View all" deep-link.
          <div style={{
            border: '1px solid var(--border)',
            borderLeft: '3px solid var(--err)',
            background: 'color-mix(in oklab, var(--err) 7%, var(--bg1))',
            borderRadius: 6, padding: 12, marginBottom: 14,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
              <span style={{ color: 'var(--err)', fontWeight: 600 }}>
                ! {openProbs.length} open problem{openProbs.length === 1 ? '' : 's'} on {svc}
              </span>
              <span style={{ flex: 1 }} />
              <Link to={`/problems?service=${encodeURIComponent(svc)}`} style={{ fontSize: 11 }}>
                View all for this service →
              </Link>
            </div>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 8,
            }}>
              {openProbs.map(p => {
                const sevCls = p.severity === 'critical' ? 'b-err' : 'b-warn';
                return (
                  <div key={p.id} style={{
                    padding: 8, borderRadius: 4,
                    background: 'var(--bg2)', border: '1px solid var(--border)',
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
                      <span className={`badge ${sevCls}`} style={{ fontSize: 10 }}>
                        {p.severity.toUpperCase()}
                      </span>
                      <span style={{ fontSize: 12, fontWeight: 600 }}>{p.ruleName}</span>
                    </div>
                    <div style={{ fontSize: 11, color: 'var(--text2)' }}>
                      <span style={{ fontFamily: 'monospace' }}>{p.metric}</span>
                      {' = '}
                      <b style={{ color: 'var(--err)' }}>{Number(p.value).toFixed(2)}</b>
                      {' '}(threshold {Number(p.threshold).toFixed(2)})
                    </div>
                    {p.description && (
                      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                        {p.description}
                      </div>
                    )}
                    <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 4, fontFamily: 'monospace' }}>
                      since {new Date(p.startedAt / 1e6).toLocaleString()}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {loading && <Spinner />}
        {!loading && (
          <>
            {/* v0.5.293 — Operator-reported: tabs go immediately
                under the KPI / problems header so the
                Operations table is the FIRST body element on
                the page. DeployHistoryPanel + ServiceCharts
                moved into Details (they remain the headline
                summary view but no longer outrank the
                per-endpoint table). Tab persists in the URL
                so a saved link / refresh lands on the same
                sub-view. */}
            <TabStrip
              tab={tab}
              onChange={setTab}
              opCount={operations.length} />

            {tab === 'overview' && (
              <ServiceOverview service={svc} range={range} info={info} problems={problems} operations={operations} />
            )}
            {tab === 'traces' && <ServiceTracesTab service={svc} range={range} />}
            {tab === 'logs' && <ServiceLogsTab service={svc} range={range} />}
            {tab === 'topology' && <ServiceTopologyTab service={svc} range={range} />}
            {tab === 'operations' && (
              <OperationsTable service={svc} rows={displayedOps} range={range}
                preset={range.preset}
                onWiden={() => setRange({ preset: '1h' })}
                normalized={normalized}
                onToggleNormalized={setNormalized}
                loading={opsLoading} />
            )}
            {tab === 'details' && (
              <>
                {/* v0.5.302 — Above-the-fold panels stay eager
                    (operator sees them instantly); everything
                    further down wraps in <LazyMount> so it
                    fetches only when scrolled into view. Before:
                    Details open fired ~12 parallel CH queries
                    in a burst → CH busy → blank-then-pop flash.
                    Now: 3 immediate queries, the rest queue
                    progressively. Mounted panels stay mounted
                    so scroll-back keeps the data. */}
                {/* v0.8.83 — Operator-requested: upstream/downstream neighbours
                    pulled to the very top of Details; Recent rollouts
                    (DeployHistoryPanel) moved to the bottom of the tab. */}
                <ServiceNeighbors service={svc} since={SINCE_MAP[range.preset] ?? '1h'} defaultOpen />
                {/* Above-the-fold (eager): ServiceInfra + ServiceCharts. */}
                <ServiceInfra     service={svc} since={SINCE_MAP[range.preset] ?? '15m'} />
                <ServiceCharts service={svc} range={range}
                  onZoom={(fromUnixSec, toUnixSec) => {
                    setRange({
                      preset: 'custom',
                      fromMs: Math.round(fromUnixSec * 1000),
                      toMs: Math.round(toUnixSec * 1000),
                    });
                  }} />
                {/* Below-the-fold (lazy): each panel waits
                    until ~200px from the viewport before mounting
                    + fetching. minHeight is sized to the typical
                    rendered panel so the page doesn't jump as
                    panels resolve. */}
                {/* Profiling tile — Dynatrace-style "Top methods"
                    card. Self-hides when the service hasn't
                    pushed profiles, so it's a no-op for
                    services not yet wired up. */}
                <LazyMount minHeight={120}>
                  <ServiceProfilingPanel service={svc} range={range} />
                </LazyMount>
                {/* Attrs browser (v0.5.381) — "what attrs is my
                    SDK emitting" answered without opening a
                    trace. Self-hides when no sample data lands
                    yet (fresh services). */}
                <LazyMount minHeight={120}>
                  <ServiceAttrsPanel service={svc} range={range} />
                </LazyMount>
                <LazyMount minHeight={300}>
                  <ServiceStructure service={svc} since={SINCE_MAP[range.preset] ?? '1h'} defaultOpen />
                </LazyMount>
                <LazyMount minHeight={140}>
                  <ServiceClusterBreakdown service={svc} range={range} />
                </LazyMount>
                <LazyMount minHeight={300}>
                  <DBQueriesPanel   service={svc}
                                    from={rangeNs.from}
                                    to={rangeNs.to}
                                    defaultOpen />
                </LazyMount>
                <LazyMount minHeight={360}>
                  <ServiceLatencyHeatmap service={svc} range={range} />
                </LazyMount>
                {/* Recent rollouts — moved to the bottom of Details (v0.8.83).
                    #deploys anchor preserved so the /deploys "history →" link
                    still scrolls here; LazyMount fetches on intersection. */}
                <div id="deploys">
                  <LazyMount minHeight={160}>
                    <DeployHistoryPanel service={svc} />
                  </LazyMount>
                </div>
              </>
            )}
          </>
        )}
      </div>
    </>
  );
}

function KPI({ label, value, cls }: { label: string; value: string; cls?: string }) {
  return (
    <div style={{
      padding: '4px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', fontSize: 12,
    }}>
      <span style={{ color: 'var(--text2)' }}>{label}: </span>
      <b style={{
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)'
          : cls === 'ok' ? 'var(--ok)' : 'var(--text)',
      }}>{value}</b>
    </div>
  );
}

// ServiceSLOChip — compact one-SLO health card for the service
// detail page's SLO strip (v0.6.51). Target + current SLI + a
// budget-remaining bar + burn-rate badge. Self-contained so the
// strip stays a simple .map(); links to /slos for full management.
function ServiceSLOChip({ slo }: { slo: SLORow }) {
  const st = slo.status;
  const healthy = st?.healthy ?? true;
  const budget = st ? Math.max(0, Math.min(1, st.budgetRemaining)) : 1;
  // Budget bar tint: green > 25% left, amber 0–25%, red exhausted.
  const budgetCls = budget > 0.25 ? 'var(--ok)' : budget > 0 ? 'var(--warn)' : 'var(--err)';
  const burn = st?.burnRate ?? 0;
  return (
    <div style={{
      padding: 10, borderRadius: 6,
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderLeft: `3px solid ${healthy ? 'var(--ok)' : 'var(--err)'}`,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>{slo.name}</span>
        <span style={{ flex: 1 }} />
        <span className={`badge ${healthy ? 'b-ok' : 'b-err'}`} style={{ fontSize: 10 }}>
          {healthy ? 'Healthy' : 'Breached'}
        </span>
      </div>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 6 }}>
        {slo.sliType === 'latency' ? `latency ≤ ${slo.thresholdMs}ms` : 'availability'}
        {' · target '}<b>{(slo.target * 100).toFixed(2)}%</b>
        {st && <> · SLI <b style={{ color: healthy ? 'var(--ok)' : 'var(--err)' }}>{(st.sli * 100).toFixed(2)}%</b></>}
      </div>
      {st && (
        <>
          {/* Budget-remaining bar */}
          <div style={{ height: 6, borderRadius: 3, background: 'var(--bg0)', overflow: 'hidden', marginBottom: 4 }}>
            <div style={{ height: '100%', width: `${budget * 100}%`, background: budgetCls }} />
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 10, color: 'var(--text3)' }}>
            <span>{(budget * 100).toFixed(0)}% budget left</span>
            <span title="Error-budget burn rate (>1 = consuming faster than allowed)"
              style={{ color: burn > 1 ? 'var(--err)' : 'var(--text3)' }}>
              burn {burn.toFixed(2)}×
            </span>
          </div>
        </>
      )}
      {!st && <div style={{ fontSize: 10, color: 'var(--text3)' }}>no status yet</div>}
    </div>
  );
}

// OperationsTable — per-operation aggregate (count / err / avg / p50 /
// p95 / p99 / apdex). Click an operation to drill into Traces with
// the service + operation name pre-filtered. Sortable; aggregate
// "All" row at the top mirrors the services page so totals are visible
// without scrolling.
//
// v0.7.54 — adopted the shared sortable + resizable table primitive
// (useDataTable / DataTableHead / DataTableColgroup). The bespoke
// OpSortKey state + OpSortTh header + manual .sort() were replaced by
// the OP_COLS column defs below; client-side FILTER (name substring)
// stays and feeds the hook as `rows`. Default sort preserved: impact
// desc (Elastic-APM's heaviest-cumulative-consumer first). Trend /
// P50 / P95 stay non-sortable (no sortValue) — they had no sort before.
function impactOf(r: OperationSummary): number {
  return r.avgDurationMs * r.spanCount;
}

// Elastic-APM's "Impact" = avg_duration × count. Surfaces the
// heaviest cumulative consumers — the operation that's slow OR
// runs a lot. A 5ms operation called 100k times shows up; a
// once-an-hour 30s job doesn't. Default sort so the top of the
// table answers "what should I optimise first" without the
// operator combining columns by eye.
// Apdex cell color (README Status semantics): ≥0.94 ok, ≥0.85 warn, else err.
function apdexColor(a: number): string {
  if (!isFinite(a)) return 'var(--text3)';
  return a >= 0.94 ? 'var(--ok)' : a >= 0.85 ? 'var(--warn)' : 'var(--err)';
}

const OP_COLS: DataTableColumn<OperationSummary>[] = [
  { id: 'name',      label: 'Operation', sortValue: r => r.name,            naturalDir: 'asc',  width: 320 },
  { id: 'trend',     label: 'Trend',     width: 92 },
  { id: 'impact',    label: 'Impact',    sortValue: r => impactOf(r),       numeric: true,      width: 130 },
  { id: 'spanCount', label: 'Calls',     sortValue: r => r.spanCount,       numeric: true,      width: 96 },
  { id: 'errorRate', label: 'Err %',     sortValue: r => r.errorRate,       numeric: true,      width: 84 },
  { id: 'avg',       label: 'Avg',       sortValue: r => r.avgDurationMs,   numeric: true,      width: 84 },
  { id: 'p50',       label: 'P50',       numeric: true,                     width: 84 },
  { id: 'p95',       label: 'P95',       numeric: true,                     width: 84 },
  { id: 'p99',       label: 'P99',       sortValue: r => r.p99DurationMs,   numeric: true,      width: 84 },
  { id: 'apdex',     label: 'Apdex',     sortValue: r => r.apdex ?? 0,      numeric: true,      naturalDir: 'asc', width: 84 },
];

// v0.5.292 — TabStrip sits between the persistent header
// (KPIs / problems / deploy markers / RED charts) and the
// per-tab body. Style mirrors the existing Topology tabs
// (no separate component yet; both surfaces use a plain
// button row + active-tab outline).
function TabStrip({ tab, onChange, opCount }: {
  tab: ServiceTab;
  onChange: (t: ServiceTab) => void;
  opCount: number;
}) {
  const items: { key: ServiceTab; label: string; hint?: string }[] = [
    { key: 'overview',   label: 'Overview' },
    { key: 'operations', label: 'Operations', hint: opCount > 0 ? `${opCount}` : undefined },
    { key: 'details',    label: 'Details' },
    { key: 'topology',   label: 'Topology' },
    { key: 'traces',     label: 'Traces' },
    { key: 'logs',       label: 'Logs' },
  ];
  return (
    <div style={{
      display: 'flex', gap: 0, marginTop: 16, marginBottom: 12,
      borderBottom: '1px solid var(--border)',
      // Sticky tab strip (design): stays pinned to the top of the #main
      // scroll viewport while the body scrolls under it. Page bg masks the
      // content; z-index keeps it above the scrolling panels.
      position: 'sticky', top: 0, zIndex: 5,
      background: 'var(--bg0)', paddingTop: 8,
    }}>
      {items.map(it => {
        const active = tab === it.key;
        return (
          <button key={it.key} type="button"
            onClick={() => onChange(it.key)}
            style={{
              all: 'unset', cursor: 'pointer',
              padding: '8px 18px',
              fontSize: 13, fontWeight: active ? 700 : 500,
              color: active ? 'var(--text)' : 'var(--text2)',
              borderBottom: active ? '2px solid var(--accent2)' : '2px solid transparent',
              marginBottom: -1,
            }}>
            {it.label}
            {it.hint && (
              <span style={{
                marginLeft: 6, fontSize: 11, color: 'var(--text3)',
                fontFamily: 'ui-monospace, monospace',
              }}>{it.hint}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

function OperationsTable({ service, rows, range, preset, onWiden, normalized, onToggleNormalized, loading }: {
  service: string;
  rows: OperationSummary[];
  range: TimeRange;
  // v0.5.292 — when the table comes back empty (typically
  // because the user's 15m default window had no traffic),
  // surface a one-click "widen to 1h" CTA rather than the
  // bare "no operations" message. preset is read to scope
  // the suggestion ("widen to 1h" only makes sense on short
  // windows; on a 7d range, empty really means empty).
  preset?: string;
  onWiden?: () => void;
  // group_id rel C — Raw ⇄ Normalized toggle. normalized reflects
  // the current mode; onToggleNormalized flips it (the parent owns
  // the fetch). loading covers the normalized refetch so the table
  // shows a Spinner instead of flashing the previous mode's rows.
  normalized: boolean;
  onToggleNormalized: (v: boolean) => void;
  loading: boolean;
}) {
  // v0.5.374 — client-side filter. At 500+ operations on a
  // monolith service the scroll-then-eyeball loop fails;
  // typing narrows live with no server round-trip.
  const [filter, setFilter] = useState('');
  const navigate = useNavigate();
  // Filter input ref — wired into useDataTable's searchRef so the
  // app-wide "/" shortcut focuses it (UX#4 keyboard nav).
  const searchRef = useRef<HTMLInputElement>(null);
  // v0.5.392 — per-row metric drill-in. Clicking the sparkline
  // opens a Modal with three synced uPlot charts (calls, errors,
  // p99) for the same (service, op) tuple. Same pattern the
  // endpoints page uses; here it pulls from the row's stored
  // sparkline + companion errors/p99 sparklines added in the
  // same release.
  const [opDetail, setOpDetail] = useState<OperationSummary | null>(null);

  // v0.5.313 — Operator-reported: drill-down used to land on
  // /traces (familiar view with the trace list + aggregate
  // tabs). Recent refactor pushed it to /explore which the
  // operator finds less direct. Reverted to /traces with the
  // service pre-selected and the operation name as the search
  // term. /traces' free-text search matches span name out of
  // the box, so an operation like "POST /payment" lands on
  // exactly the traces that touched it.
  // v0.5.317 — Operator-reported: prior link landed on the
  // Aggregated tab (default) where filter+search produced no
  // results, and rootOnly defaulted ON, hiding partial traces.
  // Now: explicit ?view=list&rootOnly=false so the operator
  // lands on the list view with every matching trace visible.
  const opHref = (op: string) =>
    `/traces?service=${encodeURIComponent(service)}&search=${encodeURIComponent(op)}&range=${encodeURIComponent(encodeRange(range))}&view=list&rootOnly=false`;

  // v0.5.374 — client-side filter. Case-insensitive substring
  // match on the operation name, same idiom as the /endpoints
  // page filter. The shared table primitive (useDataTable below)
  // owns the SORT half; we feed it this filtered array as `rows`.
  const filtered = useMemo(() => {
    const trimmed = filter.trim().toLowerCase();
    return trimmed ? rows.filter(r => r.name.toLowerCase().includes(trimmed)) : rows;
  }, [rows, filter]);

  // Same weighted-aggregate scheme as the services page totals row:
  // sum spans/errs, weight avg/apdex by span count, take max for p99.
  const agg = useMemo(() => {
    if (rows.length === 0) return null;
    let totalSpans = 0, totalErrs = 0, wAvg = 0, wApdex = 0, maxP99 = 0;
    for (const r of rows) {
      totalSpans += r.spanCount;
      totalErrs += r.errorCount;
      wAvg += r.avgDurationMs * r.spanCount;
      wApdex += (r.apdex ?? 0) * r.spanCount;
      if (r.p99DurationMs > maxP99) maxP99 = r.p99DurationMs;
    }
    return {
      spans: totalSpans, errs: totalErrs,
      errorRate: totalSpans > 0 ? (totalErrs / totalSpans) * 100 : 0,
      avgMs: totalSpans > 0 ? wAvg / totalSpans : 0,
      p99Ms: maxP99,
      apdex: totalSpans > 0 ? wApdex / totalSpans : 0,
    };
  }, [rows]);

  // Element-wise sum of every operation's sparkline → service-wide
  // call-rate trend rendered on the "All" aggregate row. Uses the
  // longest sparkline as the canvas length; shorter ones (only one
  // bucket of activity, e.g. a single-call operation) just contribute
  // zeros to the trailing slots.
  const aggSparkline = useMemo(() => {
    let len = 0;
    for (const r of rows) {
      if (r.sparkline && r.sparkline.length > len) len = r.sparkline.length;
    }
    if (len === 0) return [];
    const out = new Array(len).fill(0);
    for (const r of rows) {
      if (!r.sparkline) continue;
      for (let i = 0; i < r.sparkline.length; i++) {
        out[i] += r.sparkline[i];
      }
    }
    return out;
  }, [rows]);

  // Shared sortable + resizable table primitive (v0.7.54). Feed the
  // FILTERED rows so sorting acts on what's visible; default sort
  // preserved as impact desc. Hook is unconditional + above the
  // empty-state early return (rules-of-hooks).
  // v0.7.x — app-wide keyboard nav (UX#4). onOpen drills the selected
  // operation into /traces (service + name pre-filtered, same as the row
  // Link's opHref); searchRef binds "/" to focus the filter input; j/k move
  // the row selection and Enter/o open. dt.rowProps(i) on each <tr> paints
  // the .row-selected accent + the data-row-idx the auto-scroll needs.
  const dt = useDataTable<OperationSummary>({
    storageKey: 'service-operations',
    columns: OP_COLS,
    rows: filtered,
    initialSort: { id: 'impact', dir: 'desc' },
    onOpen: (op) => navigate(opHref(op.name)),
    searchRef,
  });

  // group_id rel C — the Raw ⇄ Normalized toggle + helper caption.
  // Rendered above EVERY state (loading / empty / populated) so the
  // operator can always flip back to raw — never trap them in a
  // normalized-empty view with no escape. Reuses the shared <Button>
  // atom (the v0.7.54 one-design-language rule); no hand-rolled button
  // styles. Viewer SEES the toggle — read-only data, no gating.
  const modeToggle = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginLeft: 'auto' }}>
      <span style={{ display: 'inline-flex', gap: 4 }}>
        <Button variant={normalized ? 'ghost' : 'secondary'} size="sm"
          onClick={() => onToggleNormalized(false)}
          title="Show operations by raw span name">Raw</Button>
        <Button variant={normalized ? 'secondary' : 'ghost'} size="sm"
          onClick={() => onToggleNormalized(true)}
          title="Collapse id-bearing operations into shapes (GET /users/:id)">Normalized</Button>
      </span>
      <span style={{ fontSize: 11, color: 'var(--text3)', maxWidth: 320, lineHeight: 1.3 }}>
        collapse id-bearing operations into shapes — <code>GET /users/:id</code>
      </span>
    </div>
  );

  // Loading covers the normalized refetch (raw arrives in the bundle).
  if (loading) {
    return (
      <div style={{ marginTop: 18 }}>
        <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
          <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
          {modeToggle}
        </div>
        <Spinner />
      </div>
    );
  }

  if (rows.length === 0) {
    // group_id rel C — normalized-empty is a DIFFERENT story than
    // raw-empty: it's not "no traffic", it's "no op_group shapes in
    // this window yet" (forward-only — grouping starts with newly-
    // ingested spans, so old windows legitimately have none). Honest
    // <Empty> message + the toggle stays visible so the operator flips
    // back to raw without losing the page.
    if (normalized) {
      return (
        <div style={{ marginTop: 18 }}>
          <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
            <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
            {modeToggle}
          </div>
          <Empty icon="∅" title="No normalized shapes in this window">
            Normalized grouping starts with newly-ingested spans — no shapes in this window yet.
          </Empty>
        </div>
      );
    }
    // v0.5.292 — short-window (≤30 min) default hits "no
    // traffic" often enough that operators reported the page
    // as broken. Surface a one-click widen-to-1h instead of
    // the bare-empty message. Wider windows keep the plain
    // empty state since "no ops in 24h" is genuinely "no
    // ops".
    const isShortWindow = preset
      && ['5m', '10m', '15m', '30m'].includes(preset);
    return (
      <div style={{ marginTop: 18 }}>
        <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
          <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
          {modeToggle}
        </div>
        <div className="empty" style={{ padding: 30 }}>
          {isShortWindow ? (
            <>
              <div style={{ marginBottom: 12 }}>
                No traffic for <b>{service}</b> in the last <b>{preset}</b>.
                Idle or low-traffic services often don't produce
                spans in a short window.
              </div>
              {onWiden && (
                <button type="button" onClick={onWiden}
                  style={{ fontSize: 12, padding: '6px 14px' }}>
                  Widen to last 1h
                </button>
              )}
            </>
          ) : (
            <>No operations seen in this window</>
          )}
        </div>
      </div>
    );
  }

  return (
    <div style={{ marginTop: 18 }}>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
        <h3 style={{ fontSize: 13, fontWeight: 700 }}>⊙ Operations</h3>
        {modeToggle}
      </div>
      <div style={{ marginBottom: 6, display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {normalized && <b style={{ color: 'var(--text2)' }}>normalized · </b>}
          {filter.trim()
            ? `${dt.sortedRows.length} / ${rows.length} matching`
            : `${rows.length} ${normalized ? 'operation shape' : 'distinct span name'}${rows.length === 1 ? '' : 's'} in ${service}`}
        </span>
        <input ref={searchRef} className="field" value={filter} onChange={e => setFilter(e.target.value)}
          placeholder="Filter by name…  ( / to focus, j/k to move, Enter to open )"
          style={{ marginLeft: 'auto', width: 320 }} />
      </div>
      {/* v0.5.462 — operator-reported: the previous maxHeight:540
          inner-scroll wrapper made even a 50-op service feel
          claustrophobic. Virtualization via content-visibility:auto
          on each row (set below) handles the 500+ op perf case
          per CLAUDE.md's "tables > 100 rows" guidance, so the
          inner scroll isn't earning its keep.
          v0.7.54 — header is now the shared <DataTableHead> (sortable
          + per-column resize); fixed layout via the <colgroup> +
          tableLayout:fixed. Body cell order tracks OP_COLS:
          Operation · Trend · Impact · Calls · Err% · Avg · P50 · P95 ·
          P99 · Apdex. */}
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {agg && (
              <tr className="agg-row">
                <td><span style={{ fontWeight: 700 }}>All ({rows.length})</span></td>
                <td>
                  {/* Aggregate trend = element-wise sum across all
                      per-operation sparklines so the "All" row
                      shows the service-wide call rate at a glance,
                      using the same window + bucket boundaries as
                      every row beneath it.
                      v0.6.13 — earlier (v0.6.10) wrapped this in a
                      /metrics link, but /metrics needs a *metric
                      name* to render and there's no natural pick
                      from a spans-aggregate sparkline, so the
                      drill landed on a blank page (operator-
                      reported). Reverted to a plain Sparkline —
                      this page is already the service detail, so
                      a self-link wouldn't help anyway. */}
                  <Sparkline values={aggSparkline} title={`total calls/bucket × ${rows.length} ops`} />
                </td>
                <td className="mono" style={{ textAlign: 'right', fontWeight: 700 }}>
                  {fmtImpact(rows.reduce((n, r) => n + impactOf(r), 0))}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(agg.spans)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  <span className={`badge b-${agg.errorRate > 5 ? 'err' : agg.errorRate > 0 ? 'warn' : 'ok'}`}>
                    {agg.errorRate.toFixed(2)}%
                  </span>
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{agg.avgMs.toFixed(1)}ms</td>
                <td className="mono" style={{ textAlign: 'right', color: 'var(--text3)' }}>—</td>
                <td className="mono" style={{ textAlign: 'right', color: 'var(--text3)' }}>—</td>
                <td className="mono" style={{ textAlign: 'right' }}>{agg.p99Ms.toFixed(1)}ms</td>
                <td className="mono" style={{ textAlign: 'right', color: apdexColor(agg.apdex), fontWeight: 600 }}>
                  {isFinite(agg.apdex) ? agg.apdex.toFixed(2) : '—'}
                </td>
              </tr>
            )}
            {dt.sortedRows.map((op, i) => {
              const errCls = op.errorRate > 5 ? 'err' : op.errorRate > 0 ? 'warn' : 'ok';
              // Tone the per-row sparkline with the same severity
              // colour as the err-rate badge so the eye reads "this
              // op is hot" from one glance at the trend column,
              // before reading the numbers.
              const sparkColor = errCls === 'err' ? 'var(--err)'
                              : errCls === 'warn' ? 'var(--warn)'
                              : undefined;
              // dt.rowProps(i) → data-row-idx (auto-scroll target) +
              // .row-selected accent when j/k lands here. Index tracks
              // dt.sortedRows (the agg "All" row above is NOT counted).
              const rp = dt.rowProps(i);
              return (
                <tr key={op.name} {...rp}
                    style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                  <td>
                    <Link
                      to={opHref(op.name)}
                      style={{ fontWeight: 500 }}
                      title="Open this operation in Traces — service + name pre-filtered"
                    >{op.name}</Link>
                  </td>
                  <td>
                    <button
                      type="button"
                      onClick={() => setOpDetail(op)}
                      title={`${fmtNum(op.spanCount)} calls — click for calls / errors / p99 detail`}
                      style={{
                        background: 'transparent', border: 0, padding: 0,
                        cursor: 'pointer', display: 'inline-block',
                      }}
                    >
                      <Sparkline values={op.sparkline ?? []}
                        color={sparkColor}
                        title="" />
                    </button>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <ImpactBar value={impactOf(op)}
                               max={Math.max(...rows.map(impactOf))} />
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(op.spanCount)}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>
                    <span className={`badge b-${errCls === 'err' ? 'err' : errCls === 'warn' ? 'warn' : 'ok'}`}>
                      {op.errorRate.toFixed(2)}%
                    </span>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.avgDurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p50DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p95DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{op.p99DurationMs.toFixed(1)}ms</td>
                  <td className="mono" style={{ textAlign: 'right', color: apdexColor(op.apdex), fontWeight: 600 }}>
                    {isFinite(op.apdex) ? op.apdex.toFixed(2) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <OperationMetricModal
        service={service}
        op={opDetail}
        onClose={() => setOpDetail(null)}
        range={range}
      />
    </div>
  );
}

// OperationMetricModal — opens on per-op sparkline click. Same
// three-RED-dimensions pattern as the Endpoints modal: calls,
// errors, p99 latency, drawn as full uPlot charts with a synced
// crosshair so the operator correlates spikes across all three
// at one instant. v0.5.392 — applies the metric drill-in pattern
// to /service per-operation rows so the operator gets the same
// reading affordance everywhere they see a sparkline.
function OperationMetricModal({
  service, op, onClose, range,
}: {
  service: string;
  op: OperationSummary | null;
  onClose: () => void;
  range: TimeRange;
}) {
  const series = useMemo(() => {
    if (!op) return { calls: [] as SpanMetricSeries[], errors: [] as SpanMetricSeries[], p99: [] as SpanMetricSeries[] };
    const { from, to } = timeRangeToNs(range);
    const calls = op.sparkline ?? [];
    const errs = op.errorsSparkline ?? [];
    const p99s = op.p99Sparkline ?? [];
    const n = Math.max(calls.length, errs.length, p99s.length);
    if (n === 0 || to <= from) {
      return { calls: [], errors: [], p99: [] };
    }
    const bucketNs = (to - from) / n;
    const t = (i: number) => from + bucketNs * i + bucketNs / 2;
    const pts = (arr: number[]) => arr.map((v, i) => ({ time: t(i), value: v }));
    return {
      calls: calls.length ? [{ groupKey: ['calls'], points: pts(calls) }] : [],
      errors: errs.length ? [{ groupKey: ['errors'], points: pts(errs) }] : [],
      p99: p99s.length ? [{ groupKey: ['p99 ms'], points: pts(p99s) }] : [],
    };
  }, [op, range]);

  if (!op) return <Modal open={false} onClose={onClose} />;
  const peakCalls = (op.sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const totalErrs = (op.errorsSparkline ?? []).reduce((s, v) => s + v, 0);
  const maxP99 = (op.p99Sparkline ?? []).reduce((m, v) => Math.max(m, v), 0);
  const errCls = op.errorRate >= 5 ? 'err' : op.errorRate >= 1 ? 'warn' : '';
  const tracesHref =
    `/traces?service=${encodeURIComponent(service)}` +
    `&search=${encodeURIComponent(op.name)}` +
    `&range=${encodeURIComponent(encodeRange(range))}` +
    `&view=list&rootOnly=false`;

  // v0.8.x — drill from this popup to /explore, operation-scoped.
  // Mirrors the v0.6.55 /services sparkline→/explore pattern
  // (Services.tsx goToExplore) but carries TWO filters: the service
  // pin AND the span name, so the operator lands on the exact metric
  // chart for THIS (service, operation) tuple with the clicked
  // aggregation preselected (Calls→rate, Errors→error_rate, P99→p99).
  // We reuse the SAME legacy ?result=metric URL shape that
  // urlCodec.seedFromLegacyParams decodes — extractScope lifts
  // service.name into the builder scope, `name=` stays a chip
  // (model.pinnedOperation reads it). No new URL scheme invented.
  const exploreHref = (agg: SpanAgg) => {
    const filters = encodeFilters([
      { k: 'service.name', op: '=', v: [service] },
      { k: 'name', op: '=', v: [op.name] },
    ]);
    return `/explore?${buildQuery([
      ['range', encodeRange(range)],
      ['filters', filters],
      ['agg', agg],
      ['field', 'duration_ms'],
      ['result', 'metric'],
    ])}`;
  };
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span className="mono" style={{ fontSize: 13 }}>
          {op.name}
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            ({service})
          </span>
        </span>
      }
    >
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)',
        gap: 12, marginBottom: 14,
      }}>
        <OpMetricTile label="Calls" big={fmtNum(op.spanCount)}
          sub={`peak ${fmtNum(peakCalls)} / bucket`}
          series={series.calls} />
        <OpMetricTile label="Errors" big={fmtNum(op.errorCount)}
          sub={`${op.errorRate.toFixed(2)}% rate`}
          subCls={errCls}
          series={series.errors} />
        <OpMetricTile label="P99 latency"
          big={`${op.p99DurationMs.toFixed(0)} ms`}
          sub={`peak ${maxP99.toFixed(0)} ms · avg ${op.avgDurationMs.toFixed(0)} ms`}
          series={series.p99} unit="ms" />
      </div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14 }}>
        Hover any chart to read the bucket value; crosshair syncs
        across all three so you can correlate calls / errors /
        p99 at the same instant. Total errors in window:
        {' '}<strong>{fmtNum(totalErrs)}</strong>.
      </div>
      <div style={{ display: 'flex', gap: 14, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <Link to={tracesHref} style={{ fontSize: 12, color: 'var(--accent2)' }}>
          View traces →
        </Link>
        {/* v0.8.x — open this (service, operation) tuple in Explore,
            carrying the clicked metric's aggregation so the operator
            lands on the exact chart with the full builder toolbar.
            Mirrors the /services sparkline→/explore drill (v0.6.55),
            operation-scoped. */}
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>Explore:</span>
        <Link to={exploreHref('rate')} style={{ fontSize: 12, color: 'var(--accent2)' }}
          title={`Open call rate for ${op.name} in Explore (service + operation scoped)`}>
          Calls →
        </Link>
        <Link to={exploreHref('error_rate')} style={{ fontSize: 12, color: 'var(--accent2)' }}
          title={`Open error rate for ${op.name} in Explore (service + operation scoped)`}>
          Errors →
        </Link>
        <Link to={exploreHref('p99')} style={{ fontSize: 12, color: 'var(--accent2)' }}
          title={`Open p99 latency for ${op.name} in Explore (service + operation scoped)`}>
          P99 →
        </Link>
      </div>
    </Modal>
  );
}

function OpMetricTile({
  label, big, sub, subCls, series, unit,
}: {
  label: string; big: string; sub: string; subCls?: string;
  series: SpanMetricSeries[]; unit?: string;
}) {
  return (
    <div style={{
      padding: '10px 12px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 600, marginBottom: 2 }}>{big}</div>
      <div style={{
        fontSize: 11, marginBottom: 8,
        color: subCls === 'err' ? 'var(--err)' : subCls === 'warn' ? 'var(--warn)' : 'var(--text3)',
      }}>{sub}</div>
      {series.length > 0 && series[0].points.length > 0 ? (
        <MultiLineChart series={series} unit={unit} height={140} syncKey="op-detail" />
      ) : (
        <div style={{
          height: 140, display: 'flex', alignItems: 'center',
          justifyContent: 'center', color: 'var(--text3)', fontSize: 11,
        }}>no data in window</div>
      )}
    </div>
  );
}

// ImpactBar renders a horizontal proportion bar + numeric label —
// Elastic APM's signature pattern in the transaction list. Bar
// width = row impact / max impact across the visible rows so the
// busiest operation always fills the cell. Keeps the heaviest
// cumulative consumer visually obvious without forcing the
// operator to read tabular numbers.
function ImpactBar({ value, max }: { value: number; max: number }) {
  const pct = max > 0 ? (value / max) * 100 : 0;
  return (
    <div style={{ position: 'relative', minWidth: 90, display: 'inline-block' }}>
      <div style={{
        position: 'absolute', inset: 0, width: `${pct}%`,
        background: 'color-mix(in oklab, var(--accent) 12%, transparent)',
        borderRadius: 3,
      }} />
      <span style={{ position: 'relative', paddingRight: 4 }}>
        {fmtImpact(value)}
      </span>
    </div>
  );
}

// fmtImpact renders impact in time units. Below a second we keep
// ms with at most one decimal; past a second we promote to s/min/h
// so a 30M-ms ops job reads as "8.3h" rather than "30000000".
function fmtImpact(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)}ms`;
  if (ms < 1000) return `${ms.toFixed(1)}ms`;
  const sec = ms / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  const min = sec / 60;
  if (min < 60) return `${min.toFixed(1)}min`;
  const hr = min / 60;
  return `${hr.toFixed(1)}h`;
}

export default function ServiceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ServiceDetailInner />
    </Suspense>
  );
}

// ServiceClusterBreakdown — sortable RED stats per cluster the
// service emitted spans from. Renders silently when there's
// only one cluster (or none), so single-cluster operators don't
// see noise. Click a row to pivot to /services?cluster=<name>
// scoped to that one cluster. The sort defaults to spanCount
// desc so the heaviest cluster lands at top — usually what an
// operator triaging "is this service slow?" wants first.
const CLUSTER_COLS: DataTableColumn<import('@/lib/types').ServiceClusterStat>[] = [
  { id: 'cluster', label: 'Cluster', sortValue: r => r.cluster,       naturalDir: 'asc', width: 220 },
  { id: 'calls',   label: 'Calls',   sortValue: r => r.spanCount,     numeric: true,     width: 110 },
  { id: 'errRate', label: 'Err %',   sortValue: r => r.errorRate,     numeric: true,     width: 90 },
  { id: 'avg',     label: 'Avg',     sortValue: r => r.avgDurationMs, numeric: true,     width: 90 },
  { id: 'p99',     label: 'P99',     sortValue: r => r.p99DurationMs, numeric: true,     width: 90 },
];

function ServiceClusterBreakdown({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  // v0.8.116 — fetch via React Query under a key shared with
  // ServiceLatencyHeatmap's cluster dropdown, so the two collapse into one
  // round trip instead of issuing the same serviceClusters call twice.
  const q = useQuery({
    queryKey: ['service-clusters', service, from, to],
    queryFn: () => api.serviceClusters(service, from, to),
    enabled: !!service && from > 0,
    staleTime: 30_000,
  });
  const clusters = useMemo(() => q.data?.clusters ?? [], [q.data]);

  // v0.8.116 — adopt the shared sortable + resizable primitive. This panel
  // previously hand-rolled sort via ClusterTh/ClusterSortKey — the exact
  // anti-pattern CLAUDE.md's "never hand-roll sort/resize" constraint names.
  // Hook is unconditional + above the <2-cluster early return.
  const dt = useDataTable<import('@/lib/types').ServiceClusterStat>({
    storageKey: 'service-clusters',
    columns: CLUSTER_COLS,
    rows: clusters,
    initialSort: { id: 'calls', dir: 'desc' },
  });

  // Silent when fewer than 2 clusters — single-cluster (or zero-cluster,
  // e.g. SDK without resource attrs) deployments don't need the panel.
  // Loading / error states stay quiet for the same reason.
  if (clusters.length < 2) return null;

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Per-cluster breakdown <span style={{
          fontWeight: 400, color: 'var(--text3)', textTransform: 'none',
        }}>· {clusters.length} clusters</span>
      </div>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(c => {
              const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
              return (
                <tr key={c.cluster}>
                  <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    <Link to={`/services?cluster=${encodeURIComponent(c.cluster)}`}
                          style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}
                          title={`Filter /services to cluster ${c.cluster}`}>
                      {c.cluster}
                    </Link>
                  </td>
                  <td className="num mono">{fmtNum(c.spanCount)}</td>
                  <td className="num mono">
                    <span className={`badge b-${errCls}`}>{c.errorRate.toFixed(2)}%</span>
                  </td>
                  <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                  <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ServiceLatencyHeatmap fetches the heatmap for the current
// service + window and renders it under a collapsible
// section. Uses the existing /api/spans/heatmap endpoint
// with a single service_name filter — that endpoint already
// uses the primary-key partition prune so this is cheap
// even on a 24h window.
function ServiceLatencyHeatmap({ service, range }: {
  service: string;
  range: import('@/lib/types').TimeRange;
}) {
  const [data, setData] = useState<import('@/lib/types').LatencyHeatmap | null | undefined>(undefined);
  const [picked, setPicked] = useState<string>(''); // '' = all
  // Collapse state — defaults open. Persisted to localStorage so an operator
  // who'd rather hide the panel doesn't fight it on every reload. Keyed
  // globally (not per-service) so the preference is a one-time setting.
  const [collapsed, setCollapsed] = useState<boolean>(
    () => getRaw(STORAGE_KEYS.svcHeatmapCollapsed) === '1');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  // v0.8.116 — the parent already wraps this panel in <LazyMount> (mounts
  // within 200px of the viewport), so the former in-component
  // IntersectionObserver/hasBeenVisible gate was a redundant second lazy
  // layer and was removed. Fetches gate on !collapsed alone.

  // Cluster set for the pivot dropdown — a service usually runs across several
  // clusters at once and the operator pivots the heatmap to one (or the union
  // "All clusters" default). Shares ServiceClusterBreakdown's query key so the
  // two panels collapse into a single serviceClusters round trip.
  const clustersQ = useQuery({
    queryKey: ['service-clusters', service, from, to],
    queryFn: () => api.serviceClusters(service, from, to),
    enabled: !!service && from > 0 && !collapsed,
    staleTime: 30_000,
  });
  const clusters = useMemo(
    () => (clustersQ.data?.clusters ?? []).map(c => c.cluster),
    [clustersQ.data],
  );
  // If the previously-picked cluster vanished from the window (window moved
  // past its traffic), drop back to "All" instead of querying for nothing.
  useEffect(() => {
    if (picked && !clusters.includes(picked)) setPicked('');
  }, [clusters, picked]);

  useEffect(() => {
    if (collapsed) return;
    setData(undefined);
    const f: { key: string; op: string; value: string }[] = [
      { key: 'service.name', op: '=', value: service },
    ];
    if (picked) {
      // Hit the resource-attr key directly. The OTLP ingest path materialises
      // k8s.cluster.name as a span attr, so a single predicate is enough (no
      // coalesce across resource + span attrs needed at query time).
      f.push({ key: 'k8s.cluster.name', op: '=', value: picked });
    }
    api.spanHeatmap({ from, to, filters: JSON.stringify(f), buckets: 60 })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [service, from, to, collapsed, picked]);

  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    setRaw(STORAGE_KEYS.svcHeatmapCollapsed, next ? '1' : '0');
  };

  return (
    <div style={{ marginTop: 24, marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6,
      }}>
        <button type="button" onClick={toggle}
          style={{
            all: 'unset', cursor: 'pointer',
            fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.4,
            display: 'inline-flex', alignItems: 'center', gap: 6,
          }}
          title={collapsed ? 'Expand' : 'Collapse'}>
          <span style={{ color: 'var(--text3)' }}>{collapsed ? '▸' : '▾'}</span>
          Latency distribution
        </button>
        {!collapsed && clusters.length >= 2 && (
          <select value={picked}
            onChange={e => setPicked(e.target.value)}
            title="Same service runs across multiple clusters — pivot the heatmap to any single cluster, or stay on the union view."
            style={{ fontSize: 11, padding: '2px 6px', marginLeft: 4 }}>
            <option value="">All clusters ({clusters.length})</option>
            {clusters.map(c => <option key={c} value={c}>{c}</option>)}
          </select>
        )}
        {!collapsed && data && data.maxCount > 0 && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            peak {data.maxCount.toLocaleString()} spans/cell · log-scale y-axis
          </span>
        )}
      </div>
      {!collapsed && (
        <div style={{
          padding: 10, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
        }}>
          {data === undefined && <Spinner />}
          {data === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>
              Failed to load latency distribution.
            </div>
          )}
          {data && (data.maxCount === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              No spans in this window.
            </div>
          ) : (
            <LatencyHeatmap data={data} height={240} />
          ))}
        </div>
      )}
    </div>
  );
}

