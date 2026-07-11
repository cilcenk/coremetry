import { Suspense, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { DrillButton } from '@/components/DrillButton';
import { Button } from '@/components/ui/Button';
import { recordServiceVisit, isServicePinned, toggleServicePin } from '@/lib/recentServices';
import { useUrlRange } from '@/lib/useUrlRange';
import { ServiceOverview } from './service/Overview';
import { ServiceTracesTab, ServiceLogsTab, ServiceTopologyTab } from './service/ServiceSignalTabs';
import { OperationsTable } from './service/OperationsTable';
import { ServiceClusterBreakdown } from './service/ServiceClusterBreakdown';
import { ServiceLatencyHeatmap } from './service/ServiceLatencyHeatmap';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceStructure } from '@/components/ServiceStructure';
import { ServiceCharts } from '@/components/ServiceCharts';
import { LazyMount } from '@/components/LazyMount';
import { ServiceCatalogPill } from '@/components/ServiceCatalogPill';
import { DBQueriesPanel } from '@/components/DBQueriesPanel';
import { DeployHistoryPanel } from '@/components/DeployHistoryPanel';
import { ServiceAttrsPanel } from '@/components/ServiceAttrsPanel';
import { DetailsPropsStrip } from './service/DetailsPropsStrip';
import { RpsByOperation } from './service/RpsByOperation';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ServiceRuntimeBadge } from '@/components/ServiceRuntimeBadge';
import { keys } from '@/lib/queries/keys';
import type { Service, Problem, OperationSummary, SLORow } from '@/lib/types';

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
  // v0.8.480 (perf dalga-3 #10) — range/servis değişiminde gövde
  // (TabStrip dahil) unmount edilmez: elde veri varken yalnız
  // solgunlaştırılır, Spinner ilk yüklemeye iner.
  const [refreshing, setRefreshing] = useState(false);
  const hadDataRef = useRef(false);
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

  // v0.8.415 (Tempo-parity T3) — operation scope lives in the URL
  // (?op=) so the RED charts AND the latency heatmap ride one
  // selection, and a copied link / refresh reproduces the exact
  // scoped view (house rule: URL is the source of truth). '' = all.
  const opScope = searchParams.get('op') ?? '';
  const setOpScope = (next: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (next) p.set('op', next); else p.delete('op');
    return p;
  }, { replace: true });
  // NO svc-change wipe of ?op= (v0.8.423). Every cross-service link in
  // the app builds a fresh /service?name=X href, so a forward
  // navigation sheds the scope naturally; the only path where svc
  // changes with ?op= present is the browser restoring a history entry
  // (back/forward) — where the scope is exactly what that entry
  // encoded. The v0.8.415 wipe effect fired ONLY there, silently
  // rewriting the restored URL (the recurring one-way-read bug class,
  // v0.8.253/256/265/267).

  useEffect(() => {
    if (!svc) return;
    if (hadDataRef.current) {
      setRefreshing(true);
    } else {
      setLoading(true);
    }
    const r = rangeNs;
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
    // v0.8.480 — soğuk yüklemede Overview'un RED batch'i gövde mount
    // olana kadar (bundle bitene kadar) başlayamıyordu: toplam süre
    // bundle+batch TOPLAMI oluyordu. Aynı RQ anahtarıyla önden ısıt —
    // Overview mount olduğunda useQuery cache'ten anında dolar; iki
    // istek artık paralel, chart paint max()'ı öder.
    queryClient.prefetchQuery({
      queryKey: ['service-overview-red', svc, r.from, r.to],
      queryFn: () => api.spanMetricBatch({
        from: r.from, to: r.to,
        dsl: `service.name = "${svc.replace(/"/g, '\\"')}"`,
        aggs: [
          { name: 'rate', agg: 'rate' },
          { name: 'error_rate', agg: 'error_rate' },
          { name: 'p99', agg: 'p99', field: 'duration_ms' },
          { name: 'p95', agg: 'p95', field: 'duration_ms' },
          { name: 'p50', agg: 'p50', field: 'duration_ms' },
        ],
      }),
      staleTime: 30_000,
    });
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
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
          setRefreshing(false);
          hadDataRef.current = true;
        }
      });
    return () => { cancelled = true; };
  }, [svc, rangeNs, queryClient]);

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
          <div style={{ opacity: refreshing ? 0.55 : 1, transition: 'opacity 120ms' }}
            aria-busy={refreshing}>
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
              <ServiceOverview service={svc} range={range} windowNs={rangeNs} info={info} operations={operations} />
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
                {/* v0.8.370 — Dynatrace-style reorganization (operator-
                    approved mockup): a properties strip on top, then three
                    question-grouped sections in a 2-col grid instead of the
                    old single-column stack. LazyMount discipline unchanged
                    (v0.5.302): eager = strip + RED charts; every grid cell
                    below mounts ~200px from the viewport. Profiling tile
                    dropped (operator: not in use); RPS-by-operation card
                    added — zero new fetches, it reuses the page's
                    operations summary. */}
                <DetailsPropsStrip service={svc} range={range} />
                <div className="dtl-sech">Performance</div>
                <ServiceCharts service={svc} range={range} windowNs={rangeNs}
                  opScope={opScope} onOpScopeChange={setOpScope}
                  onZoom={(fromUnixSec, toUnixSec) => {
                    setRange({
                      preset: 'custom',
                      fromMs: Math.round(fromUnixSec * 1000),
                      toMs: Math.round(toUnixSec * 1000),
                    });
                  }} />
                <div className="ov-grid dtl-cols ov-mb">
                  <LazyMount minHeight={360}>
                    <ServiceLatencyHeatmap service={svc} range={range}
                                           operation={opScope} />
                  </LazyMount>
                  <RpsByOperation operations={operations} range={range}
                    onOpenOperations={() => setTab('operations')} />
                </div>
                <div className="dtl-sech">Data &amp; structure</div>
                <div className="ov-grid dtl-cols ov-mb">
                  <LazyMount minHeight={300}>
                    <DBQueriesPanel service={svc}
                                    from={rangeNs.from}
                                    to={rangeNs.to}
                                    defaultOpen />
                  </LazyMount>
                  <LazyMount minHeight={300}>
                    <ServiceStructure service={svc} since={SINCE_MAP[range.preset] ?? '1h'} defaultOpen />
                  </LazyMount>
                </div>
                <div className="dtl-sech">Runtime &amp; rollouts</div>
                <div className="ov-grid dtl-cols ov-mb">
                  <LazyMount minHeight={140}>
                    <ServiceClusterBreakdown service={svc} range={range} />
                  </LazyMount>
                  <LazyMount minHeight={120}>
                    <ServiceAttrsPanel service={svc} range={range} />
                  </LazyMount>
                </div>
                {/* Recent rollouts — #deploys anchor preserved so the
                    /deploys "history →" link still scrolls here. */}
                <div id="deploys">
                  <LazyMount minHeight={160}>
                    <DeployHistoryPanel service={svc} />
                  </LazyMount>
                </div>
              </>
            )}
          </div>
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

export default function ServiceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ServiceDetailInner />
    </Suspense>
  );
}
