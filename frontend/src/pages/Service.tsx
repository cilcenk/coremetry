import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { TabStrip as UiTabStrip } from '@/components/ui/TabStrip'; // v0.10.456 (D5) — sayfanın yerel TabStrip sarmalayıcısıyla ad çakışmasın
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { navHref } from '@/lib/navHref';
import { encodeRange } from '@/lib/urlState';
import { Topbar } from '@/components/Topbar';
import { DrillButton } from '@/components/DrillButton';
import { Button } from '@/components/ui/Button';
import { recordServiceVisit, isServicePinned, toggleServicePin } from '@/lib/recentServices';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { envDSL } from '@/lib/entrySpans';
import { ServiceOverview } from './service/Overview';
import { ServiceLogsTab, ServiceTopologyTab } from './service/ServiceSignalTabs';
import { ServiceInfraTab } from './service/ServiceInfraTab';
import { ServicePodsTab } from './service/ServicePodsTab';
import { OperationsTable } from './service/OperationsTable';
import { ServiceClusterBreakdown } from './service/ServiceClusterBreakdown';
import { ServiceLatencyHeatmap } from './service/ServiceLatencyHeatmap';
import { Spinner, Empty } from '@/components/Spinner';
import { ServiceCharts } from '@/components/ServiceCharts';
import { LazyMount } from '@/components/LazyMount';
import { ServiceCatalogPill } from '@/components/ServiceCatalogPill';
import { DBQueriesPanel } from '@/components/DBQueriesPanel';
import { DeployHistoryPanel } from '@/components/DeployHistoryPanel';
import { DetailsPropsStrip } from './service/DetailsPropsStrip';
import { DetailsToc } from './service/DetailsToc';
import { DetailsMetricsSection, useDetailsMetricPanels } from './service/DetailsMetricsSection';
import { panelMaxDataPoints } from '@/lib/chartStep';
import { ServiceAnnotationLane } from '@/components/charts/ServiceAnnotationLane';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtAgoNs, tsLong } from '@/lib/utils';
import { IconSparkles } from '@/components/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ServiceRuntimeBadge } from '@/components/ServiceRuntimeBadge';
import { keys } from '@/lib/queries/keys';
import type { Service, Problem, OperationSummary, SLORow, TimeRange } from '@/lib/types';
import { stripMarkdown } from '@/components/Markdown';
import { QueryError } from '@/components/QueryError';
import { PageShell } from '@/components/ui/PageShell';

// v0.9.257 — SINCE_MAP deleted: it had no remaining reader here, and the
// dormant copy is how the divergence spread (pages/service/Overview.tsx
// inherited a mutated version that emitted Go-unparseable '7d'). The live
// callers use rangeToSince() in lib/utils.ts.

// v0.9.212 — 'traces' retired. The tab was a 25-row "slowest traces" table
// plus an "Open in Traces →" link, while the header's ⋮ Traces drill chip
// already lands on the same service-scoped /traces page (where the column is
// sortable). A stale ?tab=traces link redirects there rather than 404ing to
// the default tab.
type ServiceTab = 'overview' | 'operations' | 'details' | 'logs' | 'topology' | 'infra' | 'pods';

