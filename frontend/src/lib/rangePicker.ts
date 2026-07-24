import type { TimeRange } from './types';
import { PRESET_SECONDS } from './utils';

// ─────────────────────────────────────────────────────────────────────────────
// rangePicker — pure logic behind the Grafana-parity global time picker
// (components/TimeRangePicker.tsx). Everything here is side-effect free and
// injectable-now so vitest covers it without fake timers or a DOM.
//
// Scope note (operator brief 2026-07-24): the Grafana `now-6h` TEXT GRAMMAR is
// deliberately OUT OF SCOPE — the absolute inputs accept `YYYY-MM-DD HH:mm:ss`
// forms only; relative windows go through the quick-range presets. The global
// refresh-interval dropdown is likewise out of scope.
// ─────────────────────────────────────────────────────────────────────────────

// Ordered quick-range list for the panel's right column. Derived from
// PRESET_SECONDS so the picker, timeRangeToNs and the URL codec can never
// disagree about which presets exist (single source of truth). All 11
// Grafana-standard rungs (5m…30d) are already in the contract.
export const QUICK_PRESETS: string[] = Object.keys(PRESET_SECONDS);

// localStorage key for the "recently used" list (operator brief name).
export const RECENTS_KEY = 'range-recents';

/** Resolve any TimeRange to absolute unix-ms bounds. Pure sibling of
 *  utils.timeRangeToNs (which is ns + implicit Date.now()). */
export function resolveRangeMs(
  r: TimeRange,
  nowMs: number,
): { fromMs: number; toMs: number } {
  if (r.preset === 'custom' && r.fromMs && r.toMs) {
    return { fromMs: r.fromMs, toMs: r.toMs };
  }
  const secs = PRESET_SECONDS[r.preset] ?? 86400;
  return { fromMs: nowMs - secs * 1000, toMs: nowMs };
}

/** Grafana zoom-out: widen the window 2× around its CENTER (center stays
 *  fixed, both edges move out by half the old span). A preset drops to an
 *  absolute custom range — the operator asked for "what surrounds this
 *  window", freezing it is the point. Floor at epoch 0. */
export function zoomOutRange(r: TimeRange, nowMs: number): TimeRange {
  const { fromMs, toMs } = resolveRangeMs(r, nowMs);
  const span = Math.max(1000, toMs - fromMs);
  const center = fromMs + (toMs - fromMs) / 2;
  return {
    preset: 'custom',
    fromMs: Math.max(0, Math.round(center - span)),
    toMs: Math.round(center + span),
  };
}

/** Push an encoded range onto the recents list: dedupe, front-insert, cap. */
export function pushRecent(recents: string[], enc: string, max = 4): string[] {
  return [enc, ...recents.filter(r => r !== enc)].slice(0, max);
}

