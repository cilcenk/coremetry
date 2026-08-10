import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { fmtClock, fmtDateTime, tsLong, tsShort } from './utils';
import { parseTimeOfDay, withTimeOfDay } from './rangePicker';

// clockFormat.test.ts — v0.9.879.
//
// Operator-reported (2026-08-10): "tarih-saat her yerde 24 saat olsun,
// AM/PM istemiyorum". Two independent sources produced 12-hour text and
// NEITHER is visible to tsc / eslint / vitest / make audit, because both
// are correct code that simply follows the viewer's browser locale:
//
//   1. Native widgets — <input type="time"> in the global time picker and
//      <input type="datetime-local"> in the maintenance-window modal draw
//      an AM/PM segment on an en-US Chrome. The `lang` attribute is not a
//      reliable override there, so both became hand-drawn text inputs on
//      the "HH:mm:ss" / "YYYY-MM-DD HH:mm:ss" grammar the picker's absolute
//      inputs already used.
//   2. Bare locale formatters — eight surfaces called toLocale*String with
//      no arguments, so an en-US browser rendered "11:22:34 PM" directly
//      beside a waterfall rendering "23:22:34". They now route through the
//      two house formatters below.
//
// The house convention was ALREADY 24-hour (tsLong, v0.6.65 — same operator
// complaint, narrower blast radius). This release makes it enforceable.
//
// NOTE ON SELF-REFERENCE: the scanned needles are assembled from fragments
// at runtime so this file does not match its own rule. Six prior gates in
// this repo were silently green because the test's own prose satisfied the
// pattern it was searching for — do not inline these literals.

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx?$/.test(p)) out.push(p);
  }
  return out;
}

const rel = (p: string) => relative(SRC, p).split('\\').join('/');

// Comment-stripping that PRESERVES line numbers — a finding reported at the
// wrong line costs more than the scan saves.
const blankComments = (s: string) =>
  s
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .split('\n')
    .map(l => l.replace(/\/\/.*$/, ''))
    .join('\n');

interface Hit { file: string; line: number; text: string }

function scan(re: RegExp): Hit[] {
  const hits: Hit[] = [];
  for (const p of walk(SRC)) {
    const lines = blankComments(readFileSync(p, 'utf8')).split('\n');
    lines.forEach((l, i) => {
      if (new RegExp(re.source, re.flags.replace('g', '')).test(l)) {
        hits.push({ file: rel(p), line: i + 1, text: l.trim() });
      }
    });
  }
  return hits;
}

const fmt = (hits: Hit[]) => hits.map(h => `${h.file}:${h.line}  ${h.text}`).join('\n');

describe('24-hour lock (source scan)', () => {
  // NARROW signature: this method name exists for exactly one purpose, so a
  // hit is always the thing we care about. Searching for something broader
  // (e.g. "toLocale") would drag in the ~70 legitimate number-formatting
  // calls and force an allow-list nobody would keep current.
  it('no surface formats a clock through the browser locale', () => {
    const needle = 'toLocale' + 'TimeString\\(';
    const hits = scan(new RegExp(needle)).filter(h => h.file !== 'lib/utils.ts');
    expect(fmt(hits), 'route these through fmtClock (lib/utils.ts)').toBe('');
  });

  // Date-valued toLocaleString renders "8/10/2026, 11:22:34 PM" on en-US.
  // Only the `new Date(...)` spelling is mechanically detectable — a
  // Date held in a variable is not, which is why the unit tests below pin
  // the formatters themselves rather than relying on this scan alone.
  it('no surface formats a date+time through the browser locale', () => {
    const needle = 'new Date\\([^;]*\\)\\s*\\.toLocale' + 'String\\(';
    const hits = scan(new RegExp(needle));
    expect(fmt(hits), 'route these through fmtDateTime / tsLong').toBe('');
  });

  // Neither polarity is allowed. `hour12: true` is the bug; `hour12: false`
  // is the near-miss fix that resolves to the h24 cycle on some ICU builds
  // and prints midnight as "24:00:00" — the hand-rolled formatters have
  // neither failure mode.
  it('no surface pins the hour cycle through Intl options', () => {
    const hits = scan(new RegExp('hour' + '12'));
    expect(fmt(hits), 'use fmtClock / fmtDateTime instead of Intl options').toBe('');
  });

  // The two native widgets whose rendering the operator rejected. Guarded
  // by input TYPE, so a future "quick add" form cannot reintroduce a
  // locale-drawn clock without tripping this.
  it('no native time widget is rendered', () => {
    const hits = scan(/type="(time|datetime-local)"/);
    expect(fmt(hits), 'hand-draw the field on the HH:mm:ss grammar').toBe('');
  });
});

