import { Link } from 'react-router-dom';

// TriageCrumb (v0.8.293, Option B slice 4) — a small breadcrumb on the
// per-source triage workspaces (/problems, /anomalies) showing they are a
// drill-down of the unified /inbox feed, with a one-click path back. The
// consolidation is UI-level: /inbox is the primary triage entry (top of the
// sidebar), these pages stay as deep-dive workspaces — the crumb makes that
// relationship explicit without any redirect.
export function TriageCrumb({ label }: { label: string }) {
  return (
    <div style={{ fontSize: 12, color: 'var(--text3)', margin: '2px 0 10px' }}>
      <Link to="/inbox" style={{ color: 'var(--accent2)', textDecoration: 'none' }}>
        Triage inbox
      </Link>
      <span style={{ margin: '0 6px' }}>›</span>
      <span style={{ color: 'var(--text2)' }}>{label}</span>
    </div>
  );
}
