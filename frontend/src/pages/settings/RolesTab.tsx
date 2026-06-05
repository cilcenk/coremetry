import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Modal, Button, Stack } from '@/components/ui';
import { api, type CustomRole, type AvailablePage } from '@/lib/api';

// ── Custom roles tab ────────────────────────────────────────────────────────
//
// Operator-defined subsets of viewer's page access. Each role names a
// set of sidebar paths the user is allowed to see; the frontend
// filters the sidebar + redirects direct-URL access via AppShell's
// custom-role guard. Custom roles ONLY apply when the user's base
// role is viewer — admin/editor get no further restriction.
//
// Page catalogue is sourced from /api/admin/pages so the checkbox grid
// stays in sync with the backend's canonical sidebar registry. A new
// page lands in the sidebar → it appears here automatically on next
// load (default-unchecked, so new features stay hidden until an admin
// opts them in).
export function CustomRolesTab() {
  const [roles, setRoles] = useState<CustomRole[] | null | undefined>(undefined);
  const [pages, setPages] = useState<AvailablePage[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<CustomRole | null>(null);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    setRoles(undefined);
    Promise.all([api.listCustomRoles(), api.listAvailablePages()])
      .then(([r, p]) => {
        setRoles(r.roles ?? []);
        setPages(p.pages ?? []);
      })
      .catch(() => { setRoles(null); setPages(null); });
  };
  useEffect(load, []);

  const remove = async (name: string) => {
    if (!confirm(`Delete custom role "${name}"? Users assigned to this role will fall back to unrestricted viewer.`)) return;
    setBusy(name);
    setMsg(null);
    try {
      await api.deleteCustomRole(name);
      setMsg({ kind: 'ok', text: `Deleted "${name}"` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(null);
    }
  };

  if (roles === undefined || pages === undefined) return <Spinner />;
  if (roles === null || pages === null) {
    return <Empty icon="!" title="Failed to load custom roles">Reload the page.</Empty>;
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 12 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
          Custom roles subset the <b>viewer</b> base role to a chosen set of
          pages — e.g. a "readonly-3" that only sees traces, metrics, logs.
          Admin / editor roles are unaffected.
        </span>
        <Button onClick={() => setCreating(true)}>+ New role</Button>
      </div>

      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4,
          fontSize: 13,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'rgba(34,197,94,0.08)' : 'rgba(220,38,38,0.08)',
          border: `1px solid ${msg.kind === 'ok' ? 'rgba(34,197,94,0.3)' : 'rgba(220,38,38,0.3)'}`,
        }}>{msg.text}</div>
      )}

      {roles.length === 0 ? (
        <Empty icon="◇" title="No custom roles yet">
          Create one to give a viewer access to only a subset of pages.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Pages</th>
                <th style={{ textAlign: 'right' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {roles.map(r => (
                <tr key={r.name}>
                  <td style={{ fontWeight: 600 }}>{r.name}</td>
                  <td style={{ fontSize: 12, color: 'var(--text2)' }}>
                    {r.pages.length === 0
                      ? <span style={{ color: 'var(--err)' }}>(none — user will see no nav)</span>
                      : r.pages.join(', ')}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="secondary" size="sm" onClick={() => setEditing(r)} style={{ marginRight: 6 }}>
                      Edit
                    </Button>
                    <Button variant="secondary" size="sm" onClick={() => remove(r.name)} disabled={busy === r.name}>
                      Delete
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <RoleEditorModal
          existing={editing}
          pages={pages}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={() => { setCreating(false); setEditing(null); load(); }}
        />
      )}
    </div>
  );
}

function RoleEditorModal({ existing, pages, onClose, onSaved }: {
  existing: CustomRole | null;
  pages: AvailablePage[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(existing?.name ?? '');
  const [selected, setSelected] = useState<Set<string>>(() => new Set(existing?.pages ?? []));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Group pages by their group key so the checkbox grid mirrors
  // the sidebar's grouping — easier to scan than a flat list.
  const byGroup = useMemo(() => {
    const m = new Map<string, AvailablePage[]>();
    for (const p of pages) {
      const k = p.group || '_ungrouped';
      const arr = m.get(k) ?? [];
      arr.push(p);
      m.set(k, arr);
    }
    return m;
  }, [pages]);

  const toggle = (id: string) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await api.upsertCustomRole({ name: name.trim(), pages: [...selected] });
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={existing ? `Edit role — ${existing.name}` : 'New custom role'}
      size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="role-form" loading={busy}>Save</Button>
        </>
      }>
      <form id="role-form" onSubmit={submit}>
        <Stack gap={3}>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              Role name
            </label>
            <input
              value={name}
              onChange={e => setName(e.target.value)}
              required
              disabled={!!existing}
              style={{ width: '100%' }}
              placeholder="e.g. readonly-3, sre-readonly, audit-only" />
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
              Cannot be admin/editor/viewer.
            </div>
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 6 }}>
              Pages this role can see ({selected.size} selected)
            </div>
            <div style={{
              border: '1px solid var(--border)', borderRadius: 4,
              padding: 10, maxHeight: 320, overflowY: 'auto',
            }}>
              {[...byGroup.entries()].map(([g, items]) => (
                <div key={g} style={{ marginBottom: 8 }}>
                  {g !== '_ungrouped' && (
                    <div style={{
                      fontSize: 10, fontWeight: 700, color: 'var(--text3)',
                      textTransform: 'uppercase', letterSpacing: 0.6,
                      marginBottom: 4,
                    }}>{g.replace('navGroup.', '')}</div>
                  )}
                  {items.map(p => (
                    <label key={p.id} style={{
                      display: 'flex', alignItems: 'center', gap: 8,
                      padding: '3px 4px', fontSize: 13, cursor: 'pointer',
                    }}>
                      <input type="checkbox"
                        checked={selected.has(p.id)}
                        onChange={() => toggle(p.id)} />
                      <code style={{ fontSize: 11, color: 'var(--text3)' }}>{p.id}</code>
                      <span style={{ color: 'var(--text)' }}>
                        {p.label.replace('nav.', '')}
                      </span>
                    </label>
                  ))}
                </div>
              ))}
            </div>
          </div>
          {error && (
            <div style={{
              color: 'var(--err)', fontSize: 12,
              padding: '4px 8px', background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
