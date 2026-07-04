import { useEffect, useMemo, useState, FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { Modal, Field, SelectField, Button, Stack } from '@/components/ui';
import { keys, useUsers, useCustomRoles } from '@/lib/queries';
import { api, type UserRow, type CustomRole } from '@/lib/api';
import type { Role } from '@/lib/types';
import { tsLong } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// Columns for the shared sortable + resizable DataTable.
const USER_COLS: DataTableColumn<UserRow>[] = [
  { id: 'email',      label: 'Email',       sortValue: u => u.email,             naturalDir: 'asc',  width: 240 },
  { id: 'role',       label: 'Role',        sortValue: u => u.role,              naturalDir: 'asc',  width: 120 },
  { id: 'customRole', label: 'Custom role', width: 150 },
  { id: 'team',       label: 'Team',        sortValue: u => u.team ?? '',        naturalDir: 'asc',  width: 140 },
  { id: 'provider',   label: 'Provider',    sortValue: u => u.authProvider ?? '', naturalDir: 'asc', width: 110 },
  { id: 'created',    label: 'Created',     sortValue: u => u.createdAt,         naturalDir: 'desc', width: 170 },
  { id: 'actions',    label: 'Actions',     align: 'right', width: 230 },
];

export default function UsersPage() {
  const { user: me } = useAuth();
  const [showNew, setShowNew] = useState(false);
  const [resetFor, setResetFor] = useState<UserRow | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  // undefined = loading, null = fetch failed — the tri-state the
  // render branches below key on. Custom-role catalog drives the
  // per-row picker — fetched alongside the user list, refreshed on
  // every change so a role added in Settings → Roles appears here
  // without a hard reload.
  const usersQ = useUsers();
  const rolesQ = useCustomRoles();
  // Memoized so the tri-state mapping keeps a stable identity for
  // the teamOptions / filteredUsers memos below.
  const users = useMemo<UserRow[] | null | undefined>(
    () => usersQ.isPending ? undefined : usersQ.isError ? null : usersQ.data ?? [],
    [usersQ.isPending, usersQ.isError, usersQ.data]);
  const customRoles: CustomRole[] = rolesQ.data ?? [];
  const qc = useQueryClient();
  const refresh = () => {
    // keys.users.all covers both the list and the role catalog.
    qc.invalidateQueries({ queryKey: keys.users.all });
  };

  // Team filter — distinct non-empty teams from the loaded user list plus a
  // synthetic "Unassigned" bucket; free-text so admins narrow as they type.
  // v0.7.50 — hoisted ABOVE the admin-gate early return below: these hooks were
  // after it, so on a render where `me` resolves null→admin the hook count
  // changed (react-hooks/rules-of-hooks → "rendered fewer hooks" crash). Hooks
  // must run unconditionally.
  const [teamFilter, setTeamFilter] = useState('');
  const teamOptions = useMemo(() => {
    const set = new Set<string>();
    (users ?? []).forEach(u => u.team && set.add(u.team));
    return Array.from(set).sort();
  }, [users]);
  const filteredUsers = useMemo(() => {
    if (!users) return users;
    if (!teamFilter) return users;
    if (teamFilter === '__unassigned__') {
      return users.filter(u => !u.team);
    }
    return users.filter(u => u.team === teamFilter);
  }, [users, teamFilter]);

  // Shared sortable + resizable table. Hook is ABOVE the admin gate
  // (rules-of-hooks — same reason teamFilter/filteredUsers were hoisted
  // in v0.7.50).
  const dt = useDataTable<UserRow>({
    storageKey: 'users', columns: USER_COLS,
    rows: filteredUsers ?? [], initialSort: { id: 'email', dir: 'asc' },
  });

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
          {teamOptions.length > 0 && (
            <select value={teamFilter}
              onChange={e => setTeamFilter(e.target.value)}
              title="Filter by team — pulled from the active users' team labels"
              style={{ minWidth: 160 }}>
              <option value="">All teams ({teamOptions.length})</option>
              <option value="__unassigned__">Unassigned</option>
              {teamOptions.map(t => <option key={t} value={t}>{t}</option>)}
            </select>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {filteredUsers?.length ?? 0}
            {teamFilter && users && filteredUsers && filteredUsers.length !== users.length
              ? ` of ${users.length}` : ''} users
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
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(u => {
                  const isMe = me?.id === u.id;
                  const isOIDC = u.authProvider === 'oidc';
                  return (
                    <tr key={u.id}>
                      <td>
                        {/* v0.8.238 — LDAP photo avatar; initials chip
                            fallback keeps rows aligned. */}
                        {u.hasPhoto ? (
                          <img src={`/api/users/${u.id}/photo`} alt=""
                            style={{
                              width: 20, height: 20, borderRadius: '50%',
                              objectFit: 'cover', verticalAlign: 'middle', marginRight: 8,
                            }}
                            onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                        ) : (
                          <span style={{
                            display: 'inline-grid', placeItems: 'center',
                            width: 20, height: 20, borderRadius: '50%',
                            background: 'var(--accent)', color: '#fff',
                            fontSize: 10, fontWeight: 600, textTransform: 'uppercase',
                            verticalAlign: 'middle', marginRight: 8,
                          }}>{u.email[0]}</span>
                        )}
                        <span style={{ fontWeight: 600 }}>{u.email}</span>
                        {isMe && (
                          <span style={{
                            marginLeft: 8, fontSize: 10, color: 'var(--text3)',
                            border: '1px solid var(--border)', borderRadius: 3,
                            padding: '1px 5px', textTransform: 'uppercase',
                          }}>you</span>
                        )}
                        {/* v0.8.266 — directory identity line: full
                            name + organization from LDAP, refreshed
                            on each directory login. */}
                        {(u.fullName || u.org) && (
                          <div style={{
                            fontSize: 11, color: 'var(--text3)', marginLeft: 28,
                            overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                          }} title={[u.fullName, u.org].filter(Boolean).join(' · ')}>
                            {[u.fullName, u.org].filter(Boolean).join(' · ')}
                          </div>
                        )}
                      </td>
                      <td>
                        <RoleEditor user={u} isMe={isMe} onChanged={refresh} />
                      </td>
                      <td>
                        <CustomRoleEditor user={u} catalog={customRoles} onChanged={refresh} />
                      </td>
                      <td>
                        <TeamEditor user={u} suggestions={teamOptions} onChanged={refresh} />
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
  const [role, setRole] = useState<'admin' | 'editor' | 'viewer'>('viewer');
  const [team, setTeam] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await api.createUser(email.trim(), password, role, team.trim());
      onCreated();
    } catch (err) {
      setError(humanize(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={true}
      onClose={onClose}
      title="New user"
      size="sm"
      initialFocus="input[type=email]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-user-form" loading={busy}>Create</Button>
        </>
      }>
      <form id="new-user-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="Email"
            type="email"
            required
            value={email}
            onChange={e => setEmail(e.target.value)} />
          <Field
            label="Password"
            hint="At least 6 characters."
            type="password"
            required
            minLength={6}
            value={password}
            onChange={e => setPassword(e.target.value)} />
          <SelectField
            label="Role"
            value={role}
            onChange={e => setRole(e.target.value as 'admin' | 'editor' | 'viewer')}>
            <option value="viewer">Viewer (read only)</option>
            <option value="editor">Editor (dashboards / monitors / alerts)</option>
            <option value="admin">Admin (full access)</option>
          </SelectField>
          <Field
            label="Team (optional)"
            hint="Free-text grouping for the user list — e.g. platform-sre, fraud, payments."
            value={team}
            onChange={e => setTeam(e.target.value)} />
          {error && <ErrorBox>{error}</ErrorBox>}
        </Stack>
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
    <Modal
      open={true}
      onClose={onClose}
      title={`Reset password — ${user.email}`}
      size="sm"
      initialFocus="input[type=password]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="reset-pw-form" loading={busy}>Set password</Button>
        </>
      }>
      <form id="reset-pw-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="New password"
            hint="At least 6 characters."
            type="password"
            required
            minLength={6}
            value={password}
            onChange={e => setPassword(e.target.value)} />
          {error && <ErrorBox>{error}</ErrorBox>}
        </Stack>
      </form>
    </Modal>
  );
}

// ErrorBox is the inline form-level error styling — kept as a
// local helper because it's used in two places in this file and
// the global Field error slot only covers per-field errors. If a
// third caller in another page wants the same look, lift this
// to ui/.
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

// RoleEditor renders a small inline role <select> with confirm-
// on-change. The previous "static badge" UX meant admins had to
// delete + recreate a user to change a role; the typical bank
// onboarding flow is "viewer first, promote to editor / admin
// later" so this turned into a routine annoyance.
//
// Last-admin and self-edit cases are gated server-side; here we
// just surface the API error verbatim in an alert. Confirm step
// kept short so a misclick on the dropdown doesn't immediately
// silently demote someone.
function RoleEditor({ user, isMe, onChanged }: {
  user: UserRow;
  isMe: boolean;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);

  const apply = async (next: Role) => {
    if (next === user.role) return;
    const ok = confirm(
      `Change ${user.email}'s role from ${user.role} to ${next}?` +
      (next === 'admin' ? '\n\nAdmins can manage users, settings, and every CRUD surface.'
       : next === 'editor' ? '\n\nEditors can manage dashboards / monitors / alerts but not users or system settings.'
       : '\n\nViewers are read-only.')
    );
    if (!ok) return;
    setBusy(true);
    try {
      await api.setUserRole(user.id, next);
      onChanged();
    } catch (err) {
      alert(humanize(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      <select value={user.role} disabled={busy}
        onChange={e => apply(e.target.value as Role)}
        style={{ fontSize: 11, padding: '2px 6px', minWidth: 90,
                 fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                 fontWeight: 600 }}>
        <option value="admin">admin</option>
        <option value="editor">editor</option>
        <option value="viewer">viewer</option>
      </select>
      {isMe && (
        <span style={{ fontSize: 10, color: 'var(--text3)',
                       padding: '1px 5px', borderRadius: 3,
                       border: '1px solid var(--border)' }}
              title="You'll lock yourself out of this page if you demote yourself away from admin">
          self
        </span>
      )}
    </span>
  );
}

// CustomRoleEditor renders the per-user custom-role pointer. Only
// surfaces when the base role is viewer; admin/editor get a hyphen
// because custom roles cannot further restrict their access. Empty
// catalog also hides the picker — there's nothing to pick. The
// dropdown's first option is the "no custom role" sentinel; picking
// it clears the pointer server-side via api.setUserCustomRole('').
function CustomRoleEditor({ user, catalog, onChanged }: {
  user: UserRow;
  catalog: CustomRole[];
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);

  if (user.role !== 'viewer') {
    return <span style={{ color: 'var(--text3)' }}>—</span>;
  }
  if (catalog.length === 0) {
    return (
      <span style={{ fontSize: 11, color: 'var(--text3)' }}
            title="No custom roles defined yet — create one in Settings → Custom roles">
        (none defined)
      </span>
    );
  }

  const apply = async (next: string) => {
    if (next === (user.customRole ?? '')) return;
    setBusy(true);
    try {
      await api.setUserCustomRole(user.id, next);
      onChanged();
    } catch (err) {
      alert(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <select value={user.customRole ?? ''} disabled={busy}
      onChange={e => apply(e.target.value)}
      style={{ fontSize: 11, padding: '2px 6px', minWidth: 130,
               fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}
      title="Pick a custom role to restrict this viewer to a subset of pages">
      <option value="">— unrestricted —</option>
      {catalog.map(r => (
        <option key={r.name} value={r.name}>
          {r.name} ({r.pages.length} page{r.pages.length === 1 ? '' : 's'})
        </option>
      ))}
    </select>
  );
}

// TeamEditor — inline team-label editor with autocomplete
// against existing team values. Click the chip to edit;
// commit on blur or Enter. Empty value clears the team
// (back to "Unassigned"). datalist suggestions help admins
// pick a consistent team name across users rather than
// fat-fingering variants of the same team.
function TeamEditor({ user, suggestions, onChanged }: {
  user: UserRow;
  suggestions: string[];
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(user.team ?? '');
  const [busy, setBusy] = useState(false);
  const dlId = `team-opts-${user.id}`;

  // Keep value in sync if the row's team changes via another
  // path (e.g. refresh after sibling edit).
  useEffect(() => { setValue(user.team ?? ''); }, [user.team]);

  const commit = async () => {
    const next = value.trim();
    if (next === (user.team ?? '')) {
      setEditing(false);
      return;
    }
    setBusy(true);
    try {
      await api.setUserTeam(user.id, next);
      onChanged();
    } catch (err) {
      alert(humanize(err));
      setValue(user.team ?? '');
    } finally {
      setBusy(false);
      setEditing(false);
    }
  };

  if (!editing) {
    if (!user.team) {
      return (
        <button type="button" onClick={() => setEditing(true)}
          style={{
            all: 'unset', cursor: 'pointer',
            fontSize: 10, color: 'var(--text3)',
            border: '1px dashed var(--border)', borderRadius: 3,
            padding: '1px 6px',
          }}
          title="Assign a team">
          + assign team
        </button>
      );
    }
    return (
      <button type="button" onClick={() => setEditing(true)}
        style={{
          all: 'unset', cursor: 'pointer',
          fontSize: 11, fontWeight: 600,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          color: 'var(--accent2)',
          background: 'rgba(56,139,253,0.10)',
          border: '1px solid rgba(56,139,253,0.30)',
          borderRadius: 3, padding: '1px 8px',
        }}
        title="Click to edit team">
        {user.team}
      </button>
    );
  }

  return (
    <>
      <input value={value}
        list={dlId}
        autoFocus
        disabled={busy}
        onChange={e => setValue(e.target.value)}
        onBlur={commit}
        onKeyDown={e => {
          if (e.key === 'Enter') commit();
          if (e.key === 'Escape') { setValue(user.team ?? ''); setEditing(false); }
        }}
        placeholder="team name"
        style={{
          fontSize: 11, padding: '2px 6px', minWidth: 120,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }} />
      <datalist id={dlId}>
        {suggestions.map(t => <option key={t} value={t} />)}
      </datalist>
    </>
  );
}
