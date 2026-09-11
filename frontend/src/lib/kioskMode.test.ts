import { describe, it, expect } from 'vitest';
import { isKioskBare } from './kioskMode';

// v0.10.673 — kiosk-çıplak dal yalnız /trace?kiosk=1 (audit §11 soru 1:
// Dashboard TV kiosk'unun davranışı DEĞİŞMEZ).
describe('isKioskBare (v0.10.673)', () => {
  it('yalnız /trace + ?kiosk=1', () => {
    expect(isKioskBare('/trace', '?id=abc&kiosk=1')).toBe(true);
    expect(isKioskBare('/trace/', '?kiosk=1')).toBe(true); // trailingSlash normalize
    expect(isKioskBare('/trace', '?id=abc')).toBe(false);
    expect(isKioskBare('/trace', '?kiosk=0')).toBe(false);
    expect(isKioskBare('/trace', '')).toBe(false);
  });

  it("Dashboard TV kiosk'u ve diğer sayfalar bu dala GİRMEZ", () => {
    expect(isKioskBare('/dashboard', '?id=x&kiosk=1')).toBe(false);
    expect(isKioskBare('/traces', '?kiosk=1')).toBe(false);
    expect(isKioskBare('/trace/compare', '?kiosk=1')).toBe(false);
  });
});
