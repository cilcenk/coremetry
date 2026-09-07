import { describe, it, expect } from 'vitest';
import { parseAction, actionVisible, applyActionHref } from './pageActions';

// v0.10.542 — aksiyon ayrıştırma (kök-göreli, kind=apply), görünürlük (aynı
// sayfa), uygulama (sayfa-sahipli param'lar yer değiştirir, yabancı kalır).
describe('pageActions', () => {
  it('parseAction: yalnız apply + kök-göreli; etiket yoksa varsayılan', () => {
    expect(parseAction({ kind: 'apply', href: '/traces?service=api', label: 'X' })).toEqual({ kind: 'apply', href: '/traces?service=api', label: 'X' });
    expect(parseAction({ kind: 'apply', href: '/logs?q=a' })?.label).toBe('Bu sayfada uygula');
    expect(parseAction({ kind: 'navigate', href: '/x' })).toBeNull();
    expect(parseAction({ kind: 'apply', href: 'https://evil/traces' })).toBeNull();
    expect(parseAction({ kind: 'apply', href: '//evil/traces' })).toBeNull();
    expect(parseAction('x')).toBeNull();
  });
  it('görünürlük aynı sayfa; uygulama sayfa-sahipli param\'ları değiştirir, yabancıyı korur', () => {
    const a = parseAction({ kind: 'apply', href: '/traces?service=api&hasError=1' })!;
    expect(actionVisible(a, '/traces')).toBe(true);
    expect(actionVisible(a, '/logs')).toBe(false);
    expect(applyActionHref(a, '/logs', '')).toBeNull();
    const to = applyActionHref(a, '/traces', '?range=6h&env=prod&service=old&filters=%5B%5D')!;
    const sp = new URLSearchParams(to.slice(to.indexOf('?') + 1));
    expect(to.startsWith('/traces?')).toBe(true);
    expect(sp.get('service')).toBe('api');
    expect(sp.get('hasError')).toBe('1');
    expect(sp.get('range')).toBe('6h');
    expect(sp.get('env')).toBe('prod');
    expect(sp.has('filters')).toBe(false); // sayfa-sahipli eski filtre düştü
  });
  it('logs: sayfa-sahipli param\'lar (q/severity/filters) yer değiştirir', () => {
    const a = parseAction({ kind: 'apply', href: '/logs?service=api&q=timeout' })!;
    const to = applyActionHref(a, '/logs', '?range=1h&q=old&severity=17&cols=a')!;
    const sp = new URLSearchParams(to.slice(to.indexOf('?') + 1));
    expect(sp.get('q')).toBe('timeout');
    expect(sp.has('severity')).toBe(false);
    expect(sp.get('range')).toBe('1h');
    expect(sp.get('cols')).toBe('a');
  });
});
