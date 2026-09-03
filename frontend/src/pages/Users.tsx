import { useEffect, useMemo, useState, FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { QueryError } from '@/components/QueryError';
import { readState } from '@/lib/readState';
import { useAuth } from '@/components/AuthProvider';
import { Button, Chip, Field, Modal, SelectField, Stack, useConfirm } from '@/components/ui';
import { keys, useUsers, useCustomRoles } from '@/lib/queries';
import { api, type UserRow, type CustomRole } from '@/lib/api';
import type { Role } from '@/lib/types';
import { tsLong, tsMinute, tsRel } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import { USER_COLS } from './usersColumns';
import { PageControls } from '@/components/ui/PageControls';
import { PageShell } from '@/components/ui/PageShell';
import { Combobox } from '@/components/Combobox';


export default function UsersPage() {
  const confirm = useConfirm();
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

  // v0.8.403 — header "N online" chip. Plain derivation (no hook) so
  // the admin-gate early return below can't trip rules-of-hooks.
  const onlineCount = (users ?? []).filter(u => u.online).length;

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
        <PageShell>
          <Empty icon="🔒" title="Admin access required">
            User management is only available to administrators.
          </Empty>
        </PageShell>
      </>
    );
  }

  const onDelete = async (u: UserRow) => {
    setActionError(null);
    if (!await confirm({
      title: 'Kullanıcı devre dışı bırakılsın mı?',
      body: <><b>{u.email}</b> artık oturum açamayacak. Mevcut oturumları
        bir sonraki token yenilemesinde düşer.</>,
      confirmLabel: 'Kullanıcıyı kapat',
      danger: true,
    })) return;
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
      <PageShell>
        <PageControls sticky>
          <Button variant="primary" onClick={() => setShowNew(true)}>+ New user</Button>
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
          <ResetLayoutButton dt={dt} />
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 10, marginLeft: 'auto' }}>
            {/* v0.8.403 — presence: count over ALL loaded users (not the
                team-filtered slice) so the header reads as a page-level
                "who's on Coremetry right now". */}
            {onlineCount > 0 && (
              <span className="badge b-ok"
                title="Users with authenticated API activity in the last 5 minutes">
                ● {onlineCount} online
              </span>
            )}
            <span style={{ color: 'var(--text3)', fontSize: 12 }}>
              {filteredUsers?.length ?? 0}
              {teamFilter && users && filteredUsers && filteredUsers.length !== users.length
                ? ` of ${users.length}` : ''} users
            </span>
          </span>
        </PageControls>

        {actionError && (
          <div style={{
            color: 'var(--err)', fontSize: 13, marginBottom: 10,
            padding: '6px 10px', background: 'color-mix(in srgb, var(--err) 8%, transparent)',
            border: '1px solid color-mix(in srgb, var(--err) 30%, transparent)', borderRadius: 4,
          }}>
            {actionError}
          </div>
        )}

        {readState(users) === 'loading' && <Spinner />}
        {/* v0.9.865 (tutarlılık denetimi MT1) — `!users` null için de doğru
            olduğundan okuma hatası "No users yet / create the first user"
            oluyordu: dolu bir LDAP filosu boş kullanıcı tabanı gibi
            okunuyor, operatör var olanın üstüne kullanıcı kurmaya
            yönlendiriliyordu. */}
        {readState(users) === 'error' && (
          <QueryError onRetry={() => usersQ.refetch()}>
            Users could not be loaded — this is a failed read, not an empty
            directory. Existing accounts are unaffected.
          </QueryError>
        )}
        {readState(users) === 'empty' && (
          <Empty icon="◯" title="No users yet">
            Create the first user to get started.
          </Empty>
        )}
        {users && users.length > 0 && (
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(u => {
                  const isMe = me?.id === u.id;
                  const isOIDC = u.authProvider === 'oidc';
                  return (
                    <tr key={u.id}
                      style={users.length > 100 ? { contentVisibility: 'auto', containIntrinsicSize: 'auto 44px' } : undefined}>
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
                            background: 'var(--accent)', color: 'var(--on-accent)',
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
                        <span className="badge b-gray" style={{ textTransform: 'uppercase' }}>{u.authProvider || 'local'}</span>
                      </td>
                      <td>
                        {/* v0.8.403 — presence. Online = authenticated API
                            activity in the last 5 min (open tabs poll, so
                            logged-in ≈ online). Stamp expires with the
                            window, so offline rows show the last relative
                            sighting only while it's still fresh, else "—". */}
                        {u.online ? (
                          <span className="badge b-ok"
                            title="Authenticated API activity in the last 5 minutes">
                            ● online
                          </span>
                        ) : u.lastSeenAt ? (
                          <span style={{ color: 'var(--text3)', fontSize: 12 }}
                            title={tsLong(u.lastSeenAt)}>
                            {tsRel(u.lastSeenAt)}
                          </span>
                        ) : (
                          <span style={{ color: 'var(--text3)' }}
                            title="No authenticated activity seen (or presence unavailable — requires Redis)">—</span>
                        )}
                      </td>
                      <td>
                        {u.lastLoginAt ? (
                          <span className="mono" style={{ color: 'var(--text2)', fontSize: 12 }}
                            title={tsLong(u.lastLoginAt)}>
                            {tsRel(u.lastLoginAt)}
                          </span>
                        ) : (
                          <span style={{ color: 'var(--text3)' }}
                            title="Bu sürümden beri hiç giriş yapmadı">—</span>
                        )}
                      </td>
                      <td className="mono" style={{ color: 'var(--text3)' }}
                        title={tsLong(u.createdAt)}>
                        {/* Saniyesiz — bir hesabın oluşturulma saniyesi
                            25px kolon genişliğine değmiyor ve o 25px
                            doğrudan taşmaya gidiyordu (v0.9.660). Tam
                            damga title'da. */}
                        {tsMinute(u.createdAt)}
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        <Button variant="secondary" onClick={() => setResetFor(u)}
                          disabled={isOIDC}
                          title={isOIDC ? 'OIDC users authenticate via SSO — no local password' : 'Set a new password'}
                          style={{ marginRight: 6 }}>
                          Reset password
                        </Button>
                        <Button variant="ghost-danger" onClick={() => void onDelete(u)}
                          disabled={isMe}
                          title={isMe ? "You can't delete your own account" : 'Disable user'}>
                          Delete
                        </Button>
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
      </PageShell>
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
          <Button variant="primary" type="submit" form="new-user-form" loading={busy}>Create</Button>
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
          <Button variant="primary" type="submit" form="reset-pw-form" loading={busy}>Set password</Button>
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
      padding: '6px 10px', background: 'color-mix(in srgb, var(--err) 8%, transparent)',
      border: '1px solid color-mix(in srgb, var(--err) 30%, transparent)', borderRadius: 4,
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
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);

  const apply = async (next: Role) => {
    if (next === user.role) return;
    const ok = await confirm({
      title: 'Rol değiştirilsin mi?',
      body: <>
        <b>{user.email}</b> — <code>{user.role}</code> → <code>{next}</code>.
        {next === 'admin'
          ? ' Admin kullanıcıları, ayarları ve tüm CRUD yüzeylerini yönetebilir.'
          : next === 'editor'
            ? ' Editor pano / monitör / alert yönetebilir; kullanıcılara ve sistem ayarlarına dokunamaz.'
            : ' Viewer salt-okunurdur.'}
      </>,
      confirmLabel: `${next} yap`,
      danger: next === 'admin',
    });
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
        <Chip size="xs" className="ch-dashed" onClick={() => setEditing(true)}
          title="Assign a team">
          + assign team
        </Chip>
      );
    }
    return (
      <Chip size="xs" tone="accent" className="mono" onClick={() => setEditing(true)}
        title="Click to edit team">
        {user.team}
      </Chip>
    );
  }

  return (
    // v0.9.1023 — native <datalist> → ev Combobox'ı. Datalist'in
    // açılır listesi TARAYICIYA bırakılmış bir sözleşmedir ve
    // tarayıcılar aynı fikirde değil (Safari'de ok tuşları listeyi
    // açmıyor bile) — yani "takımı klavyeyle seçebiliyorum" iddiası
    // operatörün tarayıcısına göre doğru ya da yanlıştı.
    // Sarmalayıcı span yalnız CSS kancası: ölçüler globals.css'te
    // `.team-edit .cb-wrap input` altında, satır yüksekliği aynı kalsın
    // diye (eski satır içi style bloğunun birebir karşılığı).
    <span className="team-edit">
      <Combobox
        value={value}
        onChange={setValue}
        options={suggestions}
        placeholder="team name"
        autoFocus
        disabled={busy}
        onEnter={commit}
        // Blur'da commit + Esc'te İPTAL: eski davranışın aynısı, ama
        // artık atomun sözleşmesi (v0.9.1022). Esc önce değeri geri
        // alır, sonra düzenleme modundan çıkar — ve atom Esc'ten sonra
        // gelen blur'un commit etmesini bastırır.
        onBlurCommit={() => { void commit(); }}
        onEscape={() => { setValue(user.team ?? ''); setEditing(false); }}
      />
    </span>
  );
}
