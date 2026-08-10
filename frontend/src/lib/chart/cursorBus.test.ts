import { describe, it, expect, beforeEach, vi } from 'vitest';
import { publishCursor, subscribe, getCursor, valueAtCursor, __resetCursorBus } from './cursorBus';

// explore-v2 Phase-3 — pins the cursorBus throttle semantics. The bus is
// what keeps the 60fps crosshair out of React: N synced panels publish the
// same cursor time per frame and only ONE notification (or zero, when the
// value didn't change) may reach subscribers. If the dedupe or the
// rAF-collapse regresses, every mousemove re-renders the GroupTable per
// panel and hover turns into a re-render storm. Exercised against the
// store half (subscribe/getCursor) — the React hook is a thin
// useSyncExternalStore over exactly these two functions.

beforeEach(() => {
  __resetCursorBus();
});

// publishCursor flushes on rAF in the browser, queueMicrotask under vitest —
// a macrotask hop covers both.
const flush = () => new Promise<void>(r => setTimeout(r, 0));

describe('cursorBus', () => {
  it('starts with no cursor', () => {
    expect(getCursor()).toBeNull();
  });

  it('delivers the published time after a flush and notifies subscribers', async () => {
    const seen = vi.fn();
    subscribe(seen);
    publishCursor(1700000000);
    await flush();
    expect(getCursor()).toBe(1700000000);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it('collapses intra-frame publishes to the LAST value, one notification', async () => {
    const seen = vi.fn();
    subscribe(seen);
    publishCursor(1);
    publishCursor(2);
    publishCursor(3);
    await flush();
    expect(getCursor()).toBe(3);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it('does not notify when the flushed value is unchanged (sync dedupe)', async () => {
    publishCursor(42);
    await flush();
    const seen = vi.fn();
    subscribe(seen);
    // Four synced panels republish the same time — zero notifications.
    publishCursor(42);
    publishCursor(42);
    publishCursor(42);
    publishCursor(42);
    await flush();
    expect(seen).not.toHaveBeenCalled();
    expect(getCursor()).toBe(42);
  });

  it('null clears the cursor (mouse left the chart)', async () => {
    publishCursor(7);
    await flush();
    publishCursor(null);
    await flush();
    expect(getCursor()).toBeNull();
  });

  it('unsubscribe stops notifications', async () => {
    const seen = vi.fn();
    const off = subscribe(seen);
    off();
    publishCursor(5);
    await flush();
    expect(seen).not.toHaveBeenCalled();
  });
});

// valueAtCursor bridges the bus (unix SECONDS) to TSSeries points (unix NANOS)
// and snaps to the nearest sample — the @cursor column must show what the
// operator sees under the crosshair, not interpolate or miss between buckets.
describe('valueAtCursor', () => {
  const ns = (sec: number) => sec * 1e9;
  // 100s spacing: samples at t=100,200,300 with values 10,20,30.
  const pts = [
    { time: ns(100), value: 10 },
    { time: ns(200), value: 20 },
    { time: ns(300), value: 30 },
  ];

  it('returns NaN for an empty series', () => {
    expect(valueAtCursor([], 150)).toBeNaN();
  });

  it('returns the exact value when the cursor sits on a sample', () => {
    expect(valueAtCursor(pts, 200)).toBe(20);
  });

  it('snaps to the nearer neighbour between samples', () => {
    expect(valueAtCursor(pts, 130)).toBe(10);   // closer to t=100
    expect(valueAtCursor(pts, 180)).toBe(20);   // closer to t=200
  });

  it('a tie picks the earlier sample (<=)', () => {
    expect(valueAtCursor(pts, 150)).toBe(10);   // equidistant 100/200 → earlier
  });

  it('clamps to the first / last sample outside the range', () => {
    expect(valueAtCursor(pts, 0)).toBe(10);
    expect(valueAtCursor(pts, 9999)).toBe(30);
  });

  it('returns NaN when the nearest sample is a gap (null)', () => {
    const withGap = [{ time: ns(100), value: 10 }, { time: ns(200), value: null }];
    expect(valueAtCursor(withGap, 195)).toBeNaN();
  });

  it('handles a single-point series', () => {
    expect(valueAtCursor([{ time: ns(50), value: 7 }], 999)).toBe(7);
  });
});
