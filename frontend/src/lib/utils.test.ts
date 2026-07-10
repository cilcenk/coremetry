import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TimeRange } from './types';
import {
  displaySpanName,
  escapeHTML,
  fmtAgoNs,
  fmtBytes,
  fmtDurShort,
  fmtNs,
  fmtNum,
  isMessagingDep,
  substituteVars,
  timeRangeToNs,
} from './utils';

// First-ever frontend test file (v0.7.25). Targets the pure helpers in
// utils.ts that carry real incident history — every assertion below pins
// behaviour that has either bitten in production or guards a security/XSS or
// unit-correctness edge. Mirrors CLAUDE.md #11 (regression-test-for-bugfix) on
// the frontend.

describe('timeRangeToNs', () => {
  // v0.5.184: timeRangeToNs reads Date.now() internally. Called bare in JSX it
  // re-evaluates every render → infinite refetch. The function itself is
  // correct; these tests pin its OUTPUT determinism given a fixed clock, and
  // document that callers MUST memoize on `range` (see the CLAUDE.md pitfall).
  afterEach(() => vi.useRealTimers());

  const NOW_MS = 1_700_000_000_000; // fixed wall clock

  it('preset window: to == now, span == preset seconds', () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW_MS);
    const { from, to } = timeRangeToNs({ preset: '24h' } as TimeRange);
    expect(to).toBe(NOW_MS * 1_000_000);
    expect(to - from).toBe(86_400 * 1_000_000_000); // 24h in ns
  });

  it('5m preset spans exactly 300s', () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW_MS);
    const { from, to } = timeRangeToNs({ preset: '5m' } as TimeRange);
    expect(to - from).toBe(300 * 1_000_000_000);
  });

  it('unknown preset falls back to 24h, never NaN', () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW_MS);
    const { from, to } = timeRangeToNs({ preset: 'bogus' } as TimeRange);
    expect(to - from).toBe(86_400 * 1_000_000_000);
    expect(Number.isNaN(from)).toBe(false);
  });

  it('custom range uses fromMs/toMs verbatim (clock-independent)', () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW_MS);
    const r = { preset: 'custom', fromMs: 1_000, toMs: 2_000 } as TimeRange;
    expect(timeRangeToNs(r)).toEqual({ from: 1_000_000_000, to: 2_000_000_000 });
  });
});

describe('escapeHTML', () => {
  // XSS guard: a malicious ingester can ship service.name = "<script>"; every
  // innerHTML interpolation site wraps with this. & MUST be escaped first or
  // the other replacements double-escape.
  const cases: Array<[string, string]> = [
    ['<script>', '&lt;script&gt;'],
    ['a & b', 'a &amp; b'],
    [`"x"`, '&quot;x&quot;'],
    [`it's`, 'it&#39;s'],
    ['plain', 'plain'],
    ['<img src=x onerror="alert(1)">', '&lt;img src=x onerror=&quot;alert(1)&quot;&gt;'],
  ];
  it.each(cases)('escapes %j', (input, want) => {
    expect(escapeHTML(input)).toBe(want);
  });

  it('& is escaped before < > so entities are not double-escaped', () => {
    // If "<" escaped first, "a<b & c" would become "a&amp;lt;b ..." — verify not.
    expect(escapeHTML('a<b & c')).toBe('a&lt;b &amp; c');
  });

  it('null/undefined coerce to empty string', () => {
    expect(escapeHTML(null as unknown as string)).toBe('');
    expect(escapeHTML(undefined as unknown as string)).toBe('');
  });
});

describe('fmtBytes (binary / 1024)', () => {
  const cases: Array<[number, string]> = [
    [0, '0 B'],   // v0.8.272 dedup — canonical fallback is '0 B'
    [-5, '0 B'],
    [512, '512 B'],
    [1024, '1.00 KiB'],
    [1536, '1.50 KiB'],
    [10 * 1024, '10.0 KiB'], // 10..99 → 1 decimal
    [100 * 1024, '100 KiB'], // ≥100 → 0 decimals
    [1024 * 1024, '1.00 MiB'],
    [1024 ** 4, '1.00 TiB'],
    [1024 ** 5, '1.00 PiB'], // v0.8.272 — PiB rung added in the dedup
  ];
  it.each(cases)('fmtBytes(%d) = %s', (n, want) => {
    expect(fmtBytes(n)).toBe(want);
  });
});

describe('fmtNs (ns/µs/ms/s promotion)', () => {
  const cases: Array<[number, string]> = [
    [500, '500ns'],
    [1500, '2µs'], // 1.5µs → toFixed(0)
    [1_000_000, '1.00ms'],
    [2_500_000, '2.50ms'],
    [1_000_000_000, '1.00s'],
  ];
  it.each(cases)('fmtNs(%d) = %s', (ns, want) => {
    expect(fmtNs(ns)).toBe(want);
  });
});

describe('fmtNum (K/M)', () => {
  it.each([
    [999, '999'],
    [1500, '1.5K'],
    [1_500_000, '1.5M'],
  ] as Array<[number, string]>)('fmtNum(%d) = %s', (n, want) => {
    expect(fmtNum(n)).toBe(want);
  });
});

