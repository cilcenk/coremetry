import { useEffect } from 'react';

// routePrefetch — intent-prefetch of code-split route chunks (v0.8.6 Phase 0).
//
// When the operator HOVERS an internal link, warm that route's lazy chunk
// during idle time so the click resolves instantly (no Suspense flash). The
// import specifiers below are the SAME ones App.tsx's lazy() uses, so Rollup
// emits one chunk per route and the prefetch warms exactly the chunk the route
// will load. Wired app-wide via a single delegated hover listener
// (usePrefetchOnHover) so NO nav/link markup changes — the canonical sidebar is
// left untouched.

// Hot routes only — the ones an operator clicks through constantly. Adding more
// is cheap; the goal is to warm the heavy surfaces, not every admin page.
const importers: Record<string, () => Promise<unknown>> = {
  '/services':   () => import('@/pages/Services'),
  '/service':    () => import('@/pages/Service'),
  '/traces':     () => import('@/pages/Traces'),
  '/trace':      () => import('@/pages/Trace'),
  '/metrics':    () => import('@/pages/Metrics'),
  '/logs':       () => import('@/pages/Logs'),
  '/topology':   () => import('@/pages/Topology'),
  '/service-map':() => import('@/pages/Topology'),
  '/endpoints':  () => import('@/pages/Endpoints'),
  '/databases':  () => import('@/pages/Databases'),
  '/messaging':  () => import('@/pages/Messaging'),
  '/dashboards': () => import('@/pages/Dashboards'),
  '/explore':    () => import('@/pages/Explore'),
  '/inbox':      () => import('@/pages/Inbox'),
  '/incidents':  () => import('@/pages/Incidents'),
  '/profiling':  () => import('@/pages/Profiling'),
};

const warmed = new Set<string>();

// prefetchRoute warms one route's chunk, once, during idle. Safe to spam — the
// `warmed` guard makes repeats a no-op, and a failed prefetch is forgotten so a
// later real navigation still retries.
export function prefetchRoute(path: string): void {
  const key = path.split('?')[0].split('#')[0];
  if (warmed.has(key)) return;
  const imp = importers[key];
  if (!imp) return;
  warmed.add(key);
  const run = () => { imp().catch(() => warmed.delete(key)); };
  const ric = (window as unknown as { requestIdleCallback?: (cb: () => void, o?: { timeout: number }) => void }).requestIdleCallback;
  if (ric) ric(run, { timeout: 1000 });
  else window.setTimeout(run, 0);
}

// usePrefetchOnHover installs ONE delegated mouseover/focus listener that warms
// the chunk for whatever internal <a href="/…"> the pointer is over. Mount once
// (App.tsx). Passive + idle-scheduled so it never competes with the current
// interaction.
export function usePrefetchOnHover(): void {
  useEffect(() => {
    const onOver = (e: Event) => {
      const a = (e.target as HTMLElement | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
      if (!a) return;
      const href = a.getAttribute('href');
      if (href && href.startsWith('/')) prefetchRoute(href);
    };
    document.addEventListener('mouseover', onOver, { passive: true });
    document.addEventListener('focusin', onOver, { passive: true });
    return () => {
      document.removeEventListener('mouseover', onOver);
      document.removeEventListener('focusin', onOver);
    };
  }, []);
}
