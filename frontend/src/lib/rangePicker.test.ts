import { describe, expect, it } from 'vitest';
import {
  QUICK_PRESETS,
  absRangeLabel,
  calendarGrid,
  dayClickRange,
  formatDateTime,
  formatTimeOfDay,
  parseDateTime,
  parseRecents,
  pushRecent,
  resolveRangeMs,
  utcOffsetLabel,
  withTimeOfDay,
  zoomOutRange,
} from './rangePicker';
import { PRESET_SECONDS, timeRangeToNs } from './utils';
import { decodeRange, encodeRange } from './urlState';

// Grafana-parity picker pure logic (2026-07-24 brief). All Date math is done
// through local-time constructors on both sides of each assertion, so the
// suite is timezone-agnostic.

describe('QUICK_PRESETS contract', () => {
  it('exposes the 11 Grafana rungs in panel order', () => {
    expect(QUICK_PRESETS).toEqual([
      '5m', '15m', '30m', '1h', '3h', '6h', '12h', '24h', '2d', '7d', '30d',
    ]);
  });

  it('every quick preset resolves through timeRangeToNs (no unknown-preset fallback)', () => {
    for (const p of QUICK_PRESETS) {
      const secs = PRESET_SECONDS[p];
      expect(secs, p).toBeGreaterThan(0);
      const { from, to } = timeRangeToNs({ preset: p });
      expect(to - from, p).toBe(secs * 1e9);
    }
  });

  it('every quick preset round-trips the URL codec unchanged (back-compat)', () => {
    for (const p of QUICK_PRESETS) {
      expect(encodeRange({ preset: p })).toBe(p);
      expect(decodeRange(p, { preset: '30m' })).toEqual({ preset: p });
    }
  });
});

describe('resolveRangeMs', () => {
  const now = 1_753_351_800_000;

  it('resolves a preset against the injected now', () => {
    expect(resolveRangeMs({ preset: '1h' }, now))
      .toEqual({ fromMs: now - 3_600_000, toMs: now });
  });

  it('passes an absolute range through untouched', () => {
    expect(resolveRangeMs({ preset: 'custom', fromMs: 100, toMs: 200 }, now))
      .toEqual({ fromMs: 100, toMs: 200 });
  });

  it('falls back to 24h for an unknown preset (mirrors timeRangeToNs)', () => {
    expect(resolveRangeMs({ preset: 'bogus' }, now))
      .toEqual({ fromMs: now - 86_400_000, toMs: now });
  });
});

describe('zoomOutRange', () => {
  const now = 1_753_351_800_000;

  it('widens an absolute range 2x around a fixed center', () => {
    const r = zoomOutRange({ preset: 'custom', fromMs: 100_000, toMs: 200_000 }, now);
    expect(r).toEqual({ preset: 'custom', fromMs: 50_000, toMs: 250_000 });
  });

  it('drops a preset to custom: 1h at now → [now-1h30m, now+30m]', () => {
    const r = zoomOutRange({ preset: '1h' }, now);
    expect(r.preset).toBe('custom');
    expect(r.fromMs).toBe(now - 5_400_000);
    expect(r.toMs).toBe(now + 1_800_000);
  });

  it('floors from at epoch 0', () => {
    const r = zoomOutRange({ preset: 'custom', fromMs: 50_000, toMs: 250_000 }, now);
    expect(r.fromMs).toBe(0);
    expect(r.toMs).toBe(350_000);
  });
});

describe('pushRecent / parseRecents', () => {
  it('front-inserts and caps at 4', () => {
    expect(pushRecent(['a', 'b', 'c', 'd'], 'e')).toEqual(['e', 'a', 'b', 'c']);
  });

  it('dedupes an existing entry to the front instead of duplicating', () => {
    expect(pushRecent(['a', 'b', 'c'], 'b')).toEqual(['b', 'a', 'c']);
  });

  it('starts from empty', () => {
    expect(pushRecent([], '1h')).toEqual(['1h']);
  });

  it('parseRecents never throws: null, garbage, non-array, mixed types', () => {
    expect(parseRecents(null)).toEqual([]);
    expect(parseRecents('{oops')).toEqual([]);
    expect(parseRecents('"str"')).toEqual([]);
    expect(parseRecents('[1, "1h", null, "custom:1-2"]')).toEqual(['1h', 'custom:1-2']);
  });
});

describe('calendarGrid', () => {
  it('July 2026 is Monday-first: 42 cells starting Mon Jun 29', () => {
    const cells = calendarGrid(2026, 6);
    expect(cells).toHaveLength(42);
    expect(cells[0]).toEqual({ y: 2026, m: 5, d: 29, inMonth: false });
    expect(cells[1]).toEqual({ y: 2026, m: 5, d: 30, inMonth: false });
    expect(cells[2]).toEqual({ y: 2026, m: 6, d: 1, inMonth: true });
    expect(cells.filter(c => c.inMonth)).toHaveLength(31);
  });

  it('a month starting on Monday has zero lead fill (June 2026)', () => {
    const cells = calendarGrid(2026, 5); // June 1 2026 is a Monday
    expect(cells[0]).toEqual({ y: 2026, m: 5, d: 1, inMonth: true });
  });

  it('normalises out-of-range months so navigation is just m±1', () => {
    expect(calendarGrid(2026, 12)).toEqual(calendarGrid(2027, 0));
    expect(calendarGrid(2026, -1)).toEqual(calendarGrid(2025, 11));
  });
});

