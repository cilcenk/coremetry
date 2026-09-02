// invalidationCoalescer.ts — v0.10.260 (perf profili §7 madde 4, F4 ⭐):
// SSE olayları tek tek invalidateQueries çağırıyordu; bir problem
// fırtınasında (N olay/sn) her olay aynı 'problems'/'inbox' ağaçlarını
// yeniden yükletiyordu. Bu birleştirici anahtarları 250 ms pencerede
// toplar, JSON imzasıyla tekilleştirir ve BİR kez invalidate eder.
// SAF çekirdek (createCoalescer) testli; zamanlayıcı enjekte edilir.

export type InvalidateFn = (key: readonly unknown[]) => void;

export interface Coalescer {
  /** Anahtarı pencereye ekler; pencere yoksa açar. */
  add: (key: readonly unknown[]) => void;
  /** Bekleyenleri hemen boşaltır (test / unmount). */
  flush: () => void;
  /** Bekleyen tekil anahtar sayısı. */
  pending: () => number;
}

export const COALESCE_MS = 250;

export function createCoalescer(
  invalidate: InvalidateFn,
  windowMs = COALESCE_MS,
  schedule: (fn: () => void, ms: number) => unknown = (fn, ms) => setTimeout(fn, ms),
): Coalescer {
  const pending = new Map<string, readonly unknown[]>();
  let armed = false;
  const flush = () => {
    armed = false;
    if (pending.size === 0) return;
    const keys = Array.from(pending.values());
    pending.clear();
    for (const k of keys) invalidate(k);
  };
  return {
    add(key) {
      pending.set(JSON.stringify(key), key);
      if (!armed) {
        armed = true;
        schedule(flush, windowMs);
      }
    },
    flush,
    pending: () => pending.size,
  };
}
