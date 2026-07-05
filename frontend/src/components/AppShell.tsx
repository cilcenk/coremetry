import { useEffect } from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { ShortcutsHelp } from './ShortcutsHelp';
import { CommandPalette } from './CommandPalette';
import { CopilotChat } from './CopilotChat';
import { GlobalShortcuts } from './GlobalShortcuts';
import { Toaster } from './Toaster';
import { useAuth } from './AuthProvider';
import { useEventStream } from '@/lib/queries';
import { useShortcuts } from '@/lib/keyboard';
import { isPublicPath } from '@/lib/auth-paths';
import { useBranding } from '@/lib/branding';
import { PageLoader } from './Spinner';
import { WhatChangedBanner } from './WhatChangedBanner';
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
function isPathAllowed(pathname: string, allowedPages: string[]): boolean {
  if (ALWAYS_ALLOWED.has(pathname)) return true;
  for (const p of allowedPages) {
    if (pathname === p) return true;
    if (pathname.startsWith(p + '/')) return true;
    if (p === '/traces' && pathname.startsWith('/trace')) return true;
    if (p === '/dashboards' && pathname.startsWith('/dashboard')) return true;
    if (p === '/services' && pathname.startsWith('/service')) return true;
    if (p === '/databases' && pathname.startsWith('/databases/')) return true;
  }
  return false;
}

// AppShell is the layout-route wrapper. React Router renders the
// active child route inside <Outlet/>. Public pages (login,
// public-status, public/trace) bypass the sidebar by being
// registered OUTSIDE this layout in App.tsx — but we keep the
// isPublicPath check as a defensive belt-and-suspenders so a
// future route refactor that accidentally puts a public page
// under this layout still won't render the sidebar to a
// not-yet-authenticated visitor.
export function AppShell() {
  const { pathname } = useLocation();
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

  // Global navigation shortcuts (Vim/Datadog flavour). Press
  // 'g' then a letter to jump to that page. The hook keeps its
  // bindings alive across route changes because AppShell is
  // the layout-route — never unmounts during navigation.
  // Suppressed automatically when an input/textarea is focused
  // (see lib/keyboard.ts).
  useShortcuts(
    [
      { keys: 'g s', label: 'Go to Services',     group: 'Navigation', handler: () => navigate('/services') },
      { keys: 'g m', label: 'Go to Service Map',  group: 'Navigation', handler: () => navigate('/service-map') },
      { keys: 'g t', label: 'Go to Traces',       group: 'Navigation', handler: () => navigate('/traces') },
      { keys: 'g l', label: 'Go to Logs',         group: 'Navigation', handler: () => navigate('/logs') },
      { keys: 'g e', label: 'Go to Explore',      group: 'Navigation', handler: () => navigate('/explore') },
      { keys: 'g r', label: 'Go to Runbooks',     group: 'Navigation', handler: () => navigate('/runbooks') },
      { keys: 'g d', label: 'Go to Dashboards',   group: 'Navigation', handler: () => navigate('/dashboards') },
      { keys: 'g i', label: 'Go to Incidents',    group: 'Navigation', handler: () => navigate('/incidents') },
      { keys: 'g p', label: 'Go to Problems',     group: 'Navigation', handler: () => navigate('/problems') },
      { keys: 'g a', label: 'Go to Anomalies',    group: 'Navigation', handler: () => navigate('/anomalies') },
      { keys: 'g x', label: 'Go to Alerts',       group: 'Navigation', handler: () => navigate('/alerts') },
      { keys: 'g o', label: 'Go to Monitors',     group: 'Navigation', handler: () => navigate('/monitors') },
      { keys: 'g c', label: 'Go to System stats', group: 'Navigation', handler: () => navigate('/admin/stats') },
    ],
    [navigate],
  );

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
      {/* v0.5.277 — page-top "what changed" ribbon. Open
          critical/warning counts + recent service.version
          transitions. Self-hides on a quiet install. */}
      <div id="main">
        <WhatChangedBanner />
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
      {/* Toaster (v0.5.455) — singleton notification surface.
          toast.success/error/info from anywhere in the app lands
          here. Renders null when empty so no overhead. */}
      <Toaster />
    </div>
  );
}
