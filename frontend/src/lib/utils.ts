import type { TimeRange } from './types';

// Quick-range presets in seconds, ordered for the dropdown panel.
export const PRESET_SECONDS: Record<string, number> = {
  '5m':  300,    '15m': 900,    '30m': 1800,
  '1h':  3600,   '3h':  10800,  '6h':  21600,   '12h': 43200,
  '24h': 86400,  '2d':  172800, '7d':  604800,  '30d': 2592000,
};

export const PRESET_LABELS: Record<string, string> = {
  '5m':  'Last 5 minutes',   '15m': 'Last 15 minutes', '30m': 'Last 30 minutes',
  '1h':  'Last 1 hour',      '3h':  'Last 3 hours',    '6h':  'Last 6 hours',
  '12h': 'Last 12 hours',    '24h': 'Last 24 hours',   '2d':  'Last 2 days',
  '7d':  'Last 7 days',      '30d': 'Last 30 days',
};

// Converts a TimeRange to absolute nanosecond bounds for API queries.
export function timeRangeToNs(range: TimeRange): { from: number; to: number } {
  if (range.preset === 'custom' && range.fromMs && range.toMs) {
    return { from: range.fromMs * 1_000_000, to: range.toMs * 1_000_000 };
  }
  const secs = PRESET_SECONDS[range.preset] ?? 86400;
  const now = Date.now();
  return {
    from: Math.floor((now - secs * 1000) * 1_000_000),
    to: now * 1_000_000,
  };
}

// Compact label for the picker button.
export function timeRangeLabel(r: TimeRange): string {
  if (r.preset === 'custom' && r.fromMs && r.toMs) {
    const fmt = (ms: number) => new Date(ms).toLocaleString('en-GB',
      { dateStyle: 'short', timeStyle: 'short' });
    return `${fmt(r.fromMs)} → ${fmt(r.toMs)}`;
  }
  return PRESET_LABELS[r.preset] ?? r.preset;
}

export function fmtNum(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}

export function fmtNs(ns: number): string {
  const us = ns / 1e3, ms = ns / 1e6, s = ns / 1e9;
  if (s >= 1) return s.toFixed(2) + 's';
  if (ms >= 1) return ms.toFixed(2) + 'ms';
  if (us >= 1) return us.toFixed(0) + 'µs';
  return ns + 'ns';
}

export function tsShort(ns: number): string {
  if (!ns) return '—';
  const d = new Date(ns / 1e6);
  return d.toLocaleTimeString('en', { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0');
}

export function tsLong(ns: number): string {
  if (!ns) return '—';
  return new Date(ns / 1e6).toLocaleString('en', { dateStyle: 'short', timeStyle: 'medium' });
}

const COLORS = ['#6366f1', '#8b5cf6', '#ec4899', '#E30613', '#f97316', '#eab308',
                '#22c55e', '#14b8a6', '#3b82f6', '#06b6d4', '#a855f7', '#10b981'];
export function hashColor(s: string): string {
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h) ^ s.charCodeAt(i);
  return COLORS[Math.abs(h) % COLORS.length];
}

const SEV = ['', 'TRACE', 'TRACE2', 'TRACE3', 'TRACE4', 'DEBUG', 'DEBUG2', 'DEBUG3', 'DEBUG4',
             'INFO', 'INFO2', 'INFO3', 'INFO4', 'WARN', 'WARN2', 'WARN3', 'WARN4',
             'ERROR', 'ERROR2', 'ERROR3', 'ERROR4', 'FATAL', 'FATAL2', 'FATAL3', 'FATAL4'];
export function sevName(n: number): string { return SEV[n] || String(n); }
export function sevClass(n: number): string {
  if (n >= 21) return 's-fatal';
  if (n >= 17) return 's-error';
  if (n >= 13) return 's-warn';
  if (n >= 9)  return 's-info';
  if (n >= 5)  return 's-debug';
  return 's-trace';
}
