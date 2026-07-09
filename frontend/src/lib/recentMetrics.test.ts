// recentMetrics.test.ts — v0.8.417 Data-Explorer parity DE2.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getRecentMetrics, recordMetricPick } from './recentMetrics';
import type { MetricInfo } from './types';

const mi = (name: string, over: Partial<MetricInfo> = {}): MetricInfo => ({
  name, description: `${name} desc`, unit: 'ms', type: 'Histogram', ...over,
});

// In-memory localStorage — the storage.ts wrapper feature-detects
// window.localStorage, so a plain Map-backed stub is enough.
function stubStorage() {
  const store = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => { store.set(k, v); },
    removeItem: (k: string) => { store.delete(k); },
  });
  return store;
}

describe('recentMetrics MRU', () => {
  let store: Map<string, string>;
  beforeEach(() => { store = stubStorage(); });
  afterEach(() => vi.unstubAllGlobals());

  it('newest pick lands first, dedupe by name refreshes the entry', () => {
    recordMetricPick(mi('a'));
    recordMetricPick(mi('b'));
    recordMetricPick(mi('a', { unit: 's' })); // re-pick with a fresher unit
    const names = getRecentMetrics();
    expect(names.map(m => m.name)).toEqual(['a', 'b']);
    expect(names[0].unit).toBe('s');
  });

  it('caps at 8, dropping the oldest', () => {
    for (let i = 0; i < 10; i++) recordMetricPick(mi(`m${i}`));
    const names = getRecentMetrics().map(m => m.name);
    expect(names).toHaveLength(8);
    expect(names[0]).toBe('m9');
    expect(names).not.toContain('m0');
    expect(names).not.toContain('m1');
  });

  it('ignores empty names and survives poisoned stores', () => {
    recordMetricPick(mi(''));
    expect(getRecentMetrics()).toEqual([]);
    store.set('coremetry.recentMetrics', '["just-a-string",{"name":"ok","description":"","unit":"","type":"Gauge"},{"nope":1}]');
    expect(getRecentMetrics().map(m => m.name)).toEqual(['ok']);
    store.set('coremetry.recentMetrics', 'not-json{{');
    expect(getRecentMetrics()).toEqual([]);
  });
});