/** Safe-parse the recents list from localStorage raw. Never throws. */
export function parseRecents(raw: string | null): string[] {
  if (!raw) return [];
  try {
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

// ── Calendar grid ────────────────────────────────────────────────────────────

export interface CalCell {
  y: number;       // full year of the cell
  m: number;       // 0-based month of the cell
  d: number;       // day of month
  inMonth: boolean; // false for the leading/trailing fill days
}

/** 6×7 Monday-first month grid (42 cells) for the hand-rolled mini calendar.
 *  `month` is 0-based and may be out of range (-1, 12, …) — it normalises the
 *  same way `new Date(y, m, 1)` does, so month navigation is just m±1. */
export function calendarGrid(year: number, month: number): CalCell[] {
  const base = new Date(year, month, 1);
  const y = base.getFullYear();
  const m = base.getMonth();
  const lead = (base.getDay() + 6) % 7; // getDay(): 0=Sun → Monday-first offset
  const cells: CalCell[] = [];
  for (let i = 0; i < 42; i++) {
    const d = new Date(y, m, 1 - lead + i);
    cells.push({
      y: d.getFullYear(),
      m: d.getMonth(),
      d: d.getDate(),
      inMonth: d.getMonth() === m && d.getFullYear() === y,
    });
  }
  return cells;
}

/** Day-click state machine: first click starts a selection (from = day start,
 *  to pending), second click on the same-or-later day completes it (to = day
 *  end — so from-day == to-day yields a whole-day range), a click BEFORE the
 *  pending start restarts the selection from that day. `prev.toMs !== null`
 *  (completed selection) also restarts. Caller supplies day bounds so local
 *  DST days (23h/25h) stay correct. */
export function dayClickRange(
  prev: { fromMs: number | null; toMs: number | null },
  dayStartMs: number,
  dayEndMs: number,
): { fromMs: number; toMs: number | null } {
  if (prev.fromMs === null || prev.toMs !== null) {
    return { fromMs: dayStartMs, toMs: null };
  }
  if (dayEndMs <= prev.fromMs) {
    return { fromMs: dayStartMs, toMs: null };
  }
  return { fromMs: prev.fromMs, toMs: dayEndMs };
}

// ── Absolute datetime parsing / formatting ───────────────────────────────────

const pad2 = (n: number) => String(n).padStart(2, '0');

/** Local-time "YYYY-MM-DD HH:mm:ss". Inverse of parseDateTime. */
export function formatDateTime(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}` +
    ` ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

/** "HH:mm:ss" time-of-day for the native `<input type="time" step=1>`. */
export function formatTimeOfDay(ms: number): string {
  const d = new Date(ms);
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

/** Parse an absolute LOCAL datetime: "YYYY-MM-DD", "YYYY-MM-DD HH:mm",
 *  "YYYY-MM-DD HH:mm:ss" (space or T separator). Returns unix ms or null.
 *  Rejects impossible dates (2026-02-31) and out-of-range time parts.
 *  Deliberately NO `now-…` grammar — see scope note at the top. */
export function parseDateTime(s: string): number | null {
  const m = s.trim().match(
    /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?$/,
  );
  if (!m) return null;
  const [y, mo, day] = [+m[1], +m[2], +m[3]];
  const [h, mi, se] = [m[4] ? +m[4] : 0, m[5] ? +m[5] : 0, m[6] ? +m[6] : 0];
  if (mo < 1 || mo > 12 || h > 23 || mi > 59 || se > 59) return null;
  const d = new Date(y, mo - 1, day, h, mi, se, 0);
  // Date silently rolls overflow (Feb 31 → Mar 3) — reject instead.
  if (d.getFullYear() !== y || d.getMonth() !== mo - 1 || d.getDate() !== day) return null;
  return d.getTime();
}

/** Replace the local time-of-day of `ms` with "HH:mm" / "HH:mm:ss". */
export function withTimeOfDay(ms: number, hms: string): number | null {
  const m = hms.match(/^(\d{2}):(\d{2})(?::(\d{2}))?$/);
  if (!m) return null;
  const [h, mi, se] = [+m[1], +m[2], m[3] ? +m[3] : 0];
  if (h > 23 || mi > 59 || se > 59) return null;
  const d = new Date(ms);
  d.setHours(h, mi, se, 0);
  return d.getTime();
}

// ── Labels ───────────────────────────────────────────────────────────────────
// Hand-rolled month tables instead of Intl so the label is deterministic in
// tests and identical across browsers/ICU builds. Matches lib/i18n's two
// supported languages.

export const MONTHS_SHORT: Record<'en' | 'tr', string[]> = {
  en: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
  tr: ['Oca', 'Şub', 'Mar', 'Nis', 'May', 'Haz', 'Tem', 'Ağu', 'Eyl', 'Eki', 'Kas', 'Ara'],
};
export const MONTHS_LONG: Record<'en' | 'tr', string[]> = {
  en: ['January', 'February', 'March', 'April', 'May', 'June', 'July',
    'August', 'September', 'October', 'November', 'December'],
  tr: ['Ocak', 'Şubat', 'Mart', 'Nisan', 'Mayıs', 'Haziran', 'Temmuz',
    'Ağustos', 'Eylül', 'Ekim', 'Kasım', 'Aralık'],
};
// Monday-first weekday initials for the calendar header row.
export const DOW_SHORT: Record<'en' | 'tr', string[]> = {
  en: ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'],
  tr: ['Pt', 'Sa', 'Ça', 'Pe', 'Cu', 'Ct', 'Pz'],
};

/** "24 Tem 08:00" — year appears only when it differs from now's year
 *  ("3 Oca 2025 08:00"), Grafana-style compact button label. */
export function absDateTimeLabel(ms: number, lang: 'en' | 'tr', nowMs: number): string {
  const d = new Date(ms);
  const yr = d.getFullYear() !== new Date(nowMs).getFullYear() ? ` ${d.getFullYear()}` : '';
  return `${d.getDate()} ${MONTHS_SHORT[lang][d.getMonth()]}${yr}` +
    ` ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** Button label for an absolute range: "24 Tem 08:00 → 24 Tem 12:30". */
export function absRangeLabel(
  fromMs: number, toMs: number, lang: 'en' | 'tr', nowMs: number,
): string {
  return `${absDateTimeLabel(fromMs, lang, nowMs)} → ${absDateTimeLabel(toMs, lang, nowMs)}`;
}

/** "UTC+3", "UTC+5:30", "UTC-4" from minutes EAST of UTC
 *  (i.e. `-new Date().getTimezoneOffset()`). */
export function utcOffsetLabel(offsetMin: number): string {
  const sign = offsetMin < 0 ? '-' : '+';
  const abs = Math.abs(offsetMin);
  const h = Math.floor(abs / 60);
  const mn = abs % 60;
  return `UTC${sign}${h}${mn ? ':' + pad2(mn) : ''}`;
}
