// apiTimeout.test.ts — v0.8.413 regression. Operator-reported: "purge
// telemetry data" died with "Request timed out after 60s" — the global
// fetch abort fired mid-purge (and the server, then bound to
// r.Context(), stopped half-way). The purge call must ride its own
// 5-minute window; everything else keeps the 60s default.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';

describe('request timeout override (v0.8.413)', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('purgeTelemetry survives past the 60s default', async () => {
    let aborted = false;
    vi.stubGlobal('fetch', (_url: unknown, init?: RequestInit) => {
      return new Promise<Response>((resolve, reject) => {
        init?.signal?.addEventListener('abort', () => {
          aborted = true;
          reject(Object.assign(new Error('aborted'), { name: 'AbortError' }));
        });
        // Server answers after 2 minutes of ON CLUSTER TRUNCATEs.
        setTimeout(() => resolve(new Response(JSON.stringify({ tablesPurged: [] }), {
          status: 200, headers: { 'content-type': 'application/json' },
        })), 120_000);
      });
    });

    const p = api.purgeTelemetry();
    await vi.advanceTimersByTimeAsync(90_000); // past the old 60s cliff
    expect(aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(40_000); // server answers at 120s
    await expect(p).resolves.toEqual({ tablesPurged: [] });
  });

  it('default requests still abort at 60s', async () => {
    vi.stubGlobal('fetch', (_url: unknown, init?: RequestInit) => {
      return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () =>
          reject(Object.assign(new Error('aborted'), { name: 'AbortError' })));
      });
    });
    const p = api.health();
    const assertion = expect(p).rejects.toThrow(/timed out after 60s/);
    await vi.advanceTimersByTimeAsync(61_000);
    await assertion;
  });
});
