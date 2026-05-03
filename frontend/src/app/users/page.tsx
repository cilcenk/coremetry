'use client';
import { useEffect, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api, type UserRow } from '@/lib/api';
import { tsLong } from '@/lib/utils';

export default function UsersPage() {
  const { user: me } = useAuth();
  const [users, setUsers] = useState<UserRow[] | null | undefined>(undefined);
  const [showNew, setShowNew] = useState(false);
  const [resetFor, setResetFor] = useState<UserRow | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const refresh = () => {
    setUsers(undefined);
    api.listUsers().then(u => setUsers(u ?? [])).catch(() => setUsers(null));
  };
  useEffect(refresh, []);

  // Admin gate: if a viewer somehow lands here, surface a clear message
  // rather than a stack of 401s from listUsers.
  if (me && me.role !== 'admin') {
    return (
      <>
        <Topbar title="Users" />
        <div id="content">
          <Empty icon="🔒" title="Admin access required">
            User management is only available to administrators.
          </Empty>
        </div>
      </>
    );
  }

  const onDelete = async (u: UserRow) => {
    setActionError(null);
    if (!confirm(`Disable user ${u.email}? They will no longer be able to sign in.`)) return;
    try {
      await api.deleteUser(u.id);
      refresh();
    } catch (e) {
      setActionError(humanize(e));
    }
  };

  return (
    <>
      <Topbar title="Users" />
      <div id="content">
        <div className="controls">
          <button onClick={() => setShowNew(true)}>+ New user</button>
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {users?.length ?? 0} users
          </span>
        </div>

        {actionError && (
          <div style={{
            color: 'var(--err)', fontSize: 13, marginBottom: 10,
            padding: '6px 10px', background: 'rgba(220,38,38,0.08)',
            border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
          }}>
            {actionError}
          </div>
        )}

        {users === undefined && <Spinner />}
        {users !== undefined && (!users || users.length === 0) && (
          <Empty icon="◯" title="No users yet">
            Create the first user to get started.
          </Empty>
        )}
        {users && users.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Email</th>
                  <th>Role</th>
                  <th>Provider</th>
                  <th>Created</th>
                  <th style={{ textAlign: 'right' }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {users.map(u => {
                  const isMe = me?.id === u.id;
                  const isOIDC = u.authProvider === 'oidc';
                  return (
                    <tr key={u.id}>
                      <td>
                        <span style={{ fontWeight: 600 }}>{u.email}</span>
                        {isMe && (
                          <span style={{
                            marginLeft: 8, fontSize: 10, color: 'var(--text3)',
                            border: '1px solid var(--border)', borderRadius: 3,
                            padding: '1px 5px', textTransform: 'uppercase',
                          }}>you</span>
                        )}
                      </td>
                      <td>
                        <span className={`badge ${u.role === 'admin' ? 'b-info' : 'b-ok'}`}>
                          {u.role}
                        </span>
                      </td>
                      <td>
                        <span style={{
                          fontSize: 10, color: 'var(--text3)',
                          border: '1px solid var(--border)', borderRadius: 3,
                          padding: '1px 6px', textTransform: 'uppercase',
                        }}>{u.authProvider || 'local'}</span>
                      </td>
                      <td className="mono" style={{ color: 'var(--text3)' }}>
                        {tsLong(u.createdAt)}
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        <button className="sec" onClick={() => setResetFor(u)}
                          disabled={isOIDC}
                          title={isOIDC ? 'OIDC users authenticate via SSO — no local password' : 'Set a new password'}
                          style={{ marginRight: 6 }}>
                          Reset password
                        </button>
                        <button className="sec" onClick={() => onDelete(u)}
                          disabled={isMe}
                          title={isMe ? "You can't delete your own account" : 'Disable user'}>
                          Delete
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {showNew && (
          <NewUserModal
            onClose={() => setShowNew(false)}
            onCreated={() => { setShowNew(false); refresh(); }}
          />
        )}
        {resetFor && (
          <ResetPasswordModal
            user={resetFor}
            onClose={() => setResetFor(null)}
            onDone={() => { setResetFor(null); refresh(); }}
          />
        )}
      </div>
    </>
  );
}

function NewUserModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<'admin' | 'viewer'>('viewer');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await api.createUser(email.trim(), password, role);
      onCreated();
    } catch (err) {
      setError(humanize(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="New user" onClose={onClose}>
      <form onSubmit={submit}>
        <Field label="Email">
          <input type="email" required autoFocus value={email}
            onChange={e => setEmail(e.target.value)} style={{ width: '100%' }} />
        </Field>
        <Field label="Password (min 6 chars)">
          <input type="password" required minLength={6} value={password}
            onChange={e => setPassword(e.target.value)} style={{ width: '100%' }} />
        </Field>
        <Field label="Role">
          <select value={role} onChange={e => setRole(e.target.value as 'admin' | 'viewer')}>
            <option value="viewer">Viewer (read only)</option>
            <option value="admin">Admin (full access)</option>
          </select>
        </Field>
        {error && <ErrorBox>{error}</ErrorBox>}
        <ModalActions>
          <button type="button" className="sec" onClick={onClose}>Cancel</button>
          <button type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create'}</button>
        </ModalActions>
      </form>
    </Modal>
  );
}

function ResetPasswordModal({ user, onClose, onDone }: {
  user: UserRow; onClose: () => void; onDone: () => void;
}) {
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await api.resetUserPassword(user.id, password);
      onDone();
    } catch (err) {
      setError(humanize(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={`Reset password — ${user.email}`} onClose={onClose}>
      <form onSubmit={submit}>
        <Field label="New password (min 6 chars)">
          <input type="password" required minLength={6} autoFocus value={password}
            onChange={e => setPassword(e.target.value)} style={{ width: '100%' }} />
        </Field>
        {error && <ErrorBox>{error}</ErrorBox>}
        <ModalActions>
          <button type="button" className="sec" onClick={onClose}>Cancel</button>
          <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Set password'}</button>
        </ModalActions>
      </form>
    </Modal>
  );
}

// ── Tiny modal primitives (kept inline to avoid new component files) ────────

function Modal({ title, children, onClose }: {
  title: string; children: React.ReactNode; onClose: () => void;
}) {
  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 380, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        boxShadow: '0 12px 36px rgba(0,0,0,0.3)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>{title}</div>
        {children}
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 12 }}>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

function ModalActions({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 18 }}>
      {children}
    </div>
  );
}

function ErrorBox({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      color: 'var(--err)', fontSize: 12, marginTop: 6,
      padding: '6px 10px', background: 'rgba(220,38,38,0.08)',
      border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
    }}>
      {children}
    </div>
  );
}

function humanize(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  // Strip "HTTP 4xx: " prefix and try to pull a JSON {"error":"..."} body.
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') return j.error;
  } catch {}
  return body || msg;
}
