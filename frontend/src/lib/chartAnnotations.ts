import type { ChartAnnotation } from '@/lib/types';

// chartAnnotations (v0.8.284, A7) — pure windowing for the chart annotation
// overlay. Operator events (deploy/config/incident/maintenance/custom, dropped
// on charts via /api/operator-events) are fetched for a service over a window,
// then run through annotationsInWindow BEFORE they reach the uPlot draw hook.
//
// This layer does three things the draw hook shouldn't: (1) clamp to the fetch
// window [fromNs,toNs] inclusive, (2) collapse sub-pixel clusters of the SAME
// kind so a burst of deploys renders as one line (distinct kinds at the same
// instant survive — a deploy AND an incident is real signal), (3) cap the count
// so a busy service can't hand the draw loop hundreds of vlines. The draw hook
// then re-clamps to the LIVE x-scale (drag-zoom may be narrower than the fetch
// window). All timestamps are unix NANOSECONDS end-to-end.

export type RawEvent = { time: number; kind?: string; label?: string };

export interface AnnotationOpts {
  // Minimum ns gap between two same-kind markers before the later one is
  // dropped. Defaults to window/400 — sub-pixel on a typical chart width — so
  // the caller doesn't have to know the pixel budget.
  minGapNs?: number;
  // Hard ceiling on rendered markers; the MOST RECENT `max` survive (recent
  // change context is what an operator is usually chasing). Default 50.
  max?: number;
}

export function annotationsInWindow(
  events: RawEvent[] | null | undefined,
  fromNs: number,
  toNs: number,
  opts: AnnotationOpts = {},
): ChartAnnotation[] {
  if (!events || events.length === 0 || !(toNs > fromNs)) return [];

  const span = toNs - fromNs;
  const minGap = opts.minGapNs ?? span / 400;
  const max = opts.max ?? 50;

  const inWin = events
    .filter(e => e && Number.isFinite(e.time) && e.time >= fromNs && e.time <= toNs)
    .map<ChartAnnotation>(e => ({
      timeUnixNs: e.time,
      kind: e.kind || 'custom',
      label: e.label ?? '',
    }))
    .sort((a, b) => a.timeUnixNs - b.timeUnixNs);

  // Per-kind dedup: collapse markers of the same kind closer than minGap,
  // keeping the earliest of each cluster.
  const lastByKind: Record<string, number> = {};
  const deduped: ChartAnnotation[] = [];
  for (const a of inWin) {
    const prev = lastByKind[a.kind];
    if (prev !== undefined && a.timeUnixNs - prev < minGap) continue;
    lastByKind[a.kind] = a.timeUnixNs;
    deduped.push(a);
  }

  return deduped.length > max ? deduped.slice(deduped.length - max) : deduped;
}