describe('substituteVars (dashboard variable expansion)', () => {
  it('resolves a single variable', () => {
    expect(substituteVars('service.name = "${service}"', { service: 'web' }))
      .toBe('service.name = "web"');
  });

  it('drops a line whose every variable is empty (all-services degrade)', () => {
    expect(substituteVars('service.name = "${service}"', { service: '' })).toBe('');
  });

  it('keeps lines with no variable references', () => {
    expect(substituteVars('status_code = "error"', {})).toBe('status_code = "error"');
  });

  it('multi-line: drops only the all-empty lines', () => {
    const tmpl = 'a = "${x}"\nb = "${y}"';
    expect(substituteVars(tmpl, { x: '1', y: '' })).toBe('a = "1"');
  });
});

describe('displaySpanName (OTel span-name enrichment)', () => {
  it('enriches a bare HTTP verb with server.address + path', () => {
    expect(displaySpanName({
      name: 'GET',
      attributes: { 'server.address': 'api.bank', 'url.path': '/orders' },
    })).toBe('GET api.bank/orders');
  });

  it('rewrites a generic grpc span to rpc.service/method', () => {
    expect(displaySpanName({
      name: 'grpc',
      attributes: { 'rpc.service': 'PaymentSvc', 'rpc.method': 'Charge' },
    })).toBe('PaymentSvc/Charge');
  });

  it('prefixes peer.service when it differs from rpc.service', () => {
    expect(displaySpanName({
      name: 'grpc',
      attributes: { 'rpc.service': 'PaymentSvc', 'rpc.method': 'Charge', 'peer.service': 'gateway' },
    })).toBe('gateway/PaymentSvc/Charge');
  });

  it('falls back to "<svc> → <peer>" for a bare generic call with only peer', () => {
    expect(displaySpanName({
      name: 'rpc',
      serviceName: 'checkout',
      attributes: { 'peer.service': 'inventory' },
    })).toBe('checkout → inventory');
  });

  it('leaves an already-descriptive name untouched', () => {
    expect(displaySpanName({ name: 'SELECT users', attributes: {} })).toBe('SELECT users');
  });
});

describe('isMessagingDep', () => {
  // v0.7.120 shipped a topology kafka-exclusion filter keyed on kind==='queue'.
  // The demo's kafka node is actually {kind:'external', subkind:'kafka'} (a
  // peer.service'd broker), so the v0.7.120 filter matched nothing and kafka
  // stayed on the graph — caught by runtime screenshot, fixed in v0.7.121.
  // This pins the broker matcher: catch messaging brokers however the backend
  // labels them, WITHOUT dropping real external HTTP deps or services that
  // merely contain a broker substring.
  it('matches the messaging.system queue kind regardless of subkind', () => {
    expect(isMessagingDep('queue', 'orders')).toBe(true);
    expect(isMessagingDep('queue', undefined)).toBe(true);
  });

  it('matches a peer.service external broker by subkind (the v0.7.120 miss)', () => {
    expect(isMessagingDep('external', 'kafka')).toBe(true);
    expect(isMessagingDep('external', 'kafka-broker-1')).toBe(true);
    expect(isMessagingDep('external', 'RabbitMQ')).toBe(true);
    expect(isMessagingDep('external', 'sqs')).toBe(true);
  });

  it('keeps real external HTTP deps (the ones the operator wants shown)', () => {
    expect(isMessagingDep('external', 'auth-service')).toBe(false);
    expect(isMessagingDep('external', 'email-service')).toBe(false);
    expect(isMessagingDep('external', 'payments-api')).toBe(false);
  });

  it('keeps services and databases', () => {
    expect(isMessagingDep(undefined, undefined)).toBe(false);
    expect(isMessagingDep('', undefined)).toBe(false);
    expect(isMessagingDep('db', 'redis')).toBe(false);
    expect(isMessagingDep('db', 'oracle')).toBe(false);
  });

  it('is anchored — a service merely containing a broker substring is NOT messaging', () => {
    // "payment-sqs-service" is a real HTTP service, not the SQS broker.
    expect(isMessagingDep('external', 'payment-sqs-service')).toBe(false);
    expect(isMessagingDep('external', 'my-kafka-proxy')).toBe(false);
  });
});

// v0.8.463 — fmtDurShort/fmtAgoNs dedup regresyonu: dört bileşen üç
// farklı giriş birimiyle (sec/ns/ms) kendi kopyasını taşıyordu — her
// birim tabloda pinli (unit-mixing pitfall kuralı).
describe('fmtDurShort / fmtAgoNs (v0.8.463)', () => {
  it('saniye kovaları', () => {
    expect(fmtDurShort(0)).toBe('0s');
    expect(fmtDurShort(51)).toBe('51s');
    expect(fmtDurShort(90)).toBe('2m');     // round
    expect(fmtDurShort(3599)).toBe('60m');
    expect(fmtDurShort(7200)).toBe('2h');
    expect(fmtDurShort(172800)).toBe('2d');
    expect(fmtDurShort(-5)).toBe('0s');     // negatif clamp
  });
  it('fmtAgoNs unix-NANOSANİYE alır (ms/sec değil)', () => {
    const now = Date.now();
    expect(fmtAgoNs((now - 51_000) * 1e6)).toBe('51s ago');
    expect(fmtAgoNs((now - 2 * 3600_000) * 1e6)).toBe('2h ago');
    // Yanlış birim (ms) verilirse near-epoch okunur → devasa yaş;
    // bu satır birim sözleşmesini pinler.
    expect(fmtAgoNs(now - 51_000)).not.toBe('51s ago');
  });
});
