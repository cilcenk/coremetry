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
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {data?.length ?? 0} entries
          </span>
        </div>

        {data === undefined && <Spinner />}
        {data !== undefined && data?.length === 0 && (
          <Empty icon="◇" title="No audit entries in this range" />
        )}
        {data && data.length > 0 && (
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
                {data.map(e => (
                  <tr key={e.id}>
                    <td className="mono" style={{ fontSize: 11, whiteSpace: 'nowrap' }}>
                      {tsLong(e.time)}
                    </td>
                    <td>
                      <div style={{ fontWeight: 600, fontSize: 12 }}>{e.actorEmail || '—'}</div>
                      <div style={{ fontSize: 10, color: 'var(--text3)' }}>{e.actorRole}</div>
                    </td>
                    <td className="mono" style={{ fontSize: 11 }}>{e.action}</td>
                    <td className="mono" style={{ fontSize: 11 }}>
                      {e.targetKind}
                      {e.targetId && <span style={{ color: 'var(--text3)' }}> · {e.targetId}</span>}
                    </td>
                    <td style={{ fontSize: 11, color: 'var(--text2)',
                                 maxWidth: 360, overflow: 'hidden',
                                 textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                                 fontFamily: 'monospace' }}
                        title={e.details}>
                      {e.details || '—'}
                    </td>
                    <td className="mono" style={{ fontSize: 10, color: 'var(--text3)' }}>{e.ip || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
