import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from './keys';
import type { TraceBundleResponse } from '@/lib/types';

// useTraceBundle — v0.10.672 (trace kiosk modu Dilim 2): GET
// /api/traces/{id}/bundle. Tek istekte span + log + Oracle; pencere sunucuda.
//
// staleTime = sunucu TTL (15 s, trace_bundle.go traceBundleTTL) — ES-maliyet
// disiplini: sekme odağı yeniden çekmez. Limitler anahtarda (keys.traces.
// bundle); "daha fazla" = logLimit 1000 ile YENİ anahtar, eski sayfa
// cache'te kalır. signal geçer (cancellation.test.ts kapısı): kiosk
// penceresi kapanınca ya da limit değişince eski istek CH/ES'te kesilir.
export function useTraceBundle(
  id: string | undefined,
  opts: { logLimit?: number; oracleLimit?: number } = {},
) {
  const logLimit = opts.logLimit ?? 0;
  const oracleLimit = opts.oracleLimit ?? 0;
  return useQuery<TraceBundleResponse>({
    queryKey: keys.traces.bundle(id ?? '', logLimit, oracleLimit),
    queryFn: ({ signal }) => api.traceBundle(id ?? '', opts, signal),
    enabled: !!id,
    staleTime: 15_000,
    refetchOnWindowFocus: false,
  });
}
