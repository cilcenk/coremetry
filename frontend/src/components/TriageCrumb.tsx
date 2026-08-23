import { Link, useLocation } from 'react-router-dom';
import { navHref } from '@/lib/navHref';

// TriageCrumb (v0.8.293, Option B slice 4) — a small breadcrumb on the
// per-source triage workspaces (/problems, /anomalies) showing they are a
// drill-down of the unified /inbox feed, with a one-click path back. The
// consolidation is UI-level: /inbox is the primary triage entry (top of the
// sidebar), these pages stay as deep-dive workspaces — the crumb makes that
// relationship explicit without any redirect.
//
// v0.9.1320 — the crumb went back to a BARE `/inbox`, so an operator who
// had brushed an absolute window on /problems landed on /inbox at whatever
// the sticky channel happened to hold. `navHref` is the repo's existing
// answer for a bare destination path (sidebar / ⌘K / `g x` all use it); the
// crumb reads `location.search` itself rather than taking a prop, because
// both call sites are page bodies that would otherwise have to thread it.
export function TriageCrumb({ label }: { label: string }) {
  const { search } = useLocation();
  return (
    <div style={{ fontSize: 12, color: 'var(--text3)', margin: '2px 0 10px' }}>
      <Link to={navHref('/inbox', search)} style={{ color: 'var(--accent2)', textDecoration: 'none' }}>
        Triage inbox
      </Link>
      <span style={{ margin: '0 6px' }}>›</span>
      <span style={{ color: 'var(--text2)' }}>{label}</span>
    </div>
  );
}
