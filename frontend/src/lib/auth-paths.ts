// Routes the user can reach without being authenticated. Centralised so the
// AppShell, AuthProvider and any future guard agree on the set.
//
// next.config.mjs sets trailingSlash: true, so pathnames arrive as `/login/`.
// Compare against a normalised form to avoid getting stuck redirecting.

const PUBLIC_PATHS = new Set<string>(['/login']);

export function normalizePath(p: string): string {
  if (!p) return '/';
  return p.length > 1 && p.endsWith('/') ? p.slice(0, -1) : p;
}

export function isPublicPath(p: string): boolean {
  return PUBLIC_PATHS.has(normalizePath(p));
}
