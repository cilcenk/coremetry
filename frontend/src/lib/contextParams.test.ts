// contextParams.test.ts — v0.10.250 ContextBar codec sözleşmesi (audit §12 test 1):
// from/to takma adları (now-1h/now, epoch ms/us/ns/s, çöp -> yok sayılır); ns
// yalnız rota izin verirse; compare=1 -> prior; boş değer siler, yabancıyı
// korur; takma adlar yazılmaz; uygulanmayan boyut okunmaz/yazılmaz.
import { describe, it, expect } from 'vitest';
import { parseNowExpr, epochToMs, rangeFromAliases, readScopeParams, writeScopeParams, contextSig, type ContextDim } from './contextParams';

const ALL = new Set<ContextDim>(['range', 'env', 'cluster', 'namespace', 'service', 'compare']);
const NOW = Date.UTC(2026, 8, 2, 12, 0, 0);

describe('takma adlar', () => {
  it('parseNowExpr', () => {
    expect(parseNowExpr('now')).toBe(0);
    expect(parseNowExpr('now-15m')).toBe(-15 * 60_000);
    expect(parseNowExpr('NOW-2h')).toBe(-2 * 3_600_000);
    expect(parseNowExpr('now-7d')).toBe(-7 * 86_400_000);
    expect(parseNowExpr('now+1h')).toBeNull();
    expect(parseNowExpr('yesterday')).toBeNull();
  });
  it('epochToMs birim tespiti: s / ms / us / ns', () => {
    expect(epochToMs('1788345600')).toBe(1788345600_000);
    expect(epochToMs('1788345600000')).toBe(1788345600000);
    expect(epochToMs('1788345600000000')).toBe(1788345600000);
    expect(epochToMs('1788345600000000000')).toBe(1788345600000);
    expect(epochToMs('abc')).toBeNull();
    expect(epochToMs('-5')).toBeNull();
  });
  it('rangeFromAliases: preset, custom, çöp', () => {
    expect(rangeFromAliases('now-1h', 'now', NOW)).toEqual({ preset: '1h' });
    expect(rangeFromAliases('now-3h', 'now-1h', NOW)).toEqual({ preset: 'custom', fromMs: NOW - 3 * 3_600_000, toMs: NOW - 3_600_000 });
    expect(rangeFromAliases('1788345600000', '1788349200000', NOW)).toEqual({ preset: 'custom', fromMs: 1788345600000, toMs: 1788349200000 });
    expect(rangeFromAliases('1788345600000000000', '1788349200000000000', NOW)).toEqual({ preset: 'custom', fromMs: 1788345600000, toMs: 1788349200000 });
    expect(rangeFromAliases('now-1h', null, NOW)).toBeNull();
    expect(rangeFromAliases('garbage', 'now', NOW)).toBeNull();
    expect(rangeFromAliases('1788349200000', '1788345600000', NOW)).toBeNull();
  });
});

describe('readScopeParams', () => {
  it('uygulanan boyutları okur; ns yalnız izinliyse; compare=1 -> prior', () => {
    const sp = new URLSearchParams('cluster=eu1&ns=pay&service=api&compare=1&x=1');
    expect(readScopeParams(sp, ALL)).toEqual({ cluster: 'eu1', namespace: '', service: 'api', compare: 'prior' });
    expect(readScopeParams(sp, ALL, true).namespace).toBe('pay');
    const only = new Set<ContextDim>(['range', 'service']);
    expect(readScopeParams(sp, only)).toEqual({ cluster: '', namespace: '', service: 'api', compare: '' });
    expect(readScopeParams(new URLSearchParams('compare=weird'), ALL).compare).toBe('');
  });
});

describe('writeScopeParams', () => {
  it('boş siler, yabancıyı korur, takma adı temizler, uygulanmayanı yazmaz', () => {
    const prev = new URLSearchParams('range=1h&cluster=eu1&ns=old&foo=bar&s_traces-list=time:desc');
    const next = writeScopeParams(prev, { cluster: '', namespace: 'pay', compare: 'prior' }, ALL);
    expect(next.get('cluster')).toBeNull();
    expect(next.get('namespace')).toBe('pay');
    expect(next.get('ns')).toBeNull();
    expect(next.get('compare')).toBe('prior');
    expect(next.get('foo')).toBe('bar');
    expect(next.get('range')).toBe('1h');
    expect(next.get('s_traces-list')).toBe('time:desc');
    const limited = writeScopeParams(prev, { service: 'api', namespace: 'pay' }, new Set<ContextDim>(['range', 'service']));
    expect(limited.get('service')).toBe('api');
    expect(limited.get('namespace')).toBeNull();
    expect(writeScopeParams(next, { namespace: 'pay' }, ALL).toString()).toBe(next.toString());
  });
});

describe('contextSig', () => {
  it('sıraya duyarlı, kararlı, boş/null eşdeğer', () => {
    expect(contextSig(['1h', 'prod', 'eu1'])).toBe(contextSig(['1h', 'prod', 'eu1']));
    expect(contextSig(['1h', 'prod', 'eu1'])).not.toBe(contextSig(['1h', 'eu1', 'prod']));
    expect(contextSig(['a', null])).toBe(contextSig(['a', undefined]));
    expect(contextSig(['a', ''])).toBe(contextSig(['a', null]));
  });
});
