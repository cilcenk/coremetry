// traceEventLogs.test.ts — v0.8.407 trace↔log correlation, zero-ES leg.
import { describe, expect, it } from 'vitest';
import {
  perSpanLogSignals,
  spanEventLogRows,
  traceServicesWithoutTraceField,
} from './traceEventLogs';
import type { LogRow, SpanRow } from './types';

const span = (over: Partial<SpanRow>): SpanRow => ({
  traceId: 't1', spanId: 's1', parentId: '', name: 'op', kind: 'server',
  serviceName: 'checkout', startTime: 1000, endTime: 2000, durationMs: 1,
  statusCode: 'ok', statusMessage: '', attributes: {}, resourceAttributes: {},
  events: null, scopeName: '',
  ...over,
} as unknown as SpanRow);

describe('spanEventLogRows', () => {
  it('maps an exception event to an ERROR row with Type: message body', () => {
    const rows = spanEventLogRows([span({
      events: [{ name: 'exception', timeNano: 1500, attributes: {
        'exception.type': 'IOError', 'exception.message': 'disk full',
      } }],
    })]);
    expect(rows).toHaveLength(1);
    expect(rows[0].severity).toBe(17);
    expect(rows[0].severityText).toBe('ERROR');
    expect(rows[0].body).toBe('IOError: disk full');
    expect(rows[0].spanId).toBe('s1');
    expect(rows[0].origin).toBe('span-event');
    expect(rows[0].id).toBeLessThan(0); // never collides with backend ids
  });

  it('honours log-bridge severity spellings and message', () => {
    const rows = spanEventLogRows([span({
      events: [
        { name: 'log', timeNano: 1, attributes: { 'log.level': 'warn', 'log.message': 'slow' } },
        { name: 'log', timeNano: 2, attributes: { level: 'fatal', message: 'boom' } },
      ],
    })]);
    expect(rows[0].severity).toBe(13);
    expect(rows[0].severityText).toBe('WARN');
    expect(rows[0].body).toBe('slow');
    expect(rows[1].severity).toBe(21);
    expect(rows[1].body).toBe('boom');
  });

  it('plain annotation falls back to name + INFO; null events yield nothing', () => {
    const rows = spanEventLogRows([
      span({ events: [{ name: 'cache.miss', timeNano: 1, attributes: {} }] }),
      span({ spanId: 's2', events: null }),
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0].body).toBe('cache.miss');
    expect(rows[0].severity).toBe(9);
  });
});

describe('perSpanLogSignals', () => {
  const log = (spanId: string, severity: number): LogRow => ({
    id: 1, timestamp: 1, severity, severityText: '', body: '',
    serviceName: '', traceId: 't1', spanId,
    attributes: {}, resourceAttributes: {},
  });
  it('counts ES rows + event rows per span and flags errors', () => {
    const m = perSpanLogSignals(
      [log('a', 9), log('a', 17), log('', 21)], // empty spanId dropped
      [log('a', 9), log('b', 9)],
    );
    expect(m.get('a')).toEqual({ n: 3, err: true });
    expect(m.get('b')).toEqual({ n: 1, err: false });
    expect(m.has('')).toBe(false);
  });
  it('works before the ES fetch (undefined) — events alone', () => {
    const m = perSpanLogSignals(undefined, [log('a', 13)]);
    expect(m.get('a')).toEqual({ n: 1, err: false });
  });
});

describe('traceServicesWithoutTraceField', () => {
  it('names only services that HAVE logs but zero trace coverage', () => {
    const bad = traceServicesWithoutTraceField(
      ['checkout', 'payments', 'checkout', 'no-logs-svc'],
      [
        { service: 'checkout', total: 100, withTrace: 0 },
        { service: 'payments', total: 50, withTrace: 48 },
      ],
    );
    expect(bad).toEqual(['checkout']); // deduped; no-logs-svc excluded
  });
});
