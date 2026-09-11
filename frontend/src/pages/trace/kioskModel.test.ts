import { describe, it, expect } from 'vitest';
import { pickRootSpan, bundleLogsState } from './kioskModel';
import type { SpanRow, TraceBundleResponse } from '@/lib/types';

// v0.10.675 — TraceKiosk'un saf çekirdeği (audit §10 F9).
// SÖZLEŞME:
//   1. Kök span: analysis.rootSpanId varsa o; yoksa parent'sız ilk span;
//      o da yoksa ilk span; boşta undefined.
//   2. Bundle → TraceLogsPanel durumu: yüklenirken logs=undefined, hata
//      null; degraded slot → boş liste + neden metni (sekme boş görünmez);
//      Oracle degraded → oracleError=true, satırlar yine listelenir.
const sp = (id: string, parent = '', svc = 'shop'): SpanRow =>
  ({ traceId: 't', spanId: id, parentSpanId: parent, name: id, kind: 'server', serviceName: svc,
     hostName: '', startTime: 1, endTime: 2, durationMs: 1, statusCode: '', statusMessage: '',
     attributes: {}, resourceAttributes: {}, events: null, scopeName: '' }) as SpanRow;

describe('pickRootSpan', () => {
  it('analysis.rootSpanId öncelikli, sonra parent-sız, sonra ilk', () => {
    const spans = [sp('b', 'a'), sp('a'), sp('c', 'a')];
    expect(pickRootSpan(spans, { rootSpanId: 'c' })?.spanId).toBe('c');
    expect(pickRootSpan(spans, { rootSpanId: 'zzz' })?.spanId).toBe('a'); // id yoksa fallback
    expect(pickRootSpan(spans, undefined)?.spanId).toBe('a');
    expect(pickRootSpan([sp('x', 'orphan')], undefined)?.spanId).toBe('x');
    expect(pickRootSpan([], undefined)).toBeUndefined();
  });
});

describe('bundleLogsState', () => {
  const base = (over: Partial<TraceBundleResponse>): TraceBundleResponse => ({
    traceId: 't', spans: [], logs: { total: 0, logs: [] }, oracle: { enabled: false, logs: [], total: 0 },
    truncated: { spans: false, logs: false, oracle: false }, logLimit: 500, ...over,
  });
  it('yüklenirken undefined, hatada null', () => {
    expect(bundleLogsState(undefined, false).logs).toBeUndefined();
    expect(bundleLogsState(undefined, true).logs).toBeNull();
  });
  it('degraded log slotu boş liste + neden; toplam taşınır', () => {
    const st = bundleLogsState(base({ logs: { total: 0, logs: [], degraded: true, reason: 'log backend slow/unreachable' } }), false);
    expect(st.logs).toEqual([]);
    expect(st.degraded).toBe('log backend slow/unreachable');
    const ok = bundleLogsState(base({ logs: { total: 900, logs: [] } }), false);
    expect(ok.degraded).toBeNull();
    expect(ok.logsTotal).toBe(900);
  });
  it('Oracle degraded → oracleError, satırlar korunur', () => {
    const st = bundleLogsState(base({ oracle: { enabled: true, logs: [], total: 0, degraded: true, reason: 'oracle backend error' } }), false);
    expect(st.oracleError).toBe(true);
    expect(st.oracleRows).toEqual([]);
  });
});
