// spanLinks.test.ts — v0.10.274 sözleşmesi: outgoing sahibine, incoming
// hedefine; aynı-trace link'i iki uçta; tekrar düşer; boş payload boş indeks.
import { describe, it, expect } from 'vitest';
import { indexSpanLinks, linkedSpanIds, linkCount } from './spanLinks';
import type { SpanLink } from './types';

const L = (p: Partial<SpanLink>): SpanLink => ({
  traceId: 'T', spanId: 's1', linkedTraceId: 'U', linkedSpanId: 'u1',
  timeUnixNs: 0, serviceName: 'svc', ...p,
});

describe('indexSpanLinks', () => {
  it('outgoing satır sahibine, incoming satır hedefine indekslenir', () => {
    const idx = indexSpanLinks('T', {
      outgoing: [L({ spanId: 's1', linkedTraceId: 'U', linkedSpanId: 'u1' })],
      incoming: [L({ traceId: 'V', spanId: 'v9', linkedTraceId: 'T', linkedSpanId: 's2' })],
    });
    expect(idx.get('s1')?.outgoing.length).toBe(1);
    expect(idx.get('s1')?.incoming.length).toBe(0);
    expect(idx.get('s2')?.incoming[0].traceId).toBe('V');
    expect([...linkedSpanIds(idx)].sort()).toEqual(['s1', 's2']);
    expect(linkCount(idx.get('s1'))).toBe(1);
    expect(linkCount(undefined)).toBe(0);
  });
  it('aynı-trace link\'i kaynakta outgoing, hedefte incoming; tekrar düşer', () => {
    const same = L({ spanId: 'a', linkedTraceId: 'T', linkedSpanId: 'b' });
    const idx = indexSpanLinks('T', { outgoing: [same, same], incoming: [L({ traceId: 'T', spanId: 'a', linkedTraceId: 'T', linkedSpanId: 'b' })] });
    expect(idx.get('a')?.outgoing.length).toBe(1);
    expect(idx.get('b')?.incoming.length).toBe(1);
    expect(linkedSpanIds(idx).size).toBe(2);
  });
  it('boş / eksik payload boş indeks', () => {
    expect(indexSpanLinks('T', undefined).size).toBe(0);
    expect(indexSpanLinks('T', { outgoing: [L({ spanId: '' })], incoming: [] }).size).toBe(0);
  });
});
