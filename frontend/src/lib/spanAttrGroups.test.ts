// spanAttrGroups.test.ts — v0.10.277 sözleşmesi: semconv ön ekleri, sabit grup
// sırası, giriş sırası korunur, boş grup çıkmaz, bilinmeyen → Custom, null değer ''.
import { describe, it, expect } from 'vitest';
import { groupSpanAttrs, spanAttrGroupOf } from './spanAttrGroups';

describe('spanAttrGroups', () => {
  it('ön eklere göre gruplar ve sabit sırayla döner', () => {
    const g = groupSpanAttrs({
      'CHANNEL_CODE': 'MOBILE', 'db.system': 'oracle', 'http.route': '/x', 'k8s.pod.name': 'p-1',
      'messaging.system': 'kafka', 'db.statement': 'SELECT 1', 'url.path': '/x?y', 'rpc.service': 'Svc', 'net.peer.name': 'h',
    });
    expect(g.map(x => x.key)).toEqual(['http', 'db', 'messaging', 'rpc', 'network', 'infra', 'custom']);
    expect(g[0].entries.map(e => e[0])).toEqual(['http.route', 'url.path']);
    expect(g[1].entries.map(e => e[0])).toEqual(['db.system', 'db.statement']); // giriş sırası
    expect(g[6].entries).toEqual([['CHANNEL_CODE', 'MOBILE']]);
  });
  it('boş grup çıkmaz; boş/null girdi boş liste; null değer boş dize', () => {
    expect(groupSpanAttrs({})).toEqual([]);
    expect(groupSpanAttrs(undefined)).toEqual([]);
    const g = groupSpanAttrs({ 'exception.type': null as unknown as string, 'x': 1 });
    expect(g.map(x => x.key)).toEqual(['exception', 'custom']);
    expect(g[0].entries[0][1]).toBe('');
    expect(g[1].entries[0][1]).toBe('1');
  });
  it('spanAttrGroupOf', () => {
    expect(spanAttrGroupOf('http.method')).toBe('http');
    expect(spanAttrGroupOf('grpc.status_code')).toBe('rpc');
    expect(spanAttrGroupOf('container.id')).toBe('infra');
    expect(spanAttrGroupOf('FUNCTION_CODE')).toBe('custom');
  });
});
