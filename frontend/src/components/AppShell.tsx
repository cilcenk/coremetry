import { useEffect } from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { AnnouncementBanner } from './AnnouncementBanner';
import { ShortcutsHelp } from './ShortcutsHelp';
import { CommandPalette } from './CommandPalette';
import { CopilotChat } from './CopilotChat';
import { AIDrawer } from './ai/AIDrawer';
import { GlobalShortcuts } from './GlobalShortcuts';
import { Toaster } from './Toaster';
import { useAuth } from './AuthProvider';
import { useEventStream } from '@/lib/queries';
import { isPublicPath } from '@/lib/auth-paths';
import { useBranding } from '@/lib/branding';
import { PageLoader } from './Spinner';
import { ErrorBoundary } from './ErrorBoundary';

// ALWAYS_ALLOWED — routes the custom-role guard NEVER blocks, even
// when the user has a restrictive role. Profile/Login/PublicStatus
// must stay reachable so the operator can change their password +
// log out + reach the public surface; / (Home) and /public/trace
// are likewise infrastructure rather than nav surfaces.
const ALWAYS_ALLOWED = new Set(['/', '/login', '/profile', '/public-status', '/public/trace']);

// isPathAllowed mirrors the Sidebar's isActive() logic — a custom-
// role page `/traces` allows any `/trace*` URL (trace detail,
// compare); a `/dashboards` allows `/dashboard` (singular detail).
// Anything not in the allowed list (or in ALWAYS_ALLOWED) returns
// false → the guard redirects to the first allowed page.
export function isPathAllowed(pathname: string, allowedPages: string[]): boolean {
  if (ALWAYS_ALLOWED.has(pathname)) return true;
  for (const p of allowedPages) {
    if (pathname === p) return true;
    if (pathname.startsWith(p + '/')) return true;
    if (p === '/traces' && pathname.startsWith('/trace')) return true;
    if (p === '/dashboards' && pathname.startsWith('/dashboard')) return true;
    // v0.9.230 — was startsWith('/service'), which also matched
    // /service-map. Services and Topology are SEPARATE checkboxes in the
    // custom-role grid (internal/api/pages.go), so an admin who unchecked
    // Topology was still handing it over with Services.
    if (p === '/services' && (pathname === '/service' || pathname.startsWith('/service/'))) return true;
    // v0.9.230 — list pages whose detail route is a sibling, not a
    // sub-path: granting the list left every row click bouncing back to
    // the first allowed page. /clusters → /pod surfaced with v0.9.209,
    // which made /clusters grantable for the first time.
    if (p === '/incidents' && pathname === '/incident') return true;
    if (p === '/runbooks' && (pathname === '/runbook' || pathname === '/runbook-exec')) return true;
    if (p === '/clusters' && pathname === '/pod') return true;
    // v0.9.854 (UX denetimi K5) — v0.9.839/840'ta endpoint ve database
    // detayları çekmeceden SIBLING rotalara taşındı; istisna listesi
    // güncellenmedi. Yalnız /endpoints (ya da /databases) verilen
    // custom-rol kullanıcısı listeyi görüyor ama HER satır tıkı ilk
    // izinli sayfaya ışınlıyordu — iki detay sayfası o kullanıcı sınıfı
    // için tamamen ulaşılmaz. v0.9.230'un birebir tekrarı.
    if (p === '/endpoints' && pathname === '/endpoint') return true;
    if (p === '/databases' && pathname === '/database') return true;
  }
  return false;
}

