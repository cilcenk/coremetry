'use client';
import { useEffect, useState, useMemo } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { AuditEntry } from '@/lib/types';

// /admin/audit shows the append-only audit_log: who did what, when.
// Admin-only — the API also enforces this server-side, but the SPA
// hides the page from non-admin sidebars.
export default function AuditPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [data, setData] = useState<AuditEntry[] | null | undefined>(undefined);
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [target, setTarget] = useState('');
  const [since, setSince] = useState('24h');

  useEffect(() => {
    if (!isAdmin) return;
    setData(undefined);
    api.auditLog(since, {
      actor: actor.trim() || undefined,
      action: action.trim() || undefined,
      target: target.trim() || undefined,
    }).then(d => setData(d ?? [])).catch(() => setData(null));
  }, [isAdmin, since, actor, action, target]);

  const distinctActions = useMemo(() => {
    const s = new Set<string>();
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
