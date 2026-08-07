import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider, keepPreviousData } from '@tanstack/react-query';
import { ReactQueryDevtools } from '@tanstack/react-query-devtools';
import App from './App';
import { getRaw, STORAGE_KEYS } from './lib/storage';
import './styles/globals.css';
// uPlot temel CSS'i TEK yerden (v0.9.708): dört preset ayrı ayrı import
// edince rolldown css modülünün sahipliğini @grafana chunk'ına verdi ve
// preset'ler 198 KB gzip'lik lazy chunk'a statik kenar açtı. Eager tek
// sahip = index; grafana'nın aynı css importu eager modüle çözülür,
// ters kenar doğmaz.
import 'uplot/dist/uPlot.min.css';

// v0.9.753 (operatör: "752 sonrası Service Overview hataya düşüyor") —
// deploy sonrası BAYAT CHUNK sınıfı: açık sekmenin eski index.html'i,
// yeni deploy'un sildiği hash'li lazy chunk'ı ister → dynamic import
// 404 → error boundary ("Failed to fetch dynamically imported module").
// Vite bu durumda 'vite:preloadError' yayar; bir kez güvenli oto-yenile
// (taze index → taze hash'ler). 30 sn'lik sessionStorage korkuluğu
// gerçek bir ağ kesintisinde yenileme DÖNGÜSÜNÜ engeller — ikinci
// hata error boundary'ye düşer (Reload page affordance'ı orada).
window.addEventListener('vite:preloadError', (e) => {
  const KEY = 'cm.reload.preloadError';
  const last = Number(sessionStorage.getItem(KEY) || 0);
  if (Date.now() - last < 30_000) return;
  sessionStorage.setItem(KEY, String(Date.now()));
  (e as Event).preventDefault();
  window.location.reload();
});

// Defer AND gate the OpenTelemetry browser SDK off the critical path
// (v0.7.84 deferred; gating added to shrink the cold path further).
// The SDK + auto-instrumentations are ~26kB gzip (its own 'otel' chunk)
// and main.tsx is their ONLY importer (withSpan/getTracer have no other
// call sites). It's RUM of our OWN UI — not needed for first paint, and
// the operator's value is their services' traces, not perfect self-RUM
// on the cold-load fetch.
//
// KEY: the enablement decision is made HERE, before the dynamic
// import(), so when RUM is off the otel chunk is never even fetched or
// parsed. Previously the chunk loaded on idle every session and only
// then did initOtel() bail on the disable flag — paying the network +
// parse cost for nothing. rumEnabled() reads only runtime knobs (no
// otel import), so the gate is free.
//
// rumEnabled precedence (first match wins), default ON to preserve
// existing behaviour:
//   1. VITE_OTEL_DISABLE==='1'        → off (build-time hard kill switch)
//   2. window.__COREMETRY_RUM__       → host-page/server-injected boolean
//      (lets a deployment force RUM on/off without a rebuild)
//   3. localStorage 'coremetry-rum'   → 'off'/'on' operator opt-out toggle
//      (the runtime opt-out the perf baseline flagged as missing)
//   4. default                        → on
function rumEnabled(): boolean {
  if (import.meta.env.VITE_OTEL_DISABLE === '1') return false;
  const injected = (window as { __COREMETRY_RUM__?: boolean }).__COREMETRY_RUM__;
  if (typeof injected === 'boolean') return injected;
  // getRaw yields null in locked-down/private contexts — falls
  // through to the default rather than blocking RUM init.
  const ls = getRaw(STORAGE_KEYS.rum);
  if (ls === 'off' || ls === '0' || ls === 'false') return false;
  if (ls === 'on' || ls === '1' || ls === 'true') return true;
  return true;
}

