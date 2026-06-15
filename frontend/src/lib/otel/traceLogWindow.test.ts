// traceLogWindow — regression test for v0.8.180.
//
// Symptom: the trace→logs ES lookup (SpanDetail side-panel + Trace "Logs" tab)
// fetched by trace_id with NO from/to window, so ES scanned the WHOLE index
// (bounded only by terminate_after) — the v0.5.223 24h-from-now cap was removed
// to stop hiding old traces but never replaced with a trace-anchored window.
//
// Fix: derive a {from,to} window from the TRACE's own span times ±1min and pass
// it to /api/logs (parseTime → time.Unix(0,ns) → ES @timestamp range). The window
// is anchored to the trace's span times — NOT now() — so it does not reintroduce
// the v0.5.223 "old traces vanish" regression. This locks in the arithmetic +
// the unit (Unix nanoseconds) so the buffer can never silently flip to ms/µs.

import { describe, it, expect } from 'vitest';
import { traceLogWindow, TRACE_LOG_WINDOW_BUFFER_NS } from './hooks';

describe('traceLogWindow', () => {
  it('returns null for empty / missing span sets (caller falls back to unbounded)', () => {
    expect(traceLogWindow(undefined)).toBeNull();
    expect(traceLogWindow(null)).toBeNull();
    expect(traceLogWindow([])).toBeNull();
  });

  it('returns null when no span carries a usable time', () => {
    expect(traceLogWindow([{ startTime: 0, endTime: 0 }])).toBeNull();
    expect(traceLogWindow([{}])).toBeNull();
  });

  it('single span: window = [start-buffer, end+buffer]', () => {
    const start = 1_700_000_000_000_000_000; // Unix ns
    const end = start + 5_000_000; // +5ms
    const w = traceLogWindow([{ startTime: start, endTime: end }]);
    expect(w).toEqual({
      from: start - TRACE_LOG_WINDOW_BUFFER_NS,
      to: end + TRACE_LOG_WINDOW_BUFFER_NS,
    });
  });

  it('multi span: from anchors to the MIN start, to anchors to the MAX end', () => {
    const base = 1_700_000_000_000_000_000;
    const spans = [
      { startTime: base + 2_000_000, endTime: base + 3_000_000 }, // not the min/max
      { startTime: base, endTime: base + 1_000_000 }, // earliest start
      { startTime: base + 4_000_000, endTime: base + 9_000_000 }, // latest end
    ];
    const w = traceLogWindow(spans);
    expect(w).toEqual({
      from: base - TRACE_LOG_WINDOW_BUFFER_NS,
      to: base + 9_000_000 + TRACE_LOG_WINDOW_BUFFER_NS,
    });
  });

  it('skips zero/missing times but still bounds from the populated ones', () => {
    const base = 1_700_000_000_000_000_000;
    const spans = [
      { startTime: 0, endTime: 0 }, // ignored
      { startTime: base, endTime: base + 1_000_000 },
      { startTime: base + 500_000 }, // no endTime — contributes only to min-start
    ];
    const w = traceLogWindow(spans);
    expect(w).toEqual({
      from: base - TRACE_LOG_WINDOW_BUFFER_NS,
      to: base + 1_000_000 + TRACE_LOG_WINDOW_BUFFER_NS,
    });
  });

  it('honours a custom buffer (the param the SpanDetail fallback reuses)', () => {
    const start = 1_700_000_000_000_000_000;
    const end = start + 1_000_000;
    const w = traceLogWindow([{ startTime: start, endTime: end }], 10);
    expect(w).toEqual({ from: start - 10, to: end + 10 });
  });

  it('buffer constant is exactly 60s expressed in nanoseconds', () => {
    expect(TRACE_LOG_WINDOW_BUFFER_NS).toBe(60 * 1_000_000_000);
  });
});