describe('fmtClock', () => {
  const at = (h: number, m: number, s: number) =>
    new Date(2026, 7, 10, h, m, s, 0);

  it('renders afternoon hours 24-hour with no meridiem', () => {
    expect(fmtClock(at(23, 22, 34))).toBe('23:22:34');
    expect(fmtClock(at(13, 5, 9))).toBe('13:05:09');
  });

  // The h24-vs-h23 trap: midnight must be 00, never 24.
  it('renders midnight as 00:00:00, not 24:00:00', () => {
    expect(fmtClock(at(0, 0, 0))).toBe('00:00:00');
  });

  it('zero-pads every component', () => {
    expect(fmtClock(at(1, 2, 3))).toBe('01:02:03');
  });

  it('accepts unix-ms and Date interchangeably', () => {
    const d = at(9, 41, 7);
    expect(fmtClock(d.getTime())).toBe(fmtClock(d));
  });
});

describe('fmtDateTime / tsLong', () => {
  const d = new Date(2026, 7, 10, 23, 22, 34, 0);

  it('renders dd.mm.yyyy HH:mm:ss (operator convention, v0.6.65)', () => {
    expect(fmtDateTime(d)).toBe('10.08.2026 23:22:34');
  });

  it('zero-pads day and month', () => {
    expect(fmtDateTime(new Date(2026, 0, 3, 0, 0, 0))).toBe('03.01.2026 00:00:00');
  });

  // tsLong is the nanosecond-native wrapper; the two must not drift.
  it('tsLong(ns) equals fmtDateTime(ms) for the same instant', () => {
    expect(tsLong(d.getTime() * 1e6)).toBe(fmtDateTime(d));
  });

  it('tsLong keeps its em-dash for a zero timestamp', () => {
    expect(tsLong(0)).toBe('—');
  });

  it('tsShort still appends milliseconds to the 24-hour clock', () => {
    const ms = new Date(2026, 7, 10, 23, 22, 34, 7).getTime();
    expect(tsShort(ms * 1e6)).toBe('23:22:34.007');
  });
});

describe('parseTimeOfDay', () => {
  it('accepts HH:mm and HH:mm:ss', () => {
    expect(parseTimeOfDay('08:00')).toBe(8 * 3600);
    expect(parseTimeOfDay('08:00:30')).toBe(8 * 3600 + 30);
    expect(parseTimeOfDay('23:59:59')).toBe(23 * 3600 + 59 * 60 + 59);
    expect(parseTimeOfDay('00:00:00')).toBe(0);
  });

  it('rejects out-of-range components', () => {
    expect(parseTimeOfDay('24:00:00')).toBeNull();
    expect(parseTimeOfDay('12:60')).toBeNull();
    expect(parseTimeOfDay('12:00:60')).toBeNull();
  });

  // The partial forms a hand-drawn field sees on every keystroke. Each must
  // read as "not yet valid" so the field paints red instead of committing.
  it('rejects partial and meridiem input', () => {
    for (const s of ['', '1', '12', '12:', '12:0', '1:00', '11:22:34 PM', '11:22 PM']) {
      expect(parseTimeOfDay(s), s).toBeNull();
    }
  });

  it('withTimeOfDay replaces the clock and keeps the calendar day', () => {
    const base = new Date(2026, 7, 10, 3, 0, 0).getTime();
    const out = withTimeOfDay(base, '23:15:02');
    expect(out).not.toBeNull();
    expect(fmtDateTime(out!)).toBe('10.08.2026 23:15:02');
  });

  it('withTimeOfDay rejects what parseTimeOfDay rejects', () => {
    expect(withTimeOfDay(Date.now(), '25:00')).toBeNull();
    expect(withTimeOfDay(Date.now(), '9:00')).toBeNull();
  });
});
