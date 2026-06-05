import { useEffect, useRef, useState } from 'react';

// usePerfStats — live frame-rate + long-task + Core-Web-Vitals sampling
// (v0.8.6 Phase 0). DEV-INSTRUMENT ONLY: gate the consumer on
// import.meta.env.DEV so the rAF loop + observers never run in production.
// FPS is flushed ~2×/s; LCP/CLS/INP come from buffered PerformanceObservers so
// they reflect the whole session, not just post-mount.

export interface PerfStats {
  fps: number;
  longTasks: number; // cumulative count since mount
  lcp?: number;      // ms — Largest Contentful Paint
  cls?: number;      // unitless — Cumulative Layout Shift
  inp?: number;      // ms — approx Interaction to Next Paint (max event duration)
}

export function usePerfStats(enabled = true): PerfStats {
  const [stats, setStats] = useState<PerfStats>({ fps: 0, longTasks: 0 });
  const frame = useRef({ count: 0, last: 0, raf: 0 });
  const acc = useRef({ longTasks: 0, lcp: undefined as number | undefined, cls: 0, inp: 0 });

  useEffect(() => {
    if (!enabled) return;
    let mounted = true;

    const tick = (t: number) => {
      const f = frame.current;
      if (f.last === 0) f.last = t;
      f.count++;
      const elapsed = t - f.last;
      if (elapsed >= 500) {
        const fps = Math.round((f.count * 1000) / elapsed);
        f.count = 0;
        f.last = t;
        if (mounted) {
          setStats({
            fps,
            longTasks: acc.current.longTasks,
            lcp: acc.current.lcp,
            cls: round(acc.current.cls, 3),
            inp: Math.round(acc.current.inp),
          });
        }
      }
      f.raf = requestAnimationFrame(tick);
    };
    frame.current.raf = requestAnimationFrame(tick);

    const observers: PerformanceObserver[] = [];
    const obs = (type: string, cb: (entries: PerformanceEntryList) => void) => {
      try {
        const o = new PerformanceObserver(list => cb(list.getEntries()));
        o.observe({ type, buffered: true } as PerformanceObserverInit);
        observers.push(o);
      } catch {
        /* entry type unsupported in this browser */
      }
    };
    obs('longtask', entries => { acc.current.longTasks += entries.length; });
    obs('largest-contentful-paint', entries => {
      const last = entries[entries.length - 1];
      if (last) acc.current.lcp = Math.round(last.startTime);
    });
    obs('layout-shift', entries => {
      for (const e of entries as unknown as Array<{ value: number; hadRecentInput: boolean }>) {
        if (!e.hadRecentInput) acc.current.cls += e.value;
      }
    });
    obs('event', entries => {
      for (const e of entries) if (e.duration > acc.current.inp) acc.current.inp = e.duration;
    });

    return () => {
      mounted = false;
      cancelAnimationFrame(frame.current.raf);
      observers.forEach(o => o.disconnect());
    };
  }, [enabled]);

  return stats;
}

function round(n: number, d: number): number {
  const f = 10 ** d;
  return Math.round(n * f) / f;
}