describe('dayClickRange', () => {
  const day = (d: number) => ({
    start: new Date(2026, 6, d, 0, 0, 0, 0).getTime(),
    end: new Date(2026, 6, d, 23, 59, 59, 999).getTime(),
  });

  it('first click starts a selection (to pending)', () => {
    const d10 = day(10);
    expect(dayClickRange({ fromMs: null, toMs: null }, d10.start, d10.end))
      .toEqual({ fromMs: d10.start, toMs: null });
  });

  it('second click on a later day completes the range at day end', () => {
    const d10 = day(10); const d12 = day(12);
    expect(dayClickRange({ fromMs: d10.start, toMs: null }, d12.start, d12.end))
      .toEqual({ fromMs: d10.start, toMs: d12.end });
  });

  it('second click on the SAME day yields a whole-day range', () => {
    const d10 = day(10);
    expect(dayClickRange({ fromMs: d10.start, toMs: null }, d10.start, d10.end))
      .toEqual({ fromMs: d10.start, toMs: d10.end });
  });

  it('clicking before the pending start restarts the selection', () => {
    const d10 = day(10); const d5 = day(5);
    expect(dayClickRange({ fromMs: d10.start, toMs: null }, d5.start, d5.end))
      .toEqual({ fromMs: d5.start, toMs: null });
  });

  it('a completed selection restarts on the next click', () => {
    const d10 = day(10); const d20 = day(20);
    expect(dayClickRange({ fromMs: d10.start, toMs: d10.end }, d20.start, d20.end))
      .toEqual({ fromMs: d20.start, toMs: null });
  });
});

describe('parseDateTime / formatDateTime', () => {
  it('parses full, minute and date-only local forms', () => {
    expect(parseDateTime('2026-07-24 08:30:15'))
      .toBe(new Date(2026, 6, 24, 8, 30, 15).getTime());
    expect(parseDateTime('2026-07-24T08:30'))
      .toBe(new Date(2026, 6, 24, 8, 30, 0).getTime());
    expect(parseDateTime('2026-07-24'))
      .toBe(new Date(2026, 6, 24, 0, 0, 0).getTime());
  });

  it('round-trips through formatDateTime', () => {
    const ms = new Date(2026, 0, 3, 23, 59, 59).getTime();
    expect(parseDateTime(formatDateTime(ms))).toBe(ms);
  });

  it('rejects rolled-over dates and out-of-range time parts', () => {
    expect(parseDateTime('2026-02-31 10:00')).toBeNull();
    expect(parseDateTime('2026-13-01')).toBeNull();
    expect(parseDateTime('2026-07-24 24:00')).toBeNull();
    expect(parseDateTime('2026-07-24 10:75')).toBeNull();
  });

  it('rejects the now-grammar — deliberately out of scope (2026-07-24 brief)', () => {
    expect(parseDateTime('now')).toBeNull();
    expect(parseDateTime('now-6h')).toBeNull();
    expect(parseDateTime('garbage')).toBeNull();
  });
});

describe('withTimeOfDay / formatTimeOfDay', () => {
  it('replaces the local time-of-day keeping the date', () => {
    const base = new Date(2026, 6, 24, 8, 30, 15).getTime();
    expect(withTimeOfDay(base, '12:45:05'))
      .toBe(new Date(2026, 6, 24, 12, 45, 5).getTime());
    expect(withTimeOfDay(base, '00:00'))
      .toBe(new Date(2026, 6, 24, 0, 0, 0).getTime());
  });

  it('rejects malformed or out-of-range times', () => {
    expect(withTimeOfDay(0, '25:00')).toBeNull();
    expect(withTimeOfDay(0, '10:99')).toBeNull();
    expect(withTimeOfDay(0, 'noon')).toBeNull();
  });

  it('formatTimeOfDay emits HH:mm:ss', () => {
    expect(formatTimeOfDay(new Date(2026, 6, 24, 8, 5, 9).getTime())).toBe('08:05:09');
  });
});

describe('absRangeLabel', () => {
  const now = new Date(2026, 6, 24, 15, 0).getTime();

  it('same-year TR: "24 Tem 08:00 → 24 Tem 12:30" (brief example)', () => {
    const from = new Date(2026, 6, 24, 8, 0).getTime();
    const to = new Date(2026, 6, 24, 12, 30).getTime();
    expect(absRangeLabel(from, to, 'tr', now)).toBe('24 Tem 08:00 → 24 Tem 12:30');
  });

  it('cross-year includes the year; EN month table', () => {
    const from = new Date(2025, 11, 31, 23, 0).getTime();
    const to = new Date(2026, 0, 1, 1, 0).getTime();
    expect(absRangeLabel(from, to, 'en', now)).toBe('31 Dec 2025 23:00 → 1 Jan 01:00');
  });
});

describe('utcOffsetLabel', () => {
  it('whole hours, half hours, negative, zero', () => {
    expect(utcOffsetLabel(180)).toBe('UTC+3');
    expect(utcOffsetLabel(330)).toBe('UTC+5:30');
    expect(utcOffsetLabel(-240)).toBe('UTC-4');
    expect(utcOffsetLabel(0)).toBe('UTC+0');
  });
});