// Firing on idle means it never competes with the initial render + first
// data fetch (TTFI budget <1.5s). Trade-off: the earliest fetch spans
// before idle may be missed — acceptable for self-observability.
// v0.9.238 — the exporter posts to same-origin /v1/traces, but in a
// distributed install the browser reaches the API Deployment, which never
// starts the ingest consumers: otlpRouteGuard answers 501 and the batch
// processor retries every 5s for the life of the tab. Operator-observed in
// prod as repeated failing `traces` requests eating connection slots on an
// already-saturated page.
//
// /api/health reports the pod's roles (v0.9.238), so ask before wiring
// anything — and keep the "never even fetch the otel chunk" property by
// probing BEFORE the dynamic import.
//
// Fails OPEN on purpose: an older server without the field, or a health
// request that doesn't land, leaves RUM enabled exactly as before. A
// telemetry feature must not switch itself off because one probe failed.
async function ingestReachable(): Promise<boolean> {
  try {
    const { api } = await import('./lib/api');
    const roles = (await api.health())?.roles;
    return typeof roles?.ingest === 'boolean' ? roles.ingest : true;
  } catch {
    return true;
  }
}

function bootOtel() {
  if (!rumEnabled()) return;
  void ingestReachable().then(ok => {
    if (!ok) {
      console.info('[coremetry] RUM off: this pod does not run the ingest role');
      return;
    }
    return import('./lib/browserOtel').then(m => m.initOtel());
  }).catch(() => {});
}
if ('requestIdleCallback' in window) {
  window.requestIdleCallback(bootOtel, { timeout: 3000 });
} else {
  setTimeout(bootOtel, 1500);
}

// Single shared QueryClient for the whole app. Defaults tuned
// for an internal observability dashboard:
//
//   staleTime 10s — most data is "live but a few seconds old is
//     fine"; an admin tabbing between /services and /anomalies
//     within 10s gets the same response without a refetch.
//
//   gcTime 5min — keep cached responses around so back/forward
//     navigation doesn't show a spinner. The 5-min window
//     matches the longest server-side cache (cardinality, system
//     stats), so a stale-then-fresh swap is the worst case.
//
//   refetchOnWindowFocus — true. Operators tab away to fix the
//     issue, then come back; auto-refresh on tab focus saves a
//     manual reload and is the SRE-correct behaviour.
//
//   retry — 1, with delay 800ms. Network blips on a corp VPN
//     are common; one quick retry hides them. More retries just
//     hide a real outage.
//
//   refetchOnReconnect — true so a network drop+resume restores
//     the screen state without intervention.
//
//   placeholderData keepPreviousData (v0.7.79) — on ANY key change
//     (range switch, filter edit, pagination Next/Back) keep the
//     last successful data on screen while the new query loads,
//     instead of dropping to a spinner. Kills the per-interaction
//     loading flicker that made the dashboard feel janky; pages
//     can read `isPlaceholderData`/`isFetching` if they want a
//     subtle "updating…" hint. The biggest perceived-speed win
//     for the marginal cost of one import.
//
// Per-query overrides (refetchInterval for live polling, longer
// staleTime for slow-moving data, etc.) live next to each
// useXyz() hook in lib/queries/*.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      gcTime: 5 * 60_000,
      // refetchOnWindowFocus stays true: it's staleTime-gated (a
      // <10s tab-away never refetches) and bounded to the mounted
      // page's queries, so it's the SRE-correct "tab back → fresh"
      // behaviour, not a 48-page storm. The real perceived-speed
      // win is keepPreviousData below, not killing focus-refetch.
      refetchOnWindowFocus: true,
      refetchOnReconnect: true,
      placeholderData: keepPreviousData,
      retry: 1,
      retryDelay: 800,
    },
  },
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
      {/* Devtools mount point. Hidden in production builds via
          import.meta.env.PROD; in dev a small floating button
          opens the cache inspector. No bundle weight in prod
          since the package is dev-only. */}
      {!import.meta.env.PROD && (
        <ReactQueryDevtools initialIsOpen={false} buttonPosition="bottom-right" />
      )}
    </QueryClientProvider>
  </StrictMode>
);
