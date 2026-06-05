// workerClient.ts — singleton transform-worker manager (v0.8.6 Phase 0).
//
// One shared module Worker for the whole app (transforms are stateless, so a
// single worker with an id-correlated request queue beats one-worker-per-call).
// Small inputs run INLINE on the main thread — below ~2k items the structured-
// clone + postMessage round-trip costs more than the transform itself. If the
// environment has no Worker (tests/SSR) or the worker errors, every call falls
// back to the synchronous path transparently.

import { runTransform, type TransformRequest, type TransformResult } from './transforms';

let worker: Worker | null = null;
let workerTried = false;
let nextId = 1;
const pending = new Map<number, { resolve: (r: TransformResult) => void; reject: (e: Error) => void }>();

function getWorker(): Worker | null {
  if (worker) return worker;
  if (workerTried) return null; // don't retry a failed construction every call
  workerTried = true;
  if (typeof Worker === 'undefined') return null;
  try {
    const w = new Worker(new URL('./transform.worker.ts', import.meta.url), { type: 'module' });
    w.onmessage = (e: MessageEvent<{ id: number; result?: TransformResult; error?: string }>) => {
      const { id, result, error } = e.data;
      const p = pending.get(id);
      if (!p) return;
      pending.delete(id);
      if (error) p.reject(new Error(error));
      else p.resolve(result as TransformResult);
    };
    w.onerror = () => {
      // Drain pending to the main-thread path; stop using the worker.
      worker = null;
      for (const [, p] of pending) p.reject(new Error('transform worker crashed'));
      pending.clear();
    };
    worker = w;
  } catch {
    worker = null;
  }
  return worker;
}

// Inputs at/above this length go to the worker; smaller ones run inline.
const INLINE_THRESHOLD = 2000;

function inputSize(req: TransformRequest): number {
  switch (req.op) {
    case 'downsample': return req.xs.length;
    case 'lttb':       return req.points.length;
    case 'percentiles':return req.values.length;
    case 'aggregate':  return req.times.length;
    case 'flame':      return req.spans.length;
  }
}

// transform runs `req` off-thread when it's big enough to matter, else inline.
// Always returns a Promise so callers don't branch on where it ran.
export function transform(req: Omit<TransformRequest, 'id'>): Promise<TransformResult> {
  const full = { ...req, id: nextId++ } as TransformRequest;
  const w = inputSize(full) >= INLINE_THRESHOLD ? getWorker() : null;
  if (!w) {
    try {
      return Promise.resolve(runTransform(full));
    } catch (e) {
      return Promise.reject(e instanceof Error ? e : new Error(String(e)));
    }
  }
  return new Promise((resolve, reject) => {
    pending.set(full.id, { resolve, reject });
    w.postMessage(full);
  });
}
