import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.673 — kiosk-çıplak kabuk dalı (audit §3-§4): /trace?kiosk=1'de
// Sidebar / CopilotChat / CommandPalette / GlobalShortcuts / Toaster /
// AnnouncementBanner mount OLMAZ ve /api/events aboneliği kapalı. Kaynak
// pini: bir refactor bu dala krom ya da akış sokarsa test düşer (v0.8.529
// sınıfı — "her pencere bir stream"). 401 dalı: kimlikli kiosk penceresi
// /login'e atlamaz, sessionEnded set eder (iki yönlendirme noktasından
// biri; ikincisi user düşürülmediği için hiç tetiklenmez).
const shell = readFileSync(resolve(__dirname, 'AppShell.tsx'), 'utf8');
const auth = readFileSync(resolve(__dirname, 'AuthProvider.tsx'), 'utf8');

describe('AppShell kiosk-çıplak dalı (v0.10.673)', () => {
  it('SSE aboneliği kioskBare ile kapalı', () => {
    expect(shell).toContain('useEventStream(!!user && !isPublic && !kioskBare)');
  });

  it('dal krom ve akış bileşeni çizmez; Outlet + oturum kartı çizer', () => {
    const i = shell.indexOf('if (kioskBare) {');
    expect(i).toBeGreaterThan(-1);
    const end = shell.indexOf('<div id="app">', i); // tam-kromlu kabuğun kökü
    expect(end).toBeGreaterThan(i);
    const block = shell.slice(i, end);
    for (const tag of ['<Sidebar', '<CopilotChat', '<CommandPalette', '<GlobalShortcuts', '<Toaster', '<AnnouncementBanner']) {
      expect(block).not.toContain(tag);
    }
    expect(block).toContain('<Outlet');
    expect(block).toContain('<SessionEndedCard');
  });
});

describe('AuthProvider 401 kiosk dalı (v0.10.673)', () => {
  it('kimlikli kiosk penceresi /login yerine sessionEnded', () => {
    const i = auth.indexOf('setUnauthorizedHandler(() => {');
    expect(i).toBeGreaterThan(-1);
    const block = auth.slice(i, auth.indexOf('return () => setUnauthorizedHandler(null)', i));
    expect(block).toContain('isKioskBare(window.location.pathname, window.location.search)');
    expect(block).toContain('setSessionEnded(true)');
    // Kiosk kararı user düşürülmeden ÖNCE: aksi hâlde rota kapısı /login'e atar.
    expect(block.indexOf('setSessionEnded(true)')).toBeLessThan(block.indexOf('setUser(null)'));
  });
});
