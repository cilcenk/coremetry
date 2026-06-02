import { useSyncExternalStore } from 'react';

// useThemeTick — returns a counter that increments whenever the <html>
// `data-theme` attribute flips (light↔dark). Charts that resolve CSS-var
// colors to concrete hex at draw time (uPlot canvas strokes) must rebuild
// on theme change; the theme is a bare DOM attribute (see ThemeToggle), not
// a React store, so components otherwise get no re-render signal and their
// canvas colors go stale until remount. Add the returned value to the
// chart effect's dependency array to re-resolve on toggle.
//
// One shared MutationObserver for the whole app (module-level), wired through
// useSyncExternalStore so React subscribes/unsubscribes correctly.

let tick = 0;
const listeners = new Set<() => void>();
let observer: MutationObserver | null = null;

function ensureObserver(): void {
  if (observer || typeof MutationObserver === 'undefined') return;
  observer = new MutationObserver(() => {
    tick++;
    listeners.forEach(l => l());
  });
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
}

function subscribe(cb: () => void): () => void {
  ensureObserver();
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

export function useThemeTick(): number {
  return useSyncExternalStore(subscribe, () => tick, () => tick);
}