function ServiceDetailInner() {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  // Canonical key is `name` (every in-app link uses /service?name=…); also accept
  // `service` so a hand-typed /service?service=X resolves instead of showing
  // "Missing service name" (operator-reported, v0.8.219).
  const svc = searchParams.get('name') ?? searchParams.get('service') ?? '';
  // Runtime fingerprint (e.g. "Java OpenJDK 21") for the service-header badge.
  const runtimeQ = useQuery({ queryKey: ['svc-runtime', svc], queryFn: () => api.serviceRuntime(svc), enabled: !!svc, staleTime: 300_000 });

  const queryClient = useQueryClient();
  // Global time window (UX#2) — URL-persisted + carried across pages.
  // v0.9.429 — zoom-yığını deseni (bu dosyanın v0.9.199 referans
  // implementasyonu) paylaşılan usePageZoomRange hook'una taşındı;
  // davranış sözleşmesi hook başlığında, birebir aynı.
  const { range, setRange, handleZoom, handleZoomReset } = usePageZoomRange('30m');
  // v0.9.1041 (env(a)) — env is now APPLIED, not just forwarded to the
  // Endpoints drill: it narrows the bundle (KPI + operations), the Overview
  // span+metric RED (tiles + charts), ServiceCharts and the latency heatmap
  // to one deploy_env, so <Topbar envApplies/> is honest. The Endpoints
  // drill still forwards it too (the v0.9.306/307 "a pivot must ask the
  // same question" lesson; DrillButton builds a fresh URL).
  const [env] = useUrlEnv();
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
  const [endpoints, setEndpoints] = useState<import('@/lib/types').EndpointRow[]>([]);
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
  // v0.9.858 (UX denetimi K6) — bundle hatası. Öncesi: catch info'yu null'a
  // çekiyor, null'ın render dalı olmadığı için sayfa SESSİZCE soyuluyordu
  // (operatör "bu servisin telemetrisi yok" sanıyor).
  const [bundleErr, setBundleErr] = useState<string | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
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
    : tabParam === 'logs' ? 'logs'
    : tabParam === 'topology' ? 'topology'
    : tabParam === 'infra' ? 'infra'
    : (tabParam === 'pods' || tabParam === 'metrics') ? 'pods'
    : 'overview';
  // v0.9.212 — a bookmarked ?tab=traces would otherwise land silently on
  // Overview, which reads as "my link broke". Send it where the tab used to
  // go instead: the service-scoped /traces page, window carried.
  useEffect(() => {
    if (tabParam !== 'traces' || !svc) return;
    navigate(
      `/traces?service=${encodeURIComponent(svc)}&range=${encodeRange(range)}`,
      { replace: true },
    );
  }, [tabParam, svc, range, navigate]);

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
    // v0.9.533 — kaldırılan bağımsız JMX bölümünün paramları (?jcluster
    // /?jds) sekme değişiminde temizlenir: kimse okumuyor, eski
    // paylaşılan linklerden URL'de atıl kalıyorlardı. jpod KALIR —
    // yeniden amaçlandı (pod satırını otomatik açan derin link).
    p.delete('jcluster');
    p.delete('jds');
    return p;
  }, { replace: true });

  // v0.9.784 — Details'in "Metrikler" bölümü KOŞULLU: servisin metrik
  // kataloğunda eşleşen aile yoksa hiç kurulmaz. ToC girdisi ve ?op=
  // kapsam beyanı AYNI sinyali okumak zorunda — olmayan bir bölüme ToC
  // satırı ya da kapsam iddiası koymak ölü UI olur. Bölümün kendi
  // sorgusuyla AYNI queryKey + staleTime ⇒ RQ dedupe, ek ağ isteği YOK;
  // yalnız Details sekmesinde etkin.
  const { panels: metricPanels } = useDetailsMetricPanels(svc, tab === 'details');
  const hasMetricPanels = metricPanels.length > 0;

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
    // v0.9.391 — mdp Overview'un useQuery'siyle AYNI formülden (parite
    // şart: farklı key = prefetch boşa gider). select yok — cache HAM
    // zarfı taşır, Overview'un select'i okurken soyar.
    const redMdp = panelMaxDataPoints(3);
    // v0.9.723 — rateWindow Overview'un key/istek formülüne girdi; parite
    // BURADA da şart, yoksa prefetch ölü kalır (review bulgusu: farklı
    // uzunlukta key asla eşleşmez, istek boşa gider + soğuk yükleme eski
    // yavaşlığına döner).
    // v0.9.844 — key'in motor-bayrağı elemanı ÇIKTI ve rateWindow=180
    // SABİTLENDİ (eski motor söküldü, tek mod kaldı). Overview'un
    // useQuery'si AYNI commit'te aynı şekli aldı — bayt paritesi ancak
    // ikisi birlikte değişince korunur.
    queryClient.prefetchQuery({
      // v0.9.1041 — env joins the key + DSL in lockstep with Overview's
      // seriesQ (parity: a differently-shaped key means the prefetch never
      // hits and cold load regresses).
      queryKey: ['service-overview-red', svc, r.from, r.to, redMdp, env],
      queryFn: () => api.spanMetricBatch({
        from: r.from, to: r.to, maxDataPoints: redMdp,
        rateWindow: 180,
        dsl: `service.name = "${svc.replace(/"/g, '\\"')}"` + envDSL(env),
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
      setBundleErr(null);
      setInfo(b?.service ?? null);
      setProblems(b?.problems ?? []);
      setOperations(b?.operations ?? []);
      setEndpoints(b?.endpoints ?? []);
      if (b?.deploys) {
        queryClient.setQueryData(
          keys.deploys.forService(svc, r.from ?? 0, r.to ?? 0),
          b.deploys,
        );
      }
    };
    api.serviceBundle(svc, r, { env: env || undefined })
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
          const refreshed = await api.serviceBundle(svc, r, { refresh: true, env: env || undefined })
            .catch(() => null);
          if (refreshed) applyBundle(refreshed);
        }
      })
      .catch(e => {
        if (cancelled) return;
        // v0.9.858 (UX denetimi K6) — info=null'ın render dalı yoktu:
        // sayfa sessizce SOYULUYOR, operatör servisin telemetrisiz
        // olduğunu sanıyordu. Hata metni saklanıp gösteriliyor.
        setInfo(null); setProblems([]); setOperations([]); setEndpoints([]);
        setBundleErr(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
          setRefreshing(false);
          hadDataRef.current = true;
        }
      });
    return () => { cancelled = true; };
  }, [svc, rangeNs, queryClient, retryNonce, env]);

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
    // v0.9.1041 — env joins key + call so the normalized table narrows with
    // the raw one (bundle ops already carry env via serviceBundle above).
    queryKey: keys.services.operations(svc, { from: rangeNs.from ?? 0, to: rangeNs.to ?? 0 }, true, false, env),
    queryFn: () => api.serviceOperations(svc, { from: rangeNs.from ?? 0, to: rangeNs.to ?? 0 }, true, false, env),
    enabled: !!svc && normalized,
    staleTime: 60_000,
  });
  // v0.9.67 — v0.9.61'in Elastic-parity tablosu OPERATÖR KARARIYLA
  // geri alındı ("eski hali daha iyiydi"): compare fetch kablolaması
  // da söküldü. Backend yetenekleri (?compare=prior + latency
  // serileri, v0.9.60/64) uyumlu-sessiz durur — UI tekrar istenirse
  // fetch tarafı hazır.
  // The table's data source flips with the toggle: bundle ops when raw,
  // the op_group query when normalized. Everything downstream (row
  // renderer, useDataTable sort, sparkline) is unchanged — only `rows`.
  const displayedOps = normalized ? (normOpsQ.data ?? []) : operations;
  const opsLoading = normalized && normOpsQ.isLoading;

  if (!svc) {
    return (
      <>
        <Topbar title="Service" range={range} onRangeChange={setRange} envApplies />
        <PageShell><Empty icon="⚠" title="Missing service name" /></PageShell>
      </>
    );
  }

  const openProbs = problems.filter(p => p.status === 'open');

  return (
    <>
      <Topbar title={`Service · ${svc}`} range={range} onRangeChange={setRange} envApplies />
      <PageShell>
        {/* Service identity header (design handoff app.jsx .svc-head): big
            status dot + bare service name + runtime badge + health pill. */}
        {/* v0.9.211 — identity row absorbed the catalog pill (was its own
            block below) and dropped the HEALTHY/WARNING/CRITICAL word: the
            status dot already carries that, on the SAME threshold. The KPI
            chips that used to sit in the toolbar are gone too — Overview's
            tiles are the one place a service's RED numbers live, with a
            delta and a single latency definition. Those chips disagreed
            with this dot (chip warned at >0, dot/badge at >1: a 0.5%-error
            service rendered a green dot next to an orange "Errors 0.50%")
            and their P99 included kafka spans while the Overview tile 200px
            below excluded them. */}
        {info && (
          <div className="svc-head">
            <div className="svc-title">
              <span className={`ov-dot ${info.errorRate > 5 ? 'red' : info.errorRate > 1 ? 'amber' : 'green'}`} style={{ width: 12, height: 12 }} />
              <h1>{svc}</h1>
              {runtimeQ.data && <ServiceRuntimeBadge rt={runtimeQ.data} compact />}
              <ServiceCatalogPill service={svc} />
            </div>
          </div>
        )}
        <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 14, flexWrap: 'wrap' }}>
          {/* v0.9.1320 — geri linki pencereyi + env'i taşır (navHref);
              çıplak `/services` operatörü sticky pencereye düşürüyordu. */}
          <Link to={navHref('/services', searchParams.toString())} className="sec" style={{
            padding: '5px 12px', border: '1px solid var(--border)',
            borderRadius: 6, fontSize: 12, color: 'var(--text)', textDecoration: 'none',
          }}>← All services</Link>
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
            {/* v0.9.309 (brief N6d) — /endpoints had NO contextual way
                in: the page was reachable only from the sidebar and the
                command palette, so an incident investigation never
                descended to the per-route table. This is the drill an
                operator wants right after "which service" — "which
                ROUTE of it". env rides along for the same reason it
                rides every other pivot since v0.9.307: a link must ask
                the question the screen it left was asking. */}
            <DrillButton to="/endpoints" params={{ service: svc, env: env || undefined }} range={range}
              title="Per-route RED for this service — calls, errors, P50/P90/P95/P99 and spread per endpoint"
              label="⇄ Endpoints" />
            <DrillButton to="/problems" params={{ service: svc }}
              title="Open problems for this service"
              label="⚠ Problems" />
            <DrillButton to="/anomalies" params={{ service: svc }}
              title="Anomaly events for this service"
              label="∿ Anomalies" />
          </div>
        </div>
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
                    {/* v0.9.530 — ProblemExplainer'ın proaktif kök-sebep
                        cümlesi. Alan bundle'da ZATEN geliyordu (Problem[]),
                        yalnız çizilmiyordu; ek istek yok.
                        description ile AYNI tonda basılamaz: bu bir LLM
                        çıkarımı, o bir kural metni. Köken işareti + yaş,
                        Inbox satırıyla aynı muamele. İki satıra çivili —
                        özet çok bölümlü bir blok ve kart 50 probleme kadar
                        çizilebiliyor (bundle Limit=50), sınırsız bırakmak
                        sekmeleri sayfanın çok altına iterdi. */}
                    {p.aiSummary && (
                      <div
                        // v0.9.696 — KIRPILMIŞ yüzey (2 satır) + title
                        // özniteliği: markdown düzleştiriliyor, Inbox
                        // kartıyla aynı gerekçe.
                        title={p.aiSummaryAt
                          ? `${stripMarkdown(p.aiSummary)}\n\nAI çıkarımı · ${fmtAgoNs(p.aiSummaryAt)}`
                          : stripMarkdown(p.aiSummary)}
                        style={{
                          fontSize: 11, color: 'var(--text2)', marginTop: 4,
                          padding: '4px 8px', borderRadius: 'var(--radius-sm)',
                          background: 'var(--accent-soft)',
                          borderLeft: '2px solid var(--accent)',
                          display: '-webkit-box', WebkitLineClamp: 2,
                          WebkitBoxOrient: 'vertical', overflow: 'hidden',
                        }}
                      >
                        <IconSparkles size={10} /> {stripMarkdown(p.aiSummary)}
                        {p.aiSummaryAt && (
                          <span style={{ color: 'var(--text3)' }}> · {fmtAgoNs(p.aiSummaryAt)}</span>
                        )}
                      </div>
                    )}
                    <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 4, fontFamily: 'monospace' }}>
                      since {tsLong(p.startedAt)}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {loading && <Spinner />}
        {!loading && bundleErr && (
          <QueryError message={bundleErr} onRetry={() => setRetryNonce(n => n + 1)}>
            This service's data could not be loaded. The sections below are
            missing because the read failed — not because the service is idle.
          </QueryError>
        )}
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
              <ServiceOverview service={svc} range={range} windowNs={rangeNs} info={info} operations={operations}
                endpoints={endpoints} onZoom={handleZoom} onZoomReset={handleZoomReset} env={env} />
            )}
            {tab === 'logs' && <ServiceLogsTab service={svc} range={range} windowNs={rangeNs}
              onZoom={handleZoom} onZoomReset={handleZoomReset} />}
            {tab === 'topology' && <ServiceTopologyTab service={svc} range={range} />}
            {tab === 'infra' && <ServiceInfraTab service={svc} range={range}
              onZoom={handleZoom} onZoomReset={handleZoomReset} />}
            {/* v0.9.158 — "Pods" sekmesi (eski Metrics): cluster açılır pod
                grupları + JVM/JBoss JMX panelleri + OTel runtime çizelgeleri. */}
            {tab === 'pods' && <ServicePodsTab service={svc} range={range}
              onZoom={handleZoom} onZoomReset={handleZoomReset} />}
            {/* v0.9.63 — v0.9.62'nin sekme-tepesi RED üçlüsü OPERATÖR
                KARARIYLA geri alındı ("gereksiz olmuş"): grafikler
                Details'te (Performance) yaşar, Operations sekmesi
                tabloya odaklı kalır. Yeniden ekleme. */}
            {tab === 'operations' && (
              <OperationsTable service={svc} rows={displayedOps} range={range}
                preset={range.preset}
                onWiden={() => setRange({ preset: '1h' })}
                normalized={normalized}
                onToggleNormalized={setNormalized}
                onZoom={handleZoom} onZoomReset={handleZoomReset}
                loading={opsLoading} />
            )}
            {tab === 'details' && (
              <div style={{ display: 'flex', gap: 18, alignItems: 'flex-start' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                {/* v0.9.380 (redesign D4, mockup af7419e5) — dtl-cols (1fr 1fr)
                    grid'i ÖLDÜ: v0.9.141/348 kaldırmalarından beri üç bölümde
                    de sağ sütun boştu (sayfanın yarısı ölü piksel). Heatmap/
                    DB/Runtime tam genişlikte; sağda scroll-spy ToC rayı
                    (DetailsToc, ≥1100px). LazyMount disiplini değişmedi.
                    ?op= kapsam şeridi hangi bölümün daraldığını İŞARETLER —
                    v0.9.358-374 dürüstlük çizgisinin düzen hali. */}
                <div id="dtl-props"><DetailsPropsStrip service={svc} range={range} /></div>
                {opScope && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '4px 0 8px', fontSize: 12 }}>
                    <span className="badge b-info mono" style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                      op: {opScope}
                      <span onClick={() => setOpScope('')} style={{ cursor: 'pointer' }} title="Operasyon kapsamını kaldır">✕</span>
                    </span>
                    {/* v0.9.784 — beyan sayfa SIRASINI izler ve KOŞULLU
                        bölümü ancak kuruluysa anar. Metrikler yalnız
                        service.name ile filtreleniyor (metric_points'te
                        operasyon boyutu yok) → kapsam DIŞI. */}
                    <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                      kapsam: <span style={{ opacity: .65 }}>Clusters ✗ · Database ✗ · </span>
                      Performance ✓ ·{' '}
                      {hasMetricPanels && <span style={{ opacity: .65 }}>Metrikler ✗ · </span>}
                      Latency ✓ · <span style={{ opacity: .65 }}>Runtime ✗</span>
                    </span>
                  </div>
                )}
                {/* v0.10.151 (operatör): Clusters ve Database EN ÜSTTE —
                    çok-cluster'lı prod'da ilk soru "hangi cluster'da ne
                    oluyor?" ve "hangi sorgu?"; Performance/Latency aşağı indi.
                    Per-cluster breakdown Runtime & rollouts'tan buraya taşındı
                    (kendi çapası dtl-clusters). */}
                <div className="dtl-sech" id="dtl-clusters">Clusters
                  {opScope && <span className="badge b-gray" style={{ textTransform: 'none', letterSpacing: 0 }}>tüm servis</span>}
                </div>
                <div className="ov-mb">
                  <LazyMount minHeight={140}>
                    <ServiceClusterBreakdown service={svc} range={range} />
                  </LazyMount>
                </div>
                {/* v0.9.141 (operatör) — Structure paneli kaldırıldı; bölüm
                    yalnız DB sorgularına indi, başlık "Database" oldu. */}
                <div className="dtl-sech" id="dtl-db">Database
                  {opScope && <span className="badge b-gray" style={{ textTransform: 'none', letterSpacing: 0 }}>tüm servis</span>}
                </div>
                <div className="ov-mb">
                  <LazyMount minHeight={300}>
                    <DBQueriesPanel service={svc}
                                    from={rangeNs.from}
                                    to={rangeNs.to}
                                    defaultOpen />
                  </LazyMount>
                </div>
                <div className="dtl-sech" id="dtl-perf">Performance
                  {opScope && <span className="badge b-info" style={{ textTransform: 'none', letterSpacing: 0 }}>op kapsamı</span>}
                </div>
                {/* v0.9.348 — rootOnly: bu paneller servisin KENDİ giriş
                    noktalarını çiziyor artık, dışarı yaptığı çağrıları değil.
                    Filtresiz hâlde api-gateway'in grafiği
                    account-service/ListAccounts gibi GİDEN çağrıları kendi
                    yüküymüş gibi gösteriyordu (ölçüm: 22 seri, yalnız 14'ü
                    bu servisin ucu). Overview'a AÇILMADI — orada operatörün
                    kendi "giriş / tüm span'ler" ayrımı var. */}
                <ServiceCharts service={svc} range={range} windowNs={rangeNs}
                  opScope={opScope} onOpScopeChange={setOpScope}
                  problems={problems} rootOnly env={env}
                  onZoom={handleZoom} onZoomReset={handleZoomReset} />
                {/* v0.9.395 (Faz C-2 Ş2, mockup 52b05851 onaylı) — annotation
                    şeridi PİLOT: üç RED paneli aynı x-eksenini paylaştığı
                    için yığının altında TEK şerit hepsine hizmet eder.
                    Sayfa başına tek fetch; tık = ±15dk zoom (yığın+çift-tık
                    geri aynı yol). Problem x-bölgeleri chart İÇİNDE kalır. */}
                <ServiceAnnotationLane service={svc} fromNs={rangeNs.from} toNs={rangeNs.to}
                  onZoomTo={handleZoom} />
                {/* v0.9.784 — OTLP metrik satırı. Details'te bugüne dek
                    metric_points türevli TEK panel yoktu; panel seti
                    servisin KENDİ kataloğundan kurulur (ad hardcode YOK),
                    ailesi olmayan panel hiç kurulmaz, hiç panel yoksa
                    bölüm de ToC girdisi de gizli. */}
                <DetailsMetricsSection service={svc} rangeNs={rangeNs}
                  onZoom={handleZoom} onZoomReset={handleZoomReset} />
                <div className="dtl-sech" id="dtl-latency">Latency
                  {opScope && <span className="badge b-info" style={{ textTransform: 'none', letterSpacing: 0 }}>op kapsamı</span>}
                </div>
                <div className="ov-mb">
                  <LazyMount minHeight={360}>
                    <ServiceLatencyHeatmap service={svc} range={range}
                                           operation={opScope} rootOnly env={env} />
                  </LazyMount>
                </div>
                <div className="dtl-sech" id="dtl-runtime">Runtime &amp; rollouts
                  {opScope && <span className="badge b-gray" style={{ textTransform: 'none', letterSpacing: 0 }}>tüm servis</span>}
                </div>
                {/* Recent rollouts — #deploys anchor preserved so the
                    /deploys "history →" link still scrolls here. */}
                <div id="deploys">
                  <LazyMount minHeight={160}>
                    <DeployHistoryPanel service={svc}
                      onZoomWindow={(tNs) => handleZoom(tNs / 1e9 - 1800, tNs / 1e9 + 1800)} />
                  </LazyMount>
                </div>
              </div>
              <DetailsToc showMetrics={hasMetricPanels} />
              </div>
            )}

            {/* v0.6.51 — SLO health strip. Unifies SLO status into the
                service detail page (was /slos-only). One chip per SLO:
                target, current SLI, budget bar, burn-rate badge. Click
                jumps to /slos. Hidden when the service has no SLOs.
                v0.9.282 (operatör) — strip sekme şeridinin ÜSTÜNDEYDİ,
                her sekmede ilk gövde öğesini aşağı itiyordu ("gereksiz
                yer kaplıyor"). Artık içeriğin en altında: SLO'lar hâlâ
                her sekmede görünür, ama triage tablosu tepeyi alır. */}
            {slos.length > 0 && (
              <div style={{
                border: '1px solid var(--border)', background: 'var(--bg1)',
                borderRadius: 6, padding: 12, marginTop: 14,
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
          </div>
        )}
      </PageShell>
    </>
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
    { key: 'infra',      label: 'Infrastructure' },
    { key: 'pods',       label: 'Pods' },
    { key: 'topology',   label: 'Topology' },
    { key: 'logs',       label: 'Logs' },
  ];
  // The house `.tab-strip` set — same active colour, underline weight and
  // padding as Settings, Anomalies, Endpoints and the eight other strips
  // (v0.9.900, BB7). `svc-tabs` adds only the sticky behaviour: the strip
  // stays pinned to the top of the #main scroll viewport while the body
  // scrolls under it, with the page bg masking the content behind it.
  return (
    <UiTabStrip ariaLabel="Servis sekmeleri" className="svc-tabs" value={tab} onChange={onChange}
      tabs={items.map(it => ({ key: it.key, label: <>{it.label}{it.hint && <span className="tab-count">{it.hint}</span>}</> }))} />
  );
}

export default function ServiceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ServiceDetailInner />
    </Suspense>
  );
}

