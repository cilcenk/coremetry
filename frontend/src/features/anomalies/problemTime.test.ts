// problemTime.test.ts — Problems redesign (Variant B) time rules.
// Table-driven over EVERY branch (unit-mixing rule): short vs >20h
// windows, today vs older-day Started, all four duration units.
// Inputs are built from local-time Date components so expectations
// hold in any timezone.
import { describe, expect, it } from 'vitest';
import {
  DATED_TICK_WINDOW_SEC, fmtDurationNs, fmtHistTick, fmtStartedTs,
} from './problemTime';

const localSec = (y: number, mo: number, d: number, h: number, mi: number) =>
  new Date(y, mo, d, h, mi).getTime() / 1000;

describe('fmtHistTick', () => {
  const jul7 = localSec(2026, 6, 7, 8, 17);

  it('short window → bare clock', () => {
    expect(fmtHistTick(jul7, 3600)).toBe('08:17');
    expect(fmtHistTick(jul7, DATED_TICK_WINDOW_SEC)).toBe('08:17'); // boundary: 20h is NOT dated
  });

  it('window > 20h → dated tick', () => {
    expect(fmtHistTick(jul7, DATED_TICK_WINDOW_SEC + 1)).toBe('Jul 7 · 08:17');
    expect(fmtHistTick(localSec(2026, 11, 31, 23, 5), 7 * 86400)).toBe('Dec 31 · 23:05');
  });
});

describe('fmtStartedTs', () => {
  const now = new Date(2026, 6, 7, 14, 0, 0).getTime();

  it('today → HH:MM:SS', () => {
    const ts = new Date(2026, 6, 7, 9, 41, 7).getTime() * 1e6;
    expect(fmtStartedTs(ts, now)).toBe('09:41:07');
  });

  it('older than today → full date with year', () => {
    const ts = new Date(2026, 6, 6, 8, 17, 0).getTime() * 1e6;
    expect(fmtStartedTs(ts, now)).toBe('Jul 6, 2026 · 08:17');
    const lastYear = new Date(2025, 0, 3, 23, 59, 0).getTime() * 1e6;
    expect(fmtStartedTs(lastYear, now)).toBe('Jan 3, 2025 · 23:59');
  });

  it('same clock yesterday is still dated (calendar day, not 24h delta)', () => {
    const ts = new Date(2026, 6, 6, 14, 0, 0).getTime() * 1e6;
    expect(fmtStartedTs(ts, now)).toBe('Jul 6, 2026 · 14:00');
  });
});

describe('fmtDurationNs — every unit branch', () => {
  it.each([
    [45e9, '45s'],
    [89e9, '89s'],
    [90e9, '2m'],            // 90s rounds into minutes
    [45 * 60e9, '45m'],
    [2 * 3600e9, '2.0h'],
    [35 * 3600e9, '35.0h'],
    [40 * 3600e9, '1.7d'],
    [3 * 86400e9, '3.0d'],
  ])('%d ns → %s', (ns, want) => {
    expect(fmtDurationNs(ns as number)).toBe(want);
  });

  it('never negative', () => {
    expect(fmtDurationNs(-5e9)).toBe('0s');
  });
});
