// useUrlRange.test.ts — v0.8.409 regression: an ABSOLUTE brushed
// window must never become the sticky cross-page default.
// Operator-reported: after a chart brush pinned custom:from-to into
// localStorage, every page load (and F5) served the same past window
// — read as "the page is cached". Relative presets stay sticky;
// custom windows are URL-only.
import { describe, expect, it } from 'vitest';
import { persistableRange, pickRangeString } from './useUrlRange';

describe('persistableRange (v0.8.409)', () => {
  it('relative presets persist as the global default', () => {
    for (const enc of ['5m', '15m', '30m', '1h', '24h', '7d']) {
      expect(persistableRange(enc)).toBe(true);
    }
  });
  it('absolute custom windows never persist', () => {
    expect(persistableRange('custom:1751980000000-1751983600000')).toBe(false);
  });
});

// v0.9.855 (UX denetimi K4) — href üreticileri (⌘K palette) pencereyi
// hook'suz çözmek zorunda: pivotHref pencereyi ZORUNLU argüman yapıyor
// (dört kez düşürüldüğü için). Öncelik zincirini her çağrı yerinde elden
// türetmek beşincisinin nasıl geleceğidir; zincir burada tek yerde ve
// pinlenmiş.
describe('pickRangeString (v0.9.855)', () => {
  it('URL her şeyi ezer — sayfada görünen pencere kazanır', () => {
    expect(pickRangeString('6h', '24h', '30m')).toBe('6h');
    expect(pickRangeString('custom:1000-2000', '24h', '30m')).toBe('custom:1000-2000');
  });

  it('URL yoksa sticky, o da yoksa varsayılan', () => {
    expect(pickRangeString(null, '24h', '30m')).toBe('24h');
    expect(pickRangeString(null, null, '30m')).toBe('30m');
  });

  it('sticky kanalından gelen MUTLAK pencere yok sayılır (v0.8.409 self-heal)', () => {
    // Eski sürümlerin localStorage'a dondurduğu custom: değeri geri
    // sızarsa her sayfa yine geçmiş bir pencerede açılırdı.
    expect(pickRangeString(null, 'custom:1000-2000', '30m')).toBe('30m');
  });

  it('useUrlRange ile AYNI zinciri uygular', () => {
    // useUrlRange: raw ?? readStoredRange() ?? defaultPreset — readStoredRange
    // zaten custom'ı eler. Aynı üç basamak, aynı sıra.
    for (const [url, stored, def, want] of [
      ['1h', '7d', '30m', '1h'],
      [null, '7d', '30m', '7d'],
      [null, null, '15m', '15m'],
    ] as Array<[string | null, string | null, string, string]>) {
      expect(pickRangeString(url, stored, def)).toBe(want);
    }
  });
});
