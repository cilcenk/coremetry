import { describe, expect, it } from 'vitest';
import { classifyMetric } from './metricTemplates';
import type { MetricInfo } from './types';

const m = (name: string, type: string, unit = ''): MetricInfo =>
  ({ name, type, unit, description: '' } as MetricInfo);

// v0.9.271 — first test file for this module.
//
// The type dictionary branched on 'counter' and 'updowncounter', which the
// ingest never writes. internal/otlp/convert.go:267 is the only writer of
// Instrument and its entire vocabulary is: gauge, sum, histogram,
// exp_histogram, summary. So the two branches were dead and 'sum' fell through
// to `default: avg`.
//
// Measured against the live catalogue at the time of the fix: 79 of 114
// metrics (69.3%) carry instrument='sum', and every one of them opened in
// Explore aggregated as avg.
describe('classifyMetric — OTel type fallback', () => {
  // The vocabulary the backend ACTUALLY writes. A name here that stops
  // resolving means the ingest and the frontend have drifted apart again.
  const cases: Array<[string, string]> = [
    ['sum', 'sum'],
    ['gauge', 'last'],
    ['histogram', 'p99'],
    ['exp_histogram', 'p99'],
    ['summary', 'p99'],
  ];
  it.each(cases)('instrument %s → agg %s', (type, want) => {
    // A deliberately template-proof name, so the TYPE path is what is tested.
    expect(classifyMetric(m('zz.unmatched.metric', type))?.agg).toBe(want);
  });

  it('sum no longer falls through to avg — this was the 69.3% case', () => {
    expect(classifyMetric(m('demo.auth.logged_in', 'sum'))?.agg).toBe('sum');
  });

  it('the ingest vocabulary is covered; unknown words still degrade to avg', () => {
    // Not a regression guard so much as a statement of intent: an instrument
    // this dictionary has never heard of must not throw or return null.
    expect(classifyMetric(m('zz.unmatched.metric', 'counter'))?.agg).toBe('avg');
    expect(classifyMetric(m('zz.unmatched.metric', ''))?.agg).toBe('avg');
  });
});

// The nine non-monotonic sums in the live catalogue are all process.runtime.*.
// They are LEVELS reported as OTel updowncounters, and monotonicity never
// reaches the frontend (metric_catalog carries instrument only). Without a name
// rule the fallback above would aggregate them as sum, which is worse than the
// avg it replaced: avg(heap_alloc) is a meaningful number, sum(heap_alloc) over
// a window is noise.
describe('classifyMetric — runtime levels must not be summed', () => {
  const live = [
    'process.runtime.go.mem.heap_alloc',
    'process.runtime.go.mem.heap_idle',
    'process.runtime.go.mem.heap_inuse',
    'process.runtime.go.mem.heap_objects',
    'process.runtime.go.mem.heap_released',
    'process.runtime.go.mem.heap_sys',
    'process.runtime.go.mem.live_objects',
    'process.runtime.go.goroutines',
    'process.runtime.go.cgo.calls',
  ];
  it.each(live)('%s → last, not sum', (name) => {
    // Every one of these arrives as instrument='sum' — that is exactly why the
    // name rule has to win over the type fallback.
    expect(classifyMetric(m(name, 'sum'))?.agg).toBe('last');
  });

  it('the name rule runs BEFORE the type fallback', () => {
    const t = classifyMetric(m('process.runtime.go.mem.heap_alloc', 'sum'));
    expect(t?.id).toBe('Runtime level');
  });

  it('other runtimes are covered too, not just Go', () => {
    expect(classifyMetric(m('process.runtime.jvm.mem.heap_used', 'sum'))?.agg).toBe('last');
  });

  it('a genuine counter that merely starts with process. is NOT caught', () => {
    // The rule is deliberately anchored on the runtime-level segments rather
    // than the whole process.* namespace, so it cannot swallow real counters.
    expect(classifyMetric(m('process.requests.total', 'sum'))?.agg).toBe('sum');
  });
});
