// transform.worker.ts — the off-main-thread executor for heavy data transforms
// (v0.8.6 Phase 0). Vite bundles this as a module worker via the
// `new URL('./transform.worker.ts', import.meta.url)` reference in workerClient.
//
// It does nothing but route each request through the SAME pure runTransform the
// main-thread fallback uses, so results are identical regardless of where they
// ran. Keeping zero logic here is deliberate — the testable code lives in
// transforms.ts.

import { runTransform, type TransformRequest } from './transforms';

const ctx = self as unknown as Worker;

ctx.onmessage = (e: MessageEvent<TransformRequest>) => {
  const req = e.data;
  try {
    const result = runTransform(req);
    ctx.postMessage({ id: req.id, result });
  } catch (err) {
    ctx.postMessage({ id: req.id, error: err instanceof Error ? err.message : String(err) });
  }
};
