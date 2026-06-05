import { useState, type FormEvent } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { LDAPDirectoryUser, Role } from '@/lib/types';

// LDAPUserPicker — admin types a name/email, hits Search, picks a
// directory entry, picks a role, hits Provision. Pre-creates the
// users row so first-login lands them with the right access without
// having to wait for the group mapping to apply.
export function LDAPUserPicker() {
  const [q, setQ] = useState('');
  const [results, setResults] = useState<LDAPDirectoryUser[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [provisionFor, setProvisionFor] = useState<LDAPDirectoryUser | null>(null);
  const [role, setRole] = useState<Role>('viewer');
  const [provisionMsg, setProvisionMsg] = useState<string | null>(null);

  const search = async (e?: FormEvent) => {
    if (e) e.preventDefault();
    setBusy(true); setError(null); setResults(null);
    try {
      const r = await api.searchLDAPUsers(q, 25);
      setResults(r.users ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed');
    } finally {
      setBusy(false);
    }
  };

  const provision = async () => {
    if (!provisionFor) return;
    const email = provisionFor.email || provisionFor.username;
    if (!email) return;
    setBusy(true); setProvisionMsg(null);
    try {
      await api.provisionLDAPUser(email, role);
      setProvisionMsg(`Provisioned ${email} as ${role}.`);
      setProvisionFor(null);
    } catch (err) {
      setProvisionMsg(err instanceof Error ? err.message : 'Provision failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{
      marginTop: 18, padding: 16, borderRadius: 8,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 6 }}>Pre-provision a directory user</h3>
      <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 12 }}>
        Find a user in your LDAP, pick a role, and we'll create their Coremetry
        row up-front. They keep that role even if their AD groups would map them
        to a different one — useful for "trust this person specifically" cases.
      </p>
      <form onSubmit={search} style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
        <input value={q} onChange={e => setQ(e.target.value)}
               placeholder="Name, email or username" style={{ flex: 1 }} />
        <Button type="submit" variant="primary" disabled={busy}>{busy ? 'Searching…' : 'Search'}</Button>
      </form>
      {error && (
        <div style={{ color: 'var(--err)', fontSize: 12, marginBottom: 8 }}>{error}</div>
      )}
      {results && results.length === 0 && (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>No matches.</div>
      )}
      {results && results.length > 0 && (
        <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ background: 'var(--bg)', color: 'var(--text2)' }}>
              <th style={{ padding: 6, textAlign: 'left' }}>Name</th>
              <th style={{ padding: 6, textAlign: 'left' }}>Username</th>
              <th style={{ padding: 6, textAlign: 'left' }}>Email</th>
              <th style={{ padding: 6, textAlign: 'right', width: 100 }}></th>
            </tr>
          </thead>
          <tbody>
            {results.map(u => (
              <tr key={u.dn} style={{ borderTop: '1px solid var(--border)' }}>
                <td style={{ padding: 6 }}>{u.displayName || '—'}</td>
                <td style={{ padding: 6 }}><code>{u.username}</code></td>
                <td style={{ padding: 6 }}>{u.email || '—'}</td>
                <td style={{ padding: 6, textAlign: 'right' }}>
                  <Button variant="secondary" size="sm" type="button"
                          onClick={() => setProvisionFor(u)}>
                    Provision
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {provisionFor && (
        <div onClick={() => setProvisionFor(null)} style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
          display: 'grid', placeItems: 'center', zIndex: 100,
        }}>
          <div onClick={e => e.stopPropagation()} style={{
            width: 380, padding: 20, borderRadius: 8,
            background: 'var(--bg2)', border: '1px solid var(--border)',
          }}>
            <div style={{ fontWeight: 600, marginBottom: 10 }}>Provision LDAP user</div>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 12 }}>
              <div>{provisionFor.displayName || provisionFor.username}</div>
              <div style={{ color: 'var(--text3)' }}>{provisionFor.email || provisionFor.dn}</div>
            </div>
            <label style={{ display: 'block', marginBottom: 14 }}>
              <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Role</div>
              <select value={role} onChange={e => setRole(e.target.value as Role)}
                      style={{ width: '100%' }}>
                <option value="viewer">Viewer (read only)</option>
                <option value="editor">Editor (dashboards / monitors / alerts)</option>
                <option value="admin">Admin (full access)</option>
              </select>
            </label>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
              <Button type="button" variant="secondary" onClick={() => setProvisionFor(null)}>Cancel</Button>
              <Button type="button" variant="primary" onClick={provision} disabled={busy}>
                {busy ? 'Saving…' : 'Provision'}
              </Button>
            </div>
          </div>
        </div>
      )}
      {provisionMsg && (
        <div style={{ marginTop: 10, fontSize: 12, color: 'var(--ok)' }}>{provisionMsg}</div>
      )}
    </div>
  );
}
