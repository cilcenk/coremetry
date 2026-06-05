import { useEffect, useRef, useState } from 'react';
import { transform } from './workerClient';
import type { TransformRequest, TransformResult } from './transforms';

// useTransform — run a heavy transform off the main thread and track its result
// as React state (v0.8.6 Phase 0). Large inputs go to the shared worker; small
// ones run inline (see workerClient). Stale results are dropped last-write-wins
// so rapid input changes (zoom / filter / live append) never flash an old
// frame. `build` returns the request (or null to no-op while data is missing);
// pass a `deps` array exactly like useEffect — the transform reruns when it
// changes.
export function useTransform<R extends TransformResult = TransformResult>(
  build: () => Omit<TransformRequest, 'id'> | null,
  deps: unknown[],
): { data: R | undefined; loading: boolean; error: string | undefined } {
  const [state, setState] = useState<{ data?: R; loading: boolean; error?: string }>({ loading: false });
  const seq = useRef(0);

  useEffect(() => {
    const req = build();
    if (!req) {
      setState({ loading: false });
      return;
    }
    const mySeq = ++seq.current;
    setState(s => ({ data: s.data, loading: true })); // keep prior data visible while recomputing
    transform(req).then(
      r => { if (mySeq === seq.current) setState({ data: r as R, loading: false }); },
      e => { if (mySeq === seq.current) setState({ loading: false, error: e instanceof Error ? e.message : String(e) }); },
    );
    // No in-worker cancellation; the seq guard makes late results no-ops.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return { data: state.data, loading: state.loading, error: state.error };
}
