import { useState, useMemo } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { useAuditLog } from '@/lib/queries';
import { tsLong } from '@/lib/utils';

// Curated catalog of every action the API currently emits via
// s.audit(). Kept in lock-step with internal/api/*; new entries
// added here as soon as a new audit call ships so the action
// dropdown is fully populated even when the current window is
// empty. Discovered values from the response are still merged
// at render time as a safety net.
const KNOWN_ACTIONS = [
  'alert_rule.create',
  'alert_rule.update',
  'alert_rule.delete',
  'alert_rule.enable',
  'anomaly_silence.bulk_delete',
  'anomaly_silence.create',
  'anomaly_silence.delete',
  'dashboard.create',
  'dashboard.update',
  'dashboard.delete',
  'notification_channel.create',
  'notification_channel.update',
  'notification_channel.delete',
  'problem.acknowledge',
  'saved_view.create',
  'saved_view.delete',
  'settings.ai.update',
  'settings.ldap.update',
  'settings.sampling.update',
  'settings.smtp.update',
  'sql.query',
  'user.create',
  'user.delete',
  'user.reset_password',
  'user.set_role',
  'user.set_team',
];

// /admin/audit shows the append-only audit_log: who did what, when.
// Admin-only — the API also enforces this server-side, but the SPA
// hides the page from non-admin sidebars.
export default function AuditPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [target, setTarget] = useState('');
  const [since, setSince] = useState('24h');
  // v0.5.348 — free-text search across every visible column.
  // Filters client-side after the backend filter pass; lets
  // the operator "grep" within the current window without
  // forcing a full re-query.
  const [search, setSearch] = useState('');
  // v0.5.348 — expanded-details set. Click a Details cell to
  // toggle the row into a full pre-formatted JSON block. Set-
  // backed so a previously expanded row stays expanded on
  // re-render.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // useAuditLog re-keys on every filter change so each
  // (actor, action, target, since) tuple caches separately;
  // tabbing back to a previously viewed filter is instant.
  const auditQ = useAuditLog(since, {
    actor: actor.trim() || undefined,
    action: action.trim() || undefined,
    target: target.trim() || undefined,
  });
  const data = !isAdmin ? null
    : auditQ.isLoading ? undefined
    : auditQ.isError ? null
    : auditQ.data ?? [];

  // Seed the dropdown with every action the API currently emits
  // so an empty window doesn't hide a filter the operator is
  // looking for (e.g. "who acknowledged this morning's noise" —
  // if no acks landed in the default 24h slice, the option would
  // otherwise vanish). Discovered actions from the current page
  // get merged in case the backend ships a new one ahead of us.
  const distinctActions = useMemo(() => {
    const s = new Set<string>(KNOWN_ACTIONS);
    (data ?? []).forEach(e => s.add(e.action));
    return Array.from(s).sort();
  }, [data]);

  // Client-side search — case-insensitive substring across
  // actor / action / target / details / ip. Empty search = no
  // filtering, so the typical empty-input case is a no-op.
  const visible = useMemo(() => {
    const rows = data ?? [];
    const q = search.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter(e => {
      const hay = [
        e.actorEmail, e.actorRole, e.action,
        e.targetKind, e.targetId, e.details, e.ip,
      ].filter(Boolean).join(' ').toLowerCase();
      return hay.includes(q);
    });
  }, [data, search]);

  const toggleExpand = (id: string) => {
    setExpanded(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  if (!isAdmin) {
    return (
      <>
        <Topbar title="Audit log" />
        <div id="content"><Empty icon="◇" title="Admin only">
          The audit log records every state-changing action by a
          user. Restricted to the admin role.
        </Empty></div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Audit log" />
      <div id="content">
        <div className="controls" style={{ marginBottom: 8 }}>
          <select value={since} onChange={e => setSince(e.target.value)}>
            <option value="1h">Last 1h</option>
            <option value="24h">Last 24h</option>
            <option value="7d">Last 7d</option>
            <option value="30d">Last 30d</option>
          </select>
          <input placeholder="Actor (email or id)…"
            value={actor} onChange={e => setActor(e.target.value)} style={{ width: 220 }} />
          <select value={action} onChange={e => setAction(e.target.value)}>
            <option value="">All actions</option>
            {distinctActions.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
          <input placeholder="Target kind (e.g. alert_rule)"
            value={target} onChange={e => setTarget(e.target.value)} style={{ width: 200 }} />
          <input placeholder="Search within results…"
            value={search} onChange={e => setSearch(e.target.value)} style={{ width: 220 }} />
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {search.trim() && data
              ? `${visible.length} / ${data.length} entries`
              : `${data?.length ?? 0} entries`}
          </span>
          {/* CSV export uses the same filter shape as the JSON view —
              just a different endpoint that streams to download.
              Anchor element with attributes built inline so the
              browser handles the download path without us juggling
              a Blob; auth cookies travel with the request. */}
          <a href={`/api/admin/audit/export?since=${encodeURIComponent(since)}`
            + (actor.trim() ? `&actor=${encodeURIComponent(actor.trim())}` : '')
            + (action.trim() ? `&action=${encodeURIComponent(action.trim())}` : '')
            + (target.trim() ? `&target=${encodeURIComponent(target.trim())}` : '')}
            className="sec"
            style={{ fontSize: 11, padding: '4px 10px', textDecoration: 'none' }}
            title="Download current view as CSV">
            ↓ Export CSV
          </a>
        </div>

        {data === undefined && <Spinner />}
        {data !== undefined && data?.length === 0 && (
          <Empty icon="◇" title="No audit entries in this range" />
        )}
        {data && data.length > 0 && visible.length === 0 && (
          <Empty icon="◇" title="No entries match your search">
            <button onClick={() => setSearch('')} className="sec"
              style={{ fontSize: 12, padding: '4px 12px', marginTop: 8 }}>
              Clear search
            </button>
          </Empty>
        )}
        {data && visible.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead><tr>
                <th>Time</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Target</th>
                <th>Details</th>
                <th>IP</th>
              </tr></thead>
              <tbody>
                {visible.map(e => {
                  const isExpanded = expanded.has(e.id);
                  return (
                    <tr key={e.id}>
                      <td className="mono" style={{ fontSize: 11, whiteSpace: 'nowrap' }}>
                        {tsLong(e.time)}
                      </td>
                      <td>
                        <div style={{ fontWeight: 600, fontSize: 12 }}>
                          {e.actorEmail
                            ? <FilterClick onClick={() => setActor(e.actorEmail)}>{e.actorEmail}</FilterClick>
                            : '—'}
                        </div>
                        <div style={{ fontSize: 10, color: 'var(--text3)' }}>{e.actorRole}</div>
                      </td>
                      <td className="mono" style={{ fontSize: 11 }}>
                        <FilterClick onClick={() => setAction(e.action)}>{e.action}</FilterClick>
                      </td>
                      <td className="mono" style={{ fontSize: 11 }}>
                        <FilterClick onClick={() => setTarget(e.targetKind)}>{e.targetKind}</FilterClick>
                        {e.targetId && <span style={{ color: 'var(--text3)' }}> · {e.targetId}</span>}
                      </td>
                      <td onClick={() => e.details && toggleExpand(e.id)}
                          style={{ fontSize: 11, color: 'var(--text2)',
                                   maxWidth: isExpanded ? undefined : 360,
                                   overflow: isExpanded ? 'visible' : 'hidden',
                                   textOverflow: isExpanded ? 'clip' : 'ellipsis',
                                   whiteSpace: isExpanded ? 'pre-wrap' : 'nowrap',
                                   wordBreak: isExpanded ? 'break-all' : undefined,
                                   fontFamily: 'monospace',
                                   cursor: e.details ? 'pointer' : 'default' }}
                          title={isExpanded ? 'Click to collapse' : (e.details || '')}>
                        {isExpanded
                          ? <span>{prettyJSON(e.details)}</span>
                          : (e.details || '—')}
                      </td>
                      <td className="mono" style={{ fontSize: 10, color: 'var(--text3)' }}>{e.ip || '—'}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

// FilterClick wraps a cell value as a clickable affordance —
// click → pre-fill the matching filter input above. Same UX
// hint pattern Grafana / Datadog use on tag chips. Lets an
// operator iterate "who else did this" / "what else hit this
// target" without retyping.
function FilterClick({ children, onClick }: { children: React.ReactNode; onClick: () => void }) {
  return (
    <button onClick={onClick}
      style={{
        all: 'unset', cursor: 'pointer',
        color: 'var(--accent2)',
        textDecoration: 'underline dotted',
      }}
      title="Click to filter by this value">
      {children}
    </button>
  );
}

// prettyJSON returns the input as a 2-space indented JSON
// string when it parses, otherwise the raw value. Used by the
// expanded details row so blob payloads ({appName, primaryColor,
// …}) get readable formatting without forcing the operator to
// paste into a separate JSON viewer.
function prettyJSON(s: string): string {
  if (!s) return '';
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
