// dashboardHref — the ONE place that knows how a dashboard is addressed.
//
// v0.9.772, bug: Explore's "pin to dashboard" success link pointed at
// `/dashboards/<id>`. That route does not exist — App.tsx routes the
// list at `/dashboards` and the single dashboard at `/dashboard?id=`,
// so the extra path segment fell through to `path="*"` and the
// operator was silently bounced to the home page right after a
// successful pin. A hand-written template string is what made two
// spellings possible; this helper makes the right one the only one.
export function dashboardHref(id: string): string {
  return `/dashboard?id=${encodeURIComponent(id)}`;
}
