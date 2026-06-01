import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider, keepPreviousData } from '@tanstack/react-query';
import { ReactQueryDevtools } from '@tanstack/react-query-devtools';
import App from './App';
import { initOtel } from './lib/otel';
import './styles/globals.css';

// Initialise the OpenTelemetry browser SDK before the React
// tree mounts so DocumentLoad spans capture the React boot
// itself + the first fetch round-trips. initOtel is a no-op
// when VITE_OTEL_DISABLE=1, so this is safe to call
// unconditionally.
initOtel();

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
