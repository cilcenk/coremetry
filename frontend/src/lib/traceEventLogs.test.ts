// traceEventLogs.test.ts — v0.8.407 trace↔log correlation, zero-ES leg.
import { describe, expect, it } from 'vitest';
import { perSpanLogSignals, spanEventLogRows, traceServicesWithoutTraceField, isGrpcMessageEvent, splitGrpcMessageEvents, isNoisySpanEvent } from './traceEventLogs';
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

// v0.10.577 — gRPC per-message span event'lerinin gizlenmesi.
//
// OTel gRPC instrumentation'ı her mesaj için bir span event basıyor
// (body "message", attribute'ları yalnız message.id + message.type). Bir
// trace'te 253 tanesi çıkıp 11 gerçek log satırını görünmez yapıyordu.
//
// Yüklem DAR: dördü birden sağlanmazsa event KALIR. Asıl korunan şey
// gizlenenler değil, KORUNANLAR — exception'ı ya da gerçek gövdeli bir
// event'i düşürmek sessiz teşhis kaybıdır.
describe('isGrpcMessageEvent', () => {
  const row = (over: Partial<LogRow>): LogRow => ({
    id: -1, timestamp: 1, severity: 9, severityText: 'INFO',
    body: 'message', serviceName: 'shop-payment', traceId: 't', spanId: 's',
    attributes: { 'message.id': '1', 'message.type': 'SENT' },
    resourceAttributes: {}, origin: 'span-event', ...over,
  } as LogRow);

  it('SENT gizlenir', () => {
    expect(isGrpcMessageEvent(row({}))).toBe(true);
  });

  it('RECEIVED gizlenir', () => {
    expect(isGrpcMessageEvent(row({ attributes: { 'message.id': '2', 'message.type': 'RECEIVED' } }))).toBe(true);
  });

  it('message.id olmadan da gizlenir (alt küme)', () => {
    expect(isGrpcMessageEvent(row({ attributes: { 'message.type': 'SENT' } }))).toBe(true);
  });

  it('exception event KORUNUR', () => {
    expect(isGrpcMessageEvent(row({
      body: 'IllegalStateException: boom', severity: 17, severityText: 'ERROR',
      attributes: { 'exception.type': 'IllegalStateException', 'exception.message': 'boom' },
    }))).toBe(false);
  });

  it('EKSTRA attribute taşıyan event KORUNUR', () => {
    expect(isGrpcMessageEvent(row({
      attributes: { 'message.id': '1', 'message.type': 'SENT', 'rpc.grpc.status_code': '2' },
    }))).toBe(false);
  });

  it('gerçek gövdeli event KORUNUR', () => {
    expect(isGrpcMessageEvent(row({ body: 'payment authorised' }))).toBe(false);
  });

  it('bilinmeyen message.type KORUNUR', () => {
    expect(isGrpcMessageEvent(row({ attributes: { 'message.type': 'DROPPED' } }))).toBe(false);
  });

  it('message.type YOKSA korunur — anahtar kümesi alt küme olsa bile', () => {
    expect(isGrpcMessageEvent(row({ attributes: { 'message.id': '1' } }))).toBe(false);
  });

  it('ES log satırına ASLA dokunulmaz (origin yok)', () => {
    expect(isGrpcMessageEvent(row({ origin: undefined }))).toBe(false);
  });
});

describe('splitGrpcMessageEvents', () => {
  const ev = (type: string): LogRow => ({
    id: -1, timestamp: 1, severity: 9, severityText: 'INFO', body: 'message',
    serviceName: 's', traceId: 't', spanId: 'sp',
    attributes: { 'message.id': '1', 'message.type': type },
    resourceAttributes: {}, origin: 'span-event',
  } as LogRow);
  const real: LogRow = {
    id: -2, timestamp: 2, severity: 17, severityText: 'ERROR', body: 'boom',
    serviceName: 's', traceId: 't', spanId: 'sp',
    attributes: { 'exception.type': 'E' }, resourceAttributes: {}, origin: 'span-event',
  } as LogRow;

  it('ikiye ayırır ve SIRAYI korur', () => {
    const { visible, hidden } = splitGrpcMessageEvents([ev('SENT'), real, ev('RECEIVED')]);
    expect(visible).toEqual([real]);
    expect(hidden).toBe(2);
  });

  it('gizlenecek yoksa aynı diziyi döndürür', () => {
    const rows = [real];
    const { visible, hidden } = splitGrpcMessageEvents(rows);
    expect(visible).toBe(rows); // yeni dizi ayırmaz — gereksiz render yok
    expect(hidden).toBe(0);
  });
});

// v0.10.579 — gürültü kuralı GENİŞLEDİ (operatör onayı 2026-09-09).
//
// İkinci gürültü ailesi: `redis.encode.start` / `redis.encode.end` gibi
// hiç attribute taşımayan saf işaretçiler. Ad listesi tutmuyoruz — bugün
// redis, yarın başka kütüphane. Şeklin kendisi yakalanıyor: attribute'u
// olmayan bir event'in gövdesi HER ZAMAN adının kendisidir (eventBody
// zinciri boş çıkar), yani "bu olay oldu" demekten başka bilgi yok.
//
// EMNİYET KEMERİ: ERROR ve üstü hiçbir koşulda gizlenmez.
describe('isNoisySpanEvent — attribute taşımayan işaretçiler', () => {
  const marker = (over: Partial<LogRow>): LogRow => ({
    id: -1, timestamp: 1, severity: 9, severityText: 'INFO',
    body: 'redis.encode.start', serviceName: 's', traceId: 't', spanId: 'sp',
    attributes: {}, resourceAttributes: {}, origin: 'span-event', ...over,
  } as LogRow);

  it('attribute\'suz INFO işaretçisi gizlenir', () => {
    expect(isNoisySpanEvent(marker({}))).toBe(true);
    expect(isNoisySpanEvent(marker({ body: 'redis.encode.end' }))).toBe(true);
  });

  it('ERROR ve üstü ASLA gizlenmez — attribute\'suz olsa bile', () => {
    expect(isNoisySpanEvent(marker({ severity: 17, severityText: 'ERROR' }))).toBe(false);
    expect(isNoisySpanEvent(marker({ severity: 21, severityText: 'FATAL' }))).toBe(false);
  });

  it('attributes undefined ise de işaretçi sayılır', () => {
    expect(isNoisySpanEvent(marker({ attributes: undefined as unknown as Record<string, string> }))).toBe(true);
  });

  it('tek bir attribute bile event\'i KURTARIR', () => {
    expect(isNoisySpanEvent(marker({ attributes: { 'db.statement': 'GET k' } }))).toBe(false);
  });

  it('gRPC SENT/RECEIVED kuralı korunur', () => {
    expect(isNoisySpanEvent(marker({
      body: 'message', attributes: { 'message.id': '1', 'message.type': 'SENT' },
    }))).toBe(true);
  });

  it('ES log satırına dokunulmaz', () => {
    expect(isNoisySpanEvent(marker({ origin: undefined }))).toBe(false);
  });
});
