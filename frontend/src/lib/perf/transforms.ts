// transforms.ts — heavy, pure data transforms (v0.8.6 Phase 0).
//
// These are the CPU-bound operations that, on a 50k-row result set, would jank
// the main thread (drop scroll below 60fps, push interaction past the 100ms
// budget). They live here as PURE functions so they can run in two places with
// identical results:
//   - inside the transform Web Worker (transform.worker.ts) for big inputs, and
//   - on the main thread as a synchronous fallback (useTransformWorker) for
//     small inputs or environments without worker support.
// The single `runTransform` dispatcher keeps the two paths bit-identical.

import { downsampleXY, lttb, type Point } from './lttb';

export type AggKind = 'sum' | 'avg' | 'min' | 'max' | 'count' | 'p50' | 'p95' | 'p99';

// ── Quantiles ────────────────────────────────────────────────────────────────

// quantileSorted returns the q-th (0..1) quantile of an ascending-sorted array
// via linear interpolation between order statistics. O(1) given a sorted input.
export function quantileSorted(sorted: number[], q: number): number {
  const n = sorted.length;
  if (n === 0) return NaN;
  if (n === 1) return sorted[0];
  const pos = (n - 1) * Math.min(1, Math.max(0, q));
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  if (lo === hi) return sorted[lo];
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (pos - lo);
}

// percentiles computes several quantiles in one sort pass. `qs` are 0..1.
export function percentiles(values: number[], qs: number[]): number[] {
  const sorted = values.filter(Number.isFinite).slice().sort((a, b) => a - b);
  return qs.map(q => quantileSorted(sorted, q));
}

// ── Time-bucket aggregation ──────────────────────────────────────────────────

// aggregateBuckets folds parallel (times[], values[]) into fixed-width time
// buckets, applying `agg` per bucket. Quantile aggs collect per-bucket samples
// then quantile them; the rest fold incrementally. Returns sparse buckets
// (only buckets that received ≥1 sample) — matching the CH/ES histogram shape.
export function aggregateBuckets(
  times: number[],
  values: number[],
  bucketMs: number,
  agg: AggKind,
): { x: number[]; y: number[] } {
  if (bucketMs <= 0) return { x: times.slice(), y: values.slice() };
  const isQuantile = agg === 'p50' || agg === 'p95' || agg === 'p99';
  const acc = new Map<number, { sum: number; min: number; max: number; count: number; samples?: number[] }>();
  for (let i = 0; i < times.length; i++) {
    const v = values[i];
    if (!Number.isFinite(v)) continue;
    const b = Math.floor(times[i] / bucketMs) * bucketMs;
    let e = acc.get(b);
    if (!e) {
      e = { sum: 0, min: Infinity, max: -Infinity, count: 0, samples: isQuantile ? [] : undefined };
      acc.set(b, e);
    }
    e.sum += v;
    if (v < e.min) e.min = v;
    if (v > e.max) e.max = v;
    e.count++;
    if (e.samples) e.samples.push(v);
  }
  const keys = Array.from(acc.keys()).sort((a, b) => a - b);
  const x: number[] = [];
  const y: number[] = [];
  const q = agg === 'p50' ? 0.5 : agg === 'p95' ? 0.95 : 0.99;
  for (const k of keys) {
    const e = acc.get(k)!;
    let val: number;
    switch (agg) {
      case 'sum':   val = e.sum; break;
      case 'avg':   val = e.sum / e.count; break;
      case 'min':   val = e.min; break;
      case 'max':   val = e.max; break;
      case 'count': val = e.count; break;
      default:      val = quantileSorted(e.samples!.sort((a, b) => a - b), q);
    }
    x.push(k);
    y.push(val);
  }
  return { x, y };
}

// ── Flame / icicle tree ──────────────────────────────────────────────────────

export interface FlameSpan {
  spanId: string;
  parentId?: string | null;
  name: string;
  service?: string;
  durationMs: number;
}

export interface FlameNode {
  name: string;
  service?: string;
  value: number;        // total subtree duration (ms) — icicle width
  self: number;         // self time (value − sum(children.value)) clamped ≥0
  children: FlameNode[];
}

// buildFlameTree turns a flat span list (parent links) into the root-anchored
// icicle tree the FlameGraph component renders. Orphan spans (missing parent)
// attach to a synthetic root so nothing is dropped. O(n).
export function buildFlameTree(spans: FlameSpan[]): FlameNode {
  const nodes = new Map<string, FlameNode & { _parent?: string | null }>();
  for (const s of spans) {
    nodes.set(s.spanId, { name: s.name, service: s.service, value: s.durationMs, self: 0, children: [], _parent: s.parentId ?? null });
  }
  const root: FlameNode = { name: 'root', value: 0, self: 0, children: [] };
  for (const s of spans) {
    const node = nodes.get(s.spanId)!;
    const parent = s.parentId ? nodes.get(s.parentId) : undefined;
    (parent ?? root).children.push(node);
  }
  // self time = value − children total (clamped; async children can overlap).
  const computeSelf = (n: FlameNode): number => {
    let childSum = 0;
    for (const c of n.children) childSum += computeSelf(c);
    n.self = Math.max(0, n.value - childSum);
    return n.value;
  };
  for (const c of root.children) {
    root.value += computeSelf(c);
  }
  root.self = 0;
  return root;
}

// ── Dispatcher (shared by worker + main-thread fallback) ─────────────────────

export type TransformRequest =
  | { id: number; op: 'downsample'; xs: number[]; ys: (number | null)[]; threshold: number }
  | { id: number; op: 'lttb'; points: Point[]; threshold: number }
  | { id: number; op: 'percentiles'; values: number[]; qs: number[] }
  | { id: number; op: 'aggregate'; times: number[]; values: number[]; bucketMs: number; agg: AggKind }
  | { id: number; op: 'flame'; spans: FlameSpan[] };

export type TransformResult =
  | { xs: number[]; ys: (number | null)[] }   // downsample
  | Point[]                                     // lttb
  | number[]                                    // percentiles
  | { x: number[]; y: number[] }               // aggregate
  | FlameNode;                                  // flame

// runTransform executes one request synchronously. The worker calls this in its
// onmessage; the hook calls it directly on the main thread for small inputs.
export function runTransform(req: TransformRequest): TransformResult {
  switch (req.op) {
    case 'downsample': return downsampleXY(req.xs, req.ys, req.threshold);
    case 'lttb':       return lttb(req.points, req.threshold);
    case 'percentiles':return percentiles(req.values, req.qs);
    case 'aggregate':  return aggregateBuckets(req.times, req.values, req.bucketMs, req.agg);
    case 'flame':      return buildFlameTree(req.spans);
  }
}
