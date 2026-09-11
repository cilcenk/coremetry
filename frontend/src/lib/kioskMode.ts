import { normalizePath } from './auth-paths';

// isKioskBare — v0.10.673 (trace kiosk modu Dilim 3; audit
// docs/audit/trace-kiosk-mode-audit-2026-09-11.md §3 + §11 soru 1).
//
// Kimlikli-ama-KROMSUZ kabuk dalı yalnız /trace?kiosk=1 için: Sidebar,
// CopilotChat, Toaster, kısayollar mount edilmez, /api/events aboneliği
// kapalı (v0.8.529 sınıfı — yeni pencere sıfır akış, sıfır poll).
// Dashboard'un TV kiosk'u (?kiosk=1, v0.9.779) BU DALA GİRMEZ: orada bayrak
// yalnız CSS'tir (sidebar + duyuru gizli) ve SSE + asistan yaşamaya devam
// eder — mevcut davranış değişmedi.
//
// Saf; AppShell (render dalı) ve AuthProvider (401 kararı) aynı fonksiyonu
// okur — iki yerde iki yazım olmasın (gate-single-spelling dersi).
export const KIOSK_BARE_PATHS: ReadonlySet<string> = new Set(['/trace']);

export function isKioskBare(pathname: string, search: string): boolean {
  if (!KIOSK_BARE_PATHS.has(normalizePath(pathname ?? ''))) return false;
  return new URLSearchParams(search ?? '').get('kiosk') === '1';
}
