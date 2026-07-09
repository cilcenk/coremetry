// problemTime — Problems redesign (Variant B) time formatting rules.
// Spec (coremetry-problems-redesign-prompt): the occurrences histogram
// axis shows dated ticks ("Jul 7 · 08:17") once the window exceeds 20
// hours and bare clock ticks ("09:41") below it; the Started field is
// HH:MM:SS when the problem started today and a full year-carrying
// date ("Jul 7, 2026 · 08:17") when it is older. Pure functions so
// every branch is table-tested (the Nh/Nd unit-mixing rule).

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export const DATED_TICK_WINDOW_SEC = 20 * 3600;

const two = (n: number) => String(n).padStart(2, '0');

// fmtHistTick — one histogram x-axis label. `windowSec` is the full
// chart span; > 20h flips every tick to the dated form so a Tuesday
// spike and a Wednesday spike can't read identically.
export function fmtHistTick(tsSec: number, windowSec: number): string {
  const d = new Date(tsSec * 1000);
  const hm = `${two(d.getHours())}:${two(d.getMinutes())}`;
  if (windowSec > DATED_TICK_WINDOW_SEC) {
    return `${MONTHS[d.getMonth()]} ${d.getDate()} · ${hm}`;
  }
  return hm;
}

// fmtStartedTs — the detail page's "Started" value. Today → precise
// clock (HH:MM:SS); any earlier calendar day → full date including
// the year ("Jul 7, 2026 · 08:17").
export function fmtStartedTs(tsNs: number, nowMs: number = Date.now()): string {
  const d = new Date(tsNs / 1e6);
  const now = new Date(nowMs);
  const sameDay = d.getFullYear() === now.getFullYear()
    && d.getMonth() === now.getMonth()
    && d.getDate() === now.getDate();
  if (sameDay) return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
  return `${MONTHS[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()} · ${two(d.getHours())}:${two(d.getMinutes())}`;
}

// fmtDurationNs — compact triage duration: seconds under 90s, minutes
// under 90m, one-decimal hours under 36h, then days.
export function fmtDurationNs(ns: number): string {
  const s = Math.max(0, Math.round(ns / 1e9));
  if (s < 90) return `${s}s`;
  if (s < 90 * 60) return `${Math.round(s / 60)}m`;
  if (s < 36 * 3600) return `${(s / 3600).toFixed(1)}h`;
  return `${(s / 86400).toFixed(1)}d`;
}
