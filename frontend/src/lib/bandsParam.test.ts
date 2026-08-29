// bandsParam.test.ts — v0.10.170 sözleşmesi (bandsParam.ts başlığı).
import { describe, it, expect } from 'vitest';
import { readBandsParam, writeBandsParam } from './bandsParam';

describe('bandsParam', () => {
  it('varsayılan KAPALI; yalnız bands=1 açar', () => {
    expect(readBandsParam(new URLSearchParams(''))).toBe(false);
    expect(readBandsParam(new URLSearchParams('bands=1'))).toBe(true);
    for (const v of ['0', 'true', 'yes', 'on', '']) expect(readBandsParam(new URLSearchParams(`bands=${v}`))).toBe(false);
  });
  it('yazım foreign param\'ları korur; kapatınca param silinir; girdi mutasyona uğramaz', () => {
    const prev = new URLSearchParams('name=api-gateway&range=1h&anomaly=abc');
    const on = writeBandsParam(prev, true);
    expect(on.toString()).toBe('name=api-gateway&range=1h&anomaly=abc&bands=1');
    expect(prev.has('bands')).toBe(false);
    expect(writeBandsParam(on, false).toString()).toBe('name=api-gateway&range=1h&anomaly=abc');
    expect(readBandsParam(writeBandsParam(on, false))).toBe(false);
  });
  it('v0.10.171 — canlı location.search tohumu: prev\'de olmayan (ham replaceState ile yazılmış) param korunur, prev\'in fazlası eklenir', () => {
    const prev = new URLSearchParams('name=x&anomaly=abc');
    const out = writeBandsParam(prev, true, '?name=x&anomaly=abc&aicode=1');
    expect(out.get('aicode')).toBe('1');
    expect(out.get('bands')).toBe('1');
    const merged = writeBandsParam(new URLSearchParams('name=x&only=prev'), false, '?name=x&aicode=1&bands=1');
    expect(merged.toString()).toBe('name=x&aicode=1&only=prev');
    expect(writeBandsParam(prev, true, '').get('anomaly')).toBe('abc'); // boş canlı → prev
  });
});
