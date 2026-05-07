// Routes the user can reach without being authenticated. Centralised so the
// AppShell, AuthProvider and any future guard agree on the set.
//
// next.config.mjs sets trailingSlash: true, so pathnames arrive as `/login/`.
// Compare against a normalised form to avoid getting stuck redirecting.

// Routes that render WITHOUT the operator sidebar/topbar AND don't
// require auth. /public-status is the customer-facing status page;
// /login is the gatekeeper for everything else; /public/trace is the
// share-link viewer (token in the URL is the security boundary).
const PUBLIC_PATHS = new Set<string>(['/login', '/public-status', '/public/trace']);

export function normalizePath(p: string): string {
  if (!p) return '/';
  return p.length > 1 && p.endsWith('/') ? p.slice(0, -1) : p;
}

export function isPublicPath(p: string): boolean {
  return PUBLIC_PATHS.has(normalizePath(p));
}