// AppShell is the layout-route wrapper. React Router renders the
// active child route inside <Outlet/>.
//
// v0.9.981 — YORUM DÜZELTMESİ. Buradaki eski metin public sayfaların
// "App.tsx'te bu layout'un DIŞINDA kayıtlı" olduğunu, `isPublicPath`
// kontrolünün ise "yedek kemer" olduğunu söylüyordu. Rota listesi bunu
// YALANLIYOR: /login, /public-status ve /public/trace üçü de
// `<Route element={<AppShell/>}>`in İÇİNDE kayıtlı (App.tsx). Yani
// `isPublicPath` yedek değil, sidebar'ı basmayan TEK VE BİRİNCİL
// mekanizma — aşağıdaki erken dönüş kaldırılırsa giriş yapmamış bir
// ziyaretçi sidebar'ı görür. Yanlış yorum bir sonraki refactor'ü
// "zaten dışarıdalar, bu kontrol gereksiz" diye yanlış yönlendirirdi.
export function AppShell() {
  const { pathname, search } = useLocation();
  const navigate = useNavigate();
  const { user, loading } = useAuth();
  const isPublic = isPublicPath(pathname);
  // Subscribe so a saved branding update (from Settings) flows
  // through document.title + --accent immediately. Return value
  // unused here — applyBranding() inside the hook is the
  // side-effect we care about at shell level.
  useBranding();

  // SSE event stream — opens once we're authed + outside the
  // public surface (login, public-status). Receives
  // problem.open / problem.resolve / anomaly.* events and
  // invalidates the matching React Query caches so live state
  // changes show up in <1s. Closes on logout / unmount.
  useEventStream(!!user && !isPublic);

  // v0.8.525 — 'g <x>' navigation shortcuts consolidated into the
  // single GlobalShortcuts registry (mounted below). This inline block
  // was a SECOND registration that conflicted with GlobalShortcuts on
  // 'g o' / 'g m' and pointed at a hidden route ('g o' → Monitors) and
  // a retired path ('g c' → /admin/stats). One registry now — see
  // GlobalShortcuts.tsx.

  // Kiosk/TV modu (v0.9.779) — ?kiosk=1 kök elemana data-kiosk yazar,
  // gerisini CSS yapar (globals.css: sidebar + duyuru şeridi gizlenir,
  // #main artan genişliği kendiliğinden emer). DensityToggle'ın
  // data-density deseninin aynısı; body.classList bu kod tabanında
  // kullanılmıyor.
  //
  // Bilinçli olarak SADECE görsel: /kiosk PUBLIC_PATHS'e EKLENMEZ —
  // o bayrak auth kapısı + SSE anahtarı; kiosk kimlik doğrulamalı bir
  // oturumun içinde yaşar, yoksa bir TV linki panoları herkese açardı.
  //
  // Sayfadan çıkınca temizlenir (cleanup): kiosk /dashboard'a ait bir
  // görünüm; başka bir sayfaya gidip sol menüsüz kalmak hata olurdu.
  const kiosk = new URLSearchParams(search).get('kiosk') === '1';
  useEffect(() => {
    const el = document.documentElement;
    if (kiosk) el.setAttribute('data-kiosk', '1');
    else el.removeAttribute('data-kiosk');
    return () => el.removeAttribute('data-kiosk');
  }, [kiosk]);

  // Custom-role route guard (v0.5.251). When the user has a
  // customRolePages list, redirect any URL outside that set to
  // the first allowed page. Effect-based so the redirect doesn't
  // race the render; ALWAYS_ALLOWED keeps Profile / Home reachable
  // for password change + logout regardless of role restrictions.
  useEffect(() => {
    if (!user || isPublic) return;
    const allowed = user.customRolePages;
    if (!allowed) return; // unrestricted (admin/editor/plain viewer)
    if (isPathAllowed(pathname, allowed)) return;
    // Empty allowed list → strand on Profile so the operator can at
    // least change password / log out. Non-empty → first allowed.
    const target = allowed.length > 0 ? allowed[0] : '/profile';
    if (pathname !== target) navigate(target, { replace: true });
  }, [user, pathname, isPublic, navigate]);

  if (isPublic) {
    // v0.8.298 — route-scoped boundary on public pages too; key on the
    // path so navigating away from a crashed page auto-recovers.
    return <ErrorBoundary key={pathname}><Outlet /></ErrorBoundary>;
  }
  if (loading) {
    // v0.5.262 — centered OTel-mark loader instead of the bare
    // "Loading…" text. Matches the Suspense splash fallback used
    // for lazy routes so the app's initial paint reads as a
    // single coherent "loading" state, not two different
    // styles depending on whether the bundle or the auth check
    // is the slow path.
    return <PageLoader />;
  }
  if (!user) {
    // AuthProvider is in the middle of redirecting to /login.
    return null;
  }
  return (
    <div id="app">
      <Sidebar />
      <div id="main">
        {/* v0.8.486 — admin'in Settings'ten yönettiği duyuru şeridi
            (kaldırılan What-changed'in yerinde, operatör-kontrollü). */}
        <AnnouncementBanner />
        {/* v0.8.298 (quality bar S1) — route-scoped boundary INSIDE the
            shell: a page-render crash no longer unmounts the sidebar/nav
            (the App.tsx global boundary stays as last resort, but before
            this the operator lost the whole console to one bad row and
            could only Reload). key={pathname} remounts the boundary on
            navigation, so picking another page in the surviving sidebar
            auto-clears the fallback. */}
        <ErrorBoundary key={pathname}>
          <Outlet />
        </ErrorBoundary>
      </div>
      {/* ShortcutsHelp owns its own '?' binding + the modal
          render. Mount once at the shell so the help modal
          is reachable from any page without per-page wiring. */}
      <ShortcutsHelp />
      {/* CommandPalette (v0.5.162) — global Cmd-K / Ctrl-K
          spotlight. Self-contained: owns its hotkey binding +
          modal render; mounting here keeps it available on every
          authenticated page without per-page imports. */}
      <CommandPalette />
      {/* GlobalShortcuts (v0.5.444) — '/' to focus the page search
          input and 'g <x>' two-key sequences for fast page nav.
          Self-contained, renders null. Mounted here so every
          authenticated page inherits the bindings. */}
      <GlobalShortcuts />
      {/* CopilotChat (v0.6.53) — global in-app AI assistant. Fixed
          bottom-right launcher → drawer. Self-hides when no AI key
          is configured. Mounted here so it's reachable on every
          authenticated page, like CommandPalette. */}
      <CopilotChat />
      {/* AIDrawer (v0.9.477, onaylı mockup) — "✨ Explain" affordance'ının
          TEK evi. Yüzeydeki buton yalnız `?ai=<kind>:<id>` yazar, içeriği
          burada mount edilen çekmece render eder; sayfa-içi sekme değişimi
          cevabı söküp atmaz ve paylaşılan link aynı açıklamayı açar.
          Kapalıyken (adreste ?ai= yokken) hiçbir istek atmaz. */}
      <AIDrawer />
      {/* Toaster (v0.5.455) — singleton notification surface.
          toast.success/error/info from anywhere in the app lands
          here. Renders null when empty so no overhead. */}
      <Toaster />
    </div>
  );
}
