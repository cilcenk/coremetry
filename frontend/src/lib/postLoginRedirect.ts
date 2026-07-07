// Post-login deep-link restore — v0.8.367 (operator-reported: a
// shared /traces?…filters=… link bounced an expired session to
// /login, and after signing in the operator landed on the default
// page instead of the pasted URL; Dynatrace restores the link).
//
// The intended URL is captured when the auth guard bounces a
// protected path to /login and consumed on the first authed render.
// sessionStorage on purpose: it is tab-scoped (a stale redirect
// can't leak into tomorrow's session) yet survives the OIDC
// full-page round-trip to the IdP and back, which react-router
// navigation state would not.

const KEY = 'coremetry-post-login-redirect';

// sanitizeRedirect accepts only same-origin, in-app paths. Anything
// scheme-ful or protocol-relative ('//evil.example') is rejected so a
// crafted link can't turn the restore into an open redirect; /login
// itself and public (unauthenticated) surfaces are pointless to
// restore. Pure — vitest alongside.
export function sanitizeRedirect(raw: string | null): string | null {
  if (!raw) return null;
  if (!raw.startsWith('/') || raw.startsWith('//')) return null;
  if (raw === '/login' || raw.startsWith('/login?') || raw.startsWith('/login/')) return null;
  if (raw.startsWith('/public/')) return null;
  return raw;
}

export function savePostLoginRedirect(path: string): void {
  const p = sanitizeRedirect(path);
  if (!p) return;
  try { sessionStorage.setItem(KEY, p); } catch { /* private mode etc. */ }
}

export function consumePostLoginRedirect(): string | null {
  try {
    const p = sanitizeRedirect(sessionStorage.getItem(KEY));
    sessionStorage.removeItem(KEY);
    return p;
  } catch {
    return null;
  }
}
