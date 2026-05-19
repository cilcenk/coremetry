import { useEffect, useMemo, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api, type MaintenanceWindow, type CustomRole, type AvailablePage, type PipelineRule } from '@/lib/api';
import { Modal, Button, Stack } from '@/components/ui';
import { DEFAULT_BRANDING, invalidateBranding, type BrandingSettings } from '@/lib/branding';
import type {
  SMTPSettings, NotificationChannel, ChannelType, AIProvider,
  TempoAuthType,
  LDAPConfig, LDAPGroupRoleMapping, LDAPDirectoryUser, Role,
} from '@/lib/types';
import type { KibanaSettings } from '@/lib/types';
import {
  IconMail, IconBell, IconSparkles, IconLock, IconTrash,
} from '@/components/icons';

type Tab = 'smtp' | 'channels' | 'maintenance' | 'ai' | 'tempo' | 'kibana' | 'ldap' | 'sso' | 'retention' | 'sampling' | 'anomaly' | 'branding' | 'roles' | 'pipeline';

export default function SettingsPage() {
  const { user } = useAuth();
  const [tab, setTab] = useState<Tab>('smtp');

  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Settings" />
        <div id="content">
          <Empty icon={<IconLock size={28} />} title="Admin access required">
            System settings are only available to administrators.
          </Empty>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Settings" />
      <div id="content">
        <div className="tab-strip" style={{ marginBottom: 16 }}>
          <TabBtn active={tab === 'smtp'} onClick={() => setTab('smtp')}>
            <IconMail /> <span style={{ marginLeft: 6 }}>SMTP</span>
          </TabBtn>
          <TabBtn active={tab === 'channels'} onClick={() => setTab('channels')}>
            <IconBell /> <span style={{ marginLeft: 6 }}>Notification channels</span>
          </TabBtn>
          <TabBtn active={tab === 'maintenance'} onClick={() => setTab('maintenance')}>
            <span style={{ fontFamily: 'monospace' }}>⏸</span>
            <span style={{ marginLeft: 6 }}>Maintenance windows</span>
          </TabBtn>
          <TabBtn active={tab === 'ai'} onClick={() => setTab('ai')}>
            <IconSparkles /> <span style={{ marginLeft: 6 }}>AI Copilot</span>
          </TabBtn>
          <TabBtn active={tab === 'tempo'} onClick={() => setTab('tempo')}>
            <span style={{ fontFamily: 'monospace' }}>⇆</span>
            <span style={{ marginLeft: 6 }}>Tempo backend</span>
          </TabBtn>
          <TabBtn active={tab === 'kibana'} onClick={() => setTab('kibana')}>
            <span style={{ fontFamily: 'monospace' }}>≡</span>
            <span style={{ marginLeft: 6 }}>Kibana link</span>
          </TabBtn>
          <TabBtn active={tab === 'ldap'} onClick={() => setTab('ldap')}>
            <IconLock /> <span style={{ marginLeft: 6 }}>LDAP / AD</span>
          </TabBtn>
          <TabBtn active={tab === 'sso'} onClick={() => setTab('sso')}>
            <IconLock /> <span style={{ marginLeft: 6 }}>SSO presets</span>
          </TabBtn>
          <TabBtn active={tab === 'retention'} onClick={() => setTab('retention')}>
            <IconTrash /> <span style={{ marginLeft: 6 }}>Data retention</span>
          </TabBtn>
          <TabBtn active={tab === 'sampling'} onClick={() => setTab('sampling')}>
            <span style={{ fontFamily: 'monospace' }}>%</span>
            <span style={{ marginLeft: 6 }}>Trace sampling</span>
          </TabBtn>
          <TabBtn active={tab === 'anomaly'} onClick={() => setTab('anomaly')}>
            <span style={{ fontFamily: 'monospace' }}>↗</span>
            <span style={{ marginLeft: 6 }}>Anomaly promotion</span>
          </TabBtn>
          <TabBtn active={tab === 'branding'} onClick={() => setTab('branding')}>
            <span style={{ fontFamily: 'monospace' }}>◐</span>
            <span style={{ marginLeft: 6 }}>Branding</span>
          </TabBtn>
          <TabBtn active={tab === 'roles'} onClick={() => setTab('roles')}>
            <IconLock /> <span style={{ marginLeft: 6 }}>Custom roles</span>
          </TabBtn>
          <TabBtn active={tab === 'pipeline'} onClick={() => setTab('pipeline')}>
            <span style={{ fontFamily: 'monospace' }}>⇉</span>
            <span style={{ marginLeft: 6 }}>Pipeline</span>
          </TabBtn>
        </div>
        {tab === 'smtp' && <SMTPTab />}
        {tab === 'channels' && <ChannelsTab />}
        {tab === 'maintenance' && <MaintenanceTab />}
        {tab === 'ai' && <AITab />}
        {tab === 'tempo' && <TempoTab />}
        {tab === 'kibana' && <KibanaTab />}
        {tab === 'ldap' && <LDAPTab />}
        {tab === 'sso' && <SSOPresetsTab />}
        {tab === 'retention' && <RetentionTab />}
        {tab === 'sampling' && <SamplingTab />}
        {tab === 'anomaly' && <AnomalyPromotionTab />}
        {tab === 'branding' && <BrandingTab />}
        {tab === 'roles' && <CustomRolesTab />}
        {tab === 'pipeline' && <PipelineTab />}
      </div>
    </>
  );
}

// ── Pipeline tab (v0.5.263) ─────────────────────────────────────────────────
//
// Ingest-time drop rules — span-only MVP. Operator picks "service.name =
// frontend" and any span matching that predicate gets dropped before the
// sampler / consumer sees it. Drop counter is exposed on /admin/stats so
// the effect is observable without log-grepping.
function PipelineTab() {
  const [rules, setRules] = useState<PipelineRule[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<PipelineRule | null>(null);
  const [creating, setCreating] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    setRules(undefined);
    api.listPipelineRules().then(r => setRules(r.rules ?? [])).catch(() => setRules(null));
  };
  useEffect(load, []);

  const toggle = async (r: PipelineRule) => {
    try {
      await api.upsertPipelineRule({ ...r, enabled: !r.enabled });
      setMsg({ kind: 'ok', text: `${r.name}: ${!r.enabled ? 'enabled' : 'disabled'}` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
  };

  const remove = async (r: PipelineRule) => {
    if (!confirm(`Delete pipeline rule "${r.name}"? Spans matching this rule will no longer be dropped.`)) return;
    try {
      await api.deletePipelineRule(r.id);
      setMsg({ kind: 'ok', text: `Deleted "${r.name}"` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
  };

  if (rules === undefined) return <Spinner />;
  if (rules === null) return <Empty icon="!" title="Failed to load pipeline rules" />;

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 12 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
          Ingest-time rules evaluated <b>before</b> the sampler. A "drop"
          rule that matches removes the span entirely — no CH write, no
          tail-sampler bookkeeping. Use for noisy health-check spans,
          internal-only kinds you never want to inspect, or services
          you've decided to drop wholesale for cost.
        </span>
        <Button onClick={() => setCreating(true)}>+ New rule</Button>
      </div>

      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4, fontSize: 13,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'rgba(34,197,94,0.08)' : 'rgba(220,38,38,0.08)',
          border: `1px solid ${msg.kind === 'ok' ? 'rgba(34,197,94,0.3)' : 'rgba(220,38,38,0.3)'}`,
        }}>{msg.text}</div>
      )}

      {rules.length === 0 ? (
        <Empty icon="⇉" title="No pipeline rules yet">
          Create one to drop noisy span traffic at ingest.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Signal</th>
                <th>Predicate</th>
                <th>Enabled</th>
                <th style={{ textAlign: 'right' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {rules.map(r => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 600 }}>{r.name}</td>
                  <td>
                    <span className={r.kind === 'drop' ? 'badge b-err' : 'badge b-info'}>
                      {r.kind.toUpperCase()}
                    </span>
                  </td>
                  <td>
                    <code style={{ fontSize: 11 }}>{r.signal}</code>
                  </td>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                    {r.when.key} <b>{r.when.op}</b>{' '}
                    <span style={{ color: 'var(--text2)' }}>"{r.when.value}"</span>
                    {r.kind === 'enrich' && r.setAttributes && Object.entries(r.setAttributes).map(([k, v]) => (
                      <span key={k} style={{ marginLeft: 8, color: 'var(--accent2)' }}>
                        → {k}=<b>"{v}"</b>
                      </span>
                    ))}
                    {r.kind === 'sample' && r.rate != null && (
                      <span style={{ marginLeft: 8, color: 'var(--accent2)' }}>
                        keep <b>{(r.rate * 100).toFixed(1)}%</b>
                      </span>
                    )}
                  </td>
                  <td>
                    <input type="checkbox" checked={r.enabled} onChange={() => toggle(r)} />
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="sec" onClick={() => setEditing(r)} style={{ marginRight: 6 }}>
                      Edit
                    </button>
                    <button className="sec" onClick={() => remove(r)}>
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <PipelineRuleModal
          existing={editing}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={() => { setCreating(false); setEditing(null); load(); }}
        />
      )}
    </div>
  );
}

function PipelineRuleModal({ existing, onClose, onSaved }: {
  existing: PipelineRule | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name,    setName]    = useState(existing?.name ?? '');
  const [kind,    setKind]    = useState<PipelineRule['kind']>(existing?.kind ?? 'drop');
  const [signal,  setSignal]  = useState<PipelineRule['signal']>(existing?.signal ?? 'spans');
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [whenKey, setWhenKey] = useState(existing?.when.key ?? 'service.name');
  const [whenOp,  setWhenOp]  = useState<PipelineRule['when']['op']>(existing?.when.op ?? '=');
  const [whenVal, setWhenVal] = useState(existing?.when.value ?? '');
  // v0.5.270 — enrich + sample fields. Enrich uses a single
  // key/value pair for the MVP (multi-attr could come later
  // via a chip list — start narrow).
  const [enrichKey, setEnrichKey] = useState<string>(() => {
    const m = existing?.setAttributes ?? {};
    return Object.keys(m)[0] ?? '';
  });
  const [enrichVal, setEnrichVal] = useState<string>(() => {
    const m = existing?.setAttributes ?? {};
    const k = Object.keys(m)[0];
    return k ? m[k] : '';
  });
  const [rate, setRate] = useState<number>(existing?.rate ?? 0.1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const body: PipelineRule = {
        id: existing?.id ?? '',
        name: name.trim(),
        kind, signal, enabled,
        when: { key: whenKey.trim(), op: whenOp, value: whenVal.trim() },
      };
      if (kind === 'enrich') {
        body.setAttributes = enrichKey.trim()
          ? { [enrichKey.trim()]: enrichVal.trim() }
          : {};
      }
      if (kind === 'sample') {
        body.rate = rate;
      }
      await api.upsertPipelineRule(body);
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
      title={existing ? `Edit rule — ${existing.name}` : 'New pipeline rule'}
      size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="pipeline-form" loading={busy}>Save</Button>
        </>
      }>
      <form id="pipeline-form" onSubmit={submit}>
        <Stack gap={3}>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Rule name</label>
            <input value={name} onChange={e => setName(e.target.value)} required
              placeholder="e.g. drop frontend health-checks"
              style={{ width: '100%' }} />
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Action</label>
              <select value={kind} onChange={e => setKind(e.target.value as PipelineRule['kind'])}
                style={{ width: '100%' }}>
                <option value="drop">Drop — discard the matching signal</option>
                <option value="enrich">Enrich — set a resource attribute</option>
                <option value="sample">Sample — keep at probability</option>
              </select>
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Signal</label>
              <select value={signal} onChange={e => setSignal(e.target.value as PipelineRule['signal'])}
                style={{ width: '100%' }}>
                <option value="spans">spans</option>
                <option value="logs" disabled>logs (coming soon)</option>
                <option value="metrics" disabled>metrics (coming soon)</option>
              </select>
            </div>
          </div>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              When (predicate)
            </label>
            <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr 2fr', gap: 8 }}>
              <input value={whenKey} onChange={e => setWhenKey(e.target.value)} required
                placeholder="service.name | name | kind | attr.X | resource.X"
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              <select value={whenOp} onChange={e => setWhenOp(e.target.value as PipelineRule['when']['op'])}
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                <option value="=">=</option>
                <option value="!=">!=</option>
                <option value="contains">contains</option>
                <option value="startsWith">startsWith</option>
                <option value="endsWith">endsWith</option>
              </select>
              <input value={whenVal} onChange={e => setWhenVal(e.target.value)} required
                placeholder="value"
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
            </div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
              Well-known span fields branch directly: <code>service.name</code>,
              {' '}<code>name</code>, <code>kind</code>, <code>status_code</code>.
              Custom attributes via <code>attr.foo</code> / <code>resource.foo</code> prefix.
            </div>
          </div>

          {/* v0.5.270 — enrich-only fields */}
          {kind === 'enrich' && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Set resource attribute
              </label>
              <div style={{ display: 'grid', gridTemplateColumns: '2fr 3fr', gap: 8 }}>
                <input value={enrichKey} onChange={e => setEnrichKey(e.target.value)}
                  placeholder="e.g. team, region, cluster"
                  style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
                <input value={enrichVal} onChange={e => setEnrichVal(e.target.value)}
                  placeholder="value"
                  style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              </div>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Sets a resource attribute on every matching span. Existing keys are
                overridden. Multi-attribute support coming later — start with one.
              </div>
            </div>
          )}

          {/* v0.5.270 — sample-only fields */}
          {kind === 'sample' && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Keep rate ({(rate * 100).toFixed(1)}%)
              </label>
              <input type="range" min={0} max={1} step={0.01}
                value={rate} onChange={e => setRate(Number(e.target.value))}
                style={{ width: '100%' }} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Probability of keeping each matching span. 1.0 = no-op; 0.0 = use a
                drop rule instead. Runs BEFORE the global head sampler — that may
                still further sample the kept spans.
              </div>
            </div>
          )}

          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
            <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
            Enabled
          </label>
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
function CustomRolesTab() {
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
                    <button className="sec" onClick={() => setEditing(r)} style={{ marginRight: 6 }}>
                      Edit
                    </button>
                    <button className="sec" onClick={() => remove(r.name)} disabled={busy === r.name}>
                      Delete
                    </button>
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

function TabBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} className={active ? 'active' : ''}>{children}</button>
  );
}

// ── SMTP tab ────────────────────────────────────────────────────────────────

function SMTPTab() {
  const [s, setS] = useState<SMTPSettings | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [testTo, setTestTo] = useState('');

  const load = () => {
    setS(undefined);
    api.getSMTP().then(setS).catch(() => setS(null));
  };
  useEffect(load, []);

  if (s === undefined) return <Spinner />;
  if (s === null) return <Empty icon="⚠" title="Failed to load SMTP settings" />;

  const update = <K extends keyof SMTPSettings>(k: K, v: SMTPSettings[K]) => setS({ ...s, [k]: v });

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      // If the password field is still the masked sentinel, send empty
      // string — the backend treats "empty / ********" as "keep current".
      const payload = { ...s, password: s.password === '********' ? '' : s.password };
      const next = await api.putSMTP(payload);
      setS(next);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const sendTest = async () => {
    if (!testTo) { setMsg({ kind: 'err', text: 'Enter a recipient first' }); return; }
    setBusy(true); setMsg(null);
    try {
      await api.testSMTP(testTo);
      setMsg({ kind: 'ok', text: `Test email sent to ${testTo}.` });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={save} style={{ maxWidth: 640 }}>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Outbound mail settings used by every email notification channel.
        Changes take effect immediately — no restart needed.
      </p>

      <Row>
        <Field label="SMTP host" flex={2}>
          <input required value={s.host} placeholder="smtp.example.com"
            onChange={e => update('host', e.target.value)} />
        </Field>
        <Field label="Port" flex={1}>
          <input required type="number" value={s.port || ''} placeholder="587"
            onChange={e => update('port', parseInt(e.target.value || '0'))} />
        </Field>
      </Row>
      <Row>
        <Field label="Username" flex={1}>
          <input value={s.username} onChange={e => update('username', e.target.value)} />
        </Field>
        <Field label="Password" flex={1}>
          <input type="password" value={s.password}
            placeholder={s.configured && !s.password ? '(unchanged)' : ''}
            onChange={e => update('password', e.target.value)} />
        </Field>
      </Row>
      <Row>
        <Field label="From address" flex={2}>
          <input required type="email" value={s.from} placeholder="coremetry@yourcorp.com"
            onChange={e => update('from', e.target.value)} />
        </Field>
        <Field label="From name (optional)" flex={1}>
          <input value={s.fromName} placeholder="Coremetry Alerts"
            onChange={e => update('fromName', e.target.value)} />
        </Field>
      </Row>
      <Row>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12 }}>
          <input type="checkbox" checked={s.startTLS}
            onChange={e => update('startTLS', e.target.checked)} />
          Use STARTTLS (recommended for ports 587/25)
        </label>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12 }}>
          <input type="checkbox" checked={s.skipVerify}
            onChange={e => update('skipVerify', e.target.checked)} />
          Skip TLS verification (self-signed only)
        </label>
      </Row>

      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}

      <div style={{ display: 'flex', gap: 8, marginTop: 18, alignItems: 'center' }}>
        <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</button>
        <div style={{ flex: 1 }} />
        <input type="email" value={testTo} placeholder="recipient@example.com"
          onChange={e => setTestTo(e.target.value)} style={{ width: 240 }} />
        <button type="button" className="sec" onClick={sendTest} disabled={busy || !s.configured}>
          Send test email
        </button>
      </div>
      {!s.configured && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
          Save valid SMTP settings before testing.
        </div>
      )}
    </form>
  );
}

// ── Maintenance windows tab ────────────────────────────────────────────────
//
// Operator-declared time ranges that suppress alert
// notifications for matching (service, severity) tuples.
// Problems still open + auto-resolve as usual — only the
// live channel fan-out (Slack / email / Zoom / etc.) is
// skipped. After the window expires the /anomalies +
// /incidents pages still show the full timeline.

function MaintenanceTab() {
  const [items, setItems] = useState<MaintenanceWindow[] | null | undefined>(undefined);
  const [showAll, setShowAll] = useState(false);
  const [creating, setCreating] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    setItems(undefined);
    api.listMaintenanceWindows(showAll)
      .then(r => setItems(r ?? []))
      .catch(() => setItems(null));
  };
  useEffect(load, [showAll]);

  const del = async (id: string) => {
    if (!confirm('Delete this maintenance window? Alerts will resume firing immediately.')) return;
    try {
      await api.deleteMaintenanceWindow(id);
      setMsg({ kind: 'ok', text: 'Window removed' });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : 'Delete failed' });
    }
  };

  const now = Date.now() * 1e6;
  return (
    <div>
      <div style={{ marginBottom: 12, fontSize: 12, color: 'var(--text2)' }}>
        While an active window matches a problem's <code>(service, severity)</code>,
        the live channel fan-out is suppressed. Problems still open + auto-resolve
        so the post-window review on <code>/anomalies</code> + <code>/incidents</code>
        is intact. Service supports <code>*</code> (all), an exact name, or a
        <code>name*</code> prefix.
      </div>
      <div className="controls" style={{ marginBottom: 12 }}>
        <button onClick={() => setCreating(true)}>+ New maintenance window</button>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 5,
                        color: 'var(--text2)', cursor: 'pointer', marginLeft: 'auto' }}>
          <input type="checkbox" checked={showAll}
                 onChange={e => setShowAll(e.target.checked)} />
          Show past / disabled (last 30d)
        </label>
      </div>
      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4, fontSize: 12,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
          border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
        }}>{msg.text}</div>
      )}
      {items === undefined && <Spinner />}
      {items !== undefined && (!items || items.length === 0) && (
        <Empty icon="◯" title="No maintenance windows">
          Declare a window before a planned deploy to silence alerts on the
          affected services. They auto-expire — no clean-up needed.
        </Empty>
      )}
      {items && items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead><tr>
              <th>Service</th><th>Severity</th>
              <th>Starts</th><th>Ends</th><th>Reason</th>
              <th>By</th><th>Status</th><th style={{ textAlign: 'right' }}>Actions</th>
            </tr></thead>
            <tbody>
              {items.map(w => {
                const active = !w.disabled && w.startAt <= now && now <= w.endAt;
                const upcoming = !w.disabled && w.startAt > now;
                return (
                  <tr key={w.id}>
                    <td style={{ fontFamily: 'monospace', fontWeight: 600 }}>{w.service}</td>
                    <td className="mono" style={{ fontSize: 11, textTransform: 'uppercase' }}>{w.severity}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{new Date(w.startAt / 1e6).toLocaleString()}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{new Date(w.endAt / 1e6).toLocaleString()}</td>
                    <td style={{ fontSize: 12, color: 'var(--text2)' }}>{w.reason || '—'}</td>
                    <td style={{ fontSize: 11, color: 'var(--text3)', fontFamily: 'monospace' }}>{w.createdBy || '—'}</td>
                    <td>
                      {w.disabled ? <span className="badge b-err" style={{ fontSize: 9 }}>DISABLED</span>
                        : active   ? <span className="badge b-warn" style={{ fontSize: 9 }}>ACTIVE</span>
                        : upcoming ? <span className="badge b-info" style={{ fontSize: 9 }}>UPCOMING</span>
                        :            <span className="badge b-ok" style={{ fontSize: 9 }}>PAST</span>}
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      {!w.disabled && (
                        <button className="sec" onClick={() => del(w.id)}
                          style={{ color: 'var(--err)' }}>End / delete</button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {creating && (
        <NewMaintenanceModal onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); load(); setMsg({ kind: 'ok', text: 'Window created' }); }} />
      )}
    </div>
  );
}

function NewMaintenanceModal({ onClose, onCreated }: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [service, setService] = useState('*');
  const [severity, setSeverity] = useState('*');
  // Default to "right now → +60 min". datetime-local needs YYYY-MM-DDTHH:MM
  // formatted in the operator's local zone.
  const toLocalInput = (d: Date) => {
    const off = d.getTimezoneOffset();
    return new Date(d.getTime() - off * 60_000).toISOString().slice(0, 16);
  };
  const [startAt, setStartAt] = useState(() => toLocalInput(new Date()));
  const [endAt, setEndAt] = useState(() => toLocalInput(new Date(Date.now() + 60 * 60_000)));
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const startMs = new Date(startAt).getTime();
      const endMs = new Date(endAt).getTime();
      if (!isFinite(startMs) || !isFinite(endMs)) throw new Error('Invalid date');
      if (endMs <= startMs) throw new Error('End must be after start');
      await api.createMaintenanceWindow({
        service: service.trim() || '*',
        severity: severity || '*',
        startAt: startMs * 1e6,
        endAt: endMs * 1e6,
        reason: reason.trim(),
      });
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Create failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open onClose={onClose} title="New maintenance window" size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-mw-form" loading={busy}>Create</Button>
        </>
      }>
      <form id="new-mw-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field label='Service ("*" for global · exact name · "payment*" prefix)'>
            <input required value={service}
              onChange={e => setService(e.target.value)}
              style={{ width: '100%' }} />
          </Field>
          <Field label="Severity">
            <select value={severity} onChange={e => setSeverity(e.target.value)}
              style={{ width: '100%' }}>
              <option value="*">All severities</option>
              <option value="info">info only</option>
              <option value="warning">warning only</option>
              <option value="critical">critical only</option>
            </select>
          </Field>
          <Row>
            <Field label="Starts at" flex={1}>
              <input type="datetime-local" required value={startAt}
                onChange={e => setStartAt(e.target.value)}
                style={{ width: '100%' }} />
            </Field>
            <Field label="Ends at" flex={1}>
              <input type="datetime-local" required value={endAt}
                onChange={e => setEndAt(e.target.value)}
                style={{ width: '100%' }} />
            </Field>
          </Row>
          <Field label='Reason (optional) — e.g. "deploy payment-api v2.34"'>
            <input value={reason}
              onChange={e => setReason(e.target.value)}
              style={{ width: '100%' }} />
          </Field>
          {error && (
            <div style={{
              padding: 8, borderRadius: 4, fontSize: 12,
              color: 'var(--err)', background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)',
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}

// ── Channels tab ────────────────────────────────────────────────────────────

function ChannelsTab() {
  const [items, setItems] = useState<NotificationChannel[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<NotificationChannel | 'new' | null>(null);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const refresh = () => {
    setItems(undefined);
    api.listChannels().then(d => setItems(d ?? [])).catch(() => setItems(null));
  };
  useEffect(refresh, []);

  const onDelete = async (c: NotificationChannel) => {
    if (!confirm(`Delete channel "${c.name}"?`)) return;
    try { await api.deleteChannel(c.id); refresh(); }
    catch (err) { setMsg({ kind: 'err', text: humanize(err) }); }
  };
  const onTest = async (c: NotificationChannel) => {
    setMsg(null);
    try {
      await api.testChannel(c.id);
      setMsg({ kind: 'ok', text: `Test sent through "${c.name}".` });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    }
  };

  return (
    <div>
      <div className="controls" style={{ marginBottom: 12 }}>
        <p style={{ color: 'var(--text2)', fontSize: 13, margin: 0 }}>
          Channels receive Problem alerts whenever the evaluator or anomaly detector opens a new incident.
        </p>
        <button onClick={() => setEditing('new')} style={{ marginLeft: 'auto' }}>+ New channel</button>
      </div>

      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}

      {items === undefined && <Spinner />}
      {items !== undefined && (!items || items.length === 0) && (
        <Empty icon={<IconBell size={28} />} title="No channels yet">
          Create one to start receiving alert notifications.
        </Empty>
      )}
      {items && items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Type</th>
                <th>Recipients / target</th>
                <th>Min severity</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {items.map(c => (
                <tr key={c.id}>
                  <td><b>{c.name}</b></td>
                  <td className="mono">{c.type}</td>
                  <td className="mono" style={{ fontSize: 12 }}>{summarizeChannel(c)}</td>
                  <td><SeverityBadge s={c.minSeverity} /></td>
                  <td>{c.enabled
                    ? <span className="badge b-ok">ON</span>
                    : <span className="badge b-gray">OFF</span>}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="sec" onClick={() => onTest(c)} style={{ marginRight: 6 }}>Test</button>
                    <button className="sec" onClick={() => setEditing(c)} style={{ marginRight: 6 }}>Edit</button>
                    <button className="sec" onClick={() => onDelete(c)}
                      style={{ color: 'var(--err)' }}>Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && (
        <ChannelModal
          initial={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); refresh(); }}
        />
      )}
    </div>
  );
}

function summarizeChannel(c: NotificationChannel): string {
  if (c.type === 'email') return (c.config.recipients ?? []).join(', ') || '(none)';
  if (c.type === 'slack' || c.type === 'mattermost') return c.config.webhookUrl ?? '(no webhook)';
  if (c.type === 'teams') return c.config.webhookUrl ?? '(no webhook)';
  if (c.type === 'zoomchat') {
    // New OAuth-shape channels read the chat channel JID; legacy
    // ones still carry the webhook URL — show whichever exists so
    // the operator can spot which channels still need migration.
    // Proxy hosts get appended in parens so the list view shows
    // a non-default routing target without expanding the row.
    const proxy = c.config.apiBaseUrl ? ` via ${c.config.apiBaseUrl}` : '';
    if (c.config.channelId) return `channel: ${c.config.channelId}${proxy}`;
    if (c.config.toContact) return `DM: ${c.config.toContact}${proxy}`;
    if (c.config.webhookUrl) return '⚠ legacy webhook — please reconfigure';
    return '(not configured)';
  }
  if (c.type === 'webhook') return c.config.url ?? '(no url)';
  if (c.type === 'whatsapp') return (c.config.to ?? []).join(', ') || '(no recipients)';
  return '';
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}

function ChannelModal({ initial, onClose, onSaved }: {
  initial: NotificationChannel | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [type, setType] = useState<ChannelType>(initial?.type ?? 'email');
  const [recipients, setRecipients] = useState((initial?.config.recipients ?? []).join(', '));
  const [webhookUrl, setWebhookUrl] = useState(initial?.config.webhookUrl ?? '');
  const [url, setUrl] = useState(initial?.config.url ?? '');
  // Zoom Chat Server-to-Server OAuth fields. clientSecret is
  // write-only — the GET endpoint never echoes it back, so the
  // initial value is always empty. Existing channels keep their
  // secret intact unless the operator types a replacement.
  const [zoomAccountId, setZoomAccountId] = useState(initial?.config.accountId ?? '');
  const [zoomClientId, setZoomClientId] = useState(initial?.config.clientId ?? '');
  const [zoomClientSecret, setZoomClientSecret] = useState('');
  const [zoomChannelId, setZoomChannelId] = useState(initial?.config.channelId ?? '');
  const [zoomToContact, setZoomToContact] = useState(initial?.config.toContact ?? '');
  // Optional proxy / sandbox host overrides. Empty → public
  // Zoom defaults (api.zoom.us + zoom.us). Banks routing
  // outbound traffic through a corporate gateway fill these.
  const [zoomAPIBaseURL, setZoomAPIBaseURL] = useState(initial?.config.apiBaseUrl ?? '');
  const [zoomOAuthBaseURL, setZoomOAuthBaseURL] = useState(initial?.config.oauthBaseUrl ?? '');
  // TLS verification toggle — defaults to off (verify enabled).
  // Operators in corp networks where api.zoom.us is fronted by
  // a MITM proxy with a private CA can flip this on as a
  // workaround. Public Zoom traffic should always verify.
  const [zoomSkipVerify, setZoomSkipVerify] = useState(
    initial?.config.insecureSkipVerify ?? false);
  // WhatsApp / Twilio fields
  const [twilioSid, setTwilioSid] = useState(initial?.config.accountSid ?? '');
  const [twilioToken, setTwilioToken] = useState(initial?.config.authToken ?? '');
  const [waFrom, setWaFrom] = useState(initial?.config.from ?? '');
  const [waTo, setWaTo] = useState((initial?.config.to ?? []).join(', '));
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [minSeverity, setMinSeverity] = useState<'info' | 'warning' | 'critical'>(initial?.minSeverity ?? 'warning');
  // Routing predicates — comma-separated in the UI, parsed
  // into string arrays on save. Empty / blank inputs leave
  // the predicate unset so the channel stays a catch-all.
  const [matchServices, setMatchServices] = useState((initial?.matchRules?.services ?? []).join(', '));
  const [matchSREs, setMatchSREs] = useState((initial?.matchRules?.sreTeams ?? []).join(', '));
  const [matchOwners, setMatchOwners] = useState((initial?.matchRules?.ownerTeams ?? []).join(', '));
  const [matchClusters, setMatchClusters] = useState((initial?.matchRules?.clusters ?? []).join(', '));
  const [matchQuietHours, setMatchQuietHours] = useState(initial?.matchRules?.quietHours ?? '');
  const [matchQuietHoursTz, setMatchQuietHoursTz] = useState(initial?.matchRules?.quietHoursTz ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const config: NotificationChannel['config'] = {};
      if (type === 'email') {
        config.recipients = recipients.split(/[,;\s]+/).map(s => s.trim()).filter(Boolean);
        if (config.recipients.length === 0) throw new Error('At least one recipient is required');
      } else if (type === 'slack' || type === 'mattermost') {
        if (!webhookUrl) throw new Error(`${type === 'slack' ? 'Slack' : 'Mattermost'} webhook URL is required`);
        config.webhookUrl = webhookUrl;
      } else if (type === 'teams') {
        if (!webhookUrl) throw new Error('Microsoft Teams webhook URL is required');
        config.webhookUrl = webhookUrl;
      } else if (type === 'zoomchat') {
        if (!zoomAccountId.trim()) throw new Error('Zoom Account ID is required');
        if (!zoomClientId.trim()) throw new Error('Zoom Client ID is required');
        // Secret is required on a NEW channel; on edit, leaving it
        // blank means "keep the saved secret" — the server detects
        // that by passing through the existing value.
        if (!initial && !zoomClientSecret.trim()) {
          throw new Error('Zoom Client Secret is required');
        }
        if (!zoomChannelId.trim() && !zoomToContact.trim()) {
          throw new Error('Either a Channel ID or a contact email is required');
        }
        config.accountId = zoomAccountId.trim();
        config.clientId = zoomClientId.trim();
        if (zoomClientSecret.trim()) config.clientSecret = zoomClientSecret.trim();
        if (zoomChannelId.trim()) config.channelId = zoomChannelId.trim();
        if (zoomToContact.trim()) config.toContact = zoomToContact.trim();
        if (zoomAPIBaseURL.trim()) config.apiBaseUrl = zoomAPIBaseURL.trim();
        if (zoomOAuthBaseURL.trim()) config.oauthBaseUrl = zoomOAuthBaseURL.trim();
        if (zoomSkipVerify) config.insecureSkipVerify = true;
      } else if (type === 'webhook') {
        if (!url) throw new Error('Webhook URL is required');
        config.url = url;
      } else if (type === 'whatsapp') {
        if (!twilioSid || !twilioToken) throw new Error('Twilio Account SID and Auth Token are required');
        if (!waFrom) throw new Error('Sender number (whatsapp:+E164) is required');
        const tos = waTo.split(/[,;\s]+/).map(s => s.trim()).filter(Boolean);
        if (tos.length === 0) throw new Error('At least one WhatsApp recipient is required');
        config.accountSid = twilioSid.trim();
        config.authToken = twilioToken.trim();
        config.from = waFrom.trim();
        config.to = tos;
      }
      const splitCSL = (s: string) =>
        s.split(/[,;\s]+/).map(x => x.trim()).filter(Boolean);
      const matchRules = {
        services:     splitCSL(matchServices),
        sreTeams:     splitCSL(matchSREs),
        ownerTeams:   splitCSL(matchOwners),
        clusters:     splitCSL(matchClusters),
        quietHours:   matchQuietHours.trim(),
        quietHoursTz: matchQuietHoursTz.trim(),
      };
      const payload = { name, type, config, enabled, minSeverity, matchRules };
      if (initial) await api.updateChannel(initial.id, payload);
      else        await api.createChannel(payload);
      onSaved();
    } catch (err) {
      setError(humanize(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 460, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>
          {initial ? `Edit channel — ${initial.name}` : 'New channel'}
        </div>
        <form onSubmit={submit}>
          <Field label="Name">
            <input required autoFocus value={name}
              onChange={e => setName(e.target.value)} style={{ width: '100%' }} />
          </Field>
          <Row>
            <Field label="Type" flex={1}>
              <select value={type} onChange={e => setType(e.target.value as ChannelType)}>
                <option value="email">Email</option>
                <option value="slack">Slack</option>
                <option value="mattermost">Mattermost</option>
                <option value="teams">Microsoft Teams</option>
                <option value="zoomchat">Zoom Chat</option>
                <option value="webhook">Webhook (generic JSON POST)</option>
                <option value="whatsapp">WhatsApp (via Twilio)</option>
              </select>
            </Field>
            <Field label="Min severity" flex={1}>
              <select value={minSeverity}
                onChange={e => setMinSeverity(e.target.value as 'info' | 'warning' | 'critical')}>
                <option value="info">Info (every problem)</option>
                <option value="warning">Warning</option>
                <option value="critical">Critical only</option>
              </select>
            </Field>
          </Row>

          {type === 'email' && (
            <Field label="Recipients (comma-separated)">
              <input required value={recipients} placeholder="oncall@acme.com, sre@acme.com"
                onChange={e => setRecipients(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'slack' && (
            <Field label="Slack incoming webhook URL">
              <input required value={webhookUrl} placeholder="https://hooks.slack.com/services/T.../B.../..."
                onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'mattermost' && (
            <Field label="Mattermost incoming webhook URL">
              <input required value={webhookUrl} placeholder="https://your-mattermost.example.com/hooks/..."
                onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'teams' && (
            <Field label="Microsoft Teams incoming webhook URL">
              <input required value={webhookUrl}
                placeholder="https://outlook.office.com/webhook/..."
                onChange={e => setWebhookUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'zoomchat' && (
            <>
              <div style={{
                fontSize: 11, color: 'var(--text2)', lineHeight: 1.6,
                padding: '8px 10px', borderRadius: 4,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                marginBottom: 8,
              }}>
                Server-to-Server OAuth flow. Create a "Server-to-Server OAuth" app in the
                Zoom App Marketplace with the <code>chat_message:write:admin</code> scope,
                then paste its credentials below. Coremetry exchanges them for an access
                token (~1h cache) and posts to Zoom's REST API.
              </div>
              <Row>
                <Field label="Account ID" flex={1}>
                  <input required value={zoomAccountId}
                    placeholder="ABC1234d-XYZ..."
                    onChange={e => setZoomAccountId(e.target.value)} style={{ width: '100%' }} />
                </Field>
                <Field label="Client ID" flex={1}>
                  <input required value={zoomClientId}
                    placeholder="from the S2S OAuth app"
                    onChange={e => setZoomClientId(e.target.value)} style={{ width: '100%' }} />
                </Field>
              </Row>
              <Field label={initial
                  ? 'Client Secret (leave empty to keep saved value)'
                  : 'Client Secret'}>
                <input required={!initial} value={zoomClientSecret} type="password"
                  placeholder={initial ? '•••••••• (unchanged)' : 'never echoed back after save'}
                  onChange={e => setZoomClientSecret(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Channel ID (JID) — target chat channel">
                <div style={{ display: 'flex', gap: 6 }}>
                  <input value={zoomChannelId}
                    placeholder='e.g. "1234567890abcdef@xmpp.zoom.us" — copy from Zoom channel info'
                    onChange={e => setZoomChannelId(e.target.value)} style={{ flex: 1 }} />
                  <ZoomChannelPicker
                    existingChannelId={initial?.id}
                    accountId={zoomAccountId}
                    clientId={zoomClientId}
                    clientSecret={zoomClientSecret}
                    oauthBaseUrl={zoomOAuthBaseURL}
                    apiBaseUrl={zoomAPIBaseURL}
                    insecureSkipVerify={zoomSkipVerify}
                    onPick={jid => setZoomChannelId(jid)} />
                </div>
              </Field>
              <Field label="Or DM contact email (fallback if Channel ID is empty)">
                <input value={zoomToContact} type="email"
                  placeholder="oncall@example.com"
                  onChange={e => setZoomToContact(e.target.value)} style={{ width: '100%' }} />
              </Field>
              {/* Optional API + OAuth host overrides — proxy /
                  sandbox use cases. Leave empty for public Zoom. */}
              <details style={{ marginTop: 4 }}>
                <summary style={{ cursor: 'pointer', fontSize: 12, color: 'var(--text2)' }}>
                  Advanced: proxy / sandbox host overrides
                </summary>
                <div style={{ paddingTop: 8 }}>
                  <Row>
                    <Field label="API base URL (chat messages)" flex={1}>
                      <input value={zoomAPIBaseURL}
                        placeholder="https://api.zoom.us (default)"
                        onChange={e => setZoomAPIBaseURL(e.target.value)} style={{ width: '100%' }} />
                    </Field>
                    <Field label="OAuth base URL (token exchange)" flex={1}>
                      <input value={zoomOAuthBaseURL}
                        placeholder="https://zoom.us (default)"
                        onChange={e => setZoomOAuthBaseURL(e.target.value)} style={{ width: '100%' }} />
                    </Field>
                  </Row>
                  <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
                    Banks routing outbound traffic through a corporate gateway can point both
                    fields at their proxy. Endpoint paths stay the same
                    (<code>/v2/chat/users/me/messages</code> and <code>/oauth/token</code>) —
                    only the host changes. Leave empty to hit Zoom's public hosts.
                  </div>
                  <label style={{
                    display: 'flex', alignItems: 'flex-start', gap: 8,
                    marginTop: 12, fontSize: 12, cursor: 'pointer',
                  }}>
                    <input type="checkbox"
                      checked={zoomSkipVerify}
                      onChange={e => setZoomSkipVerify(e.target.checked)}
                      style={{ marginTop: 2 }} />
                    <span>
                      <span style={{ fontWeight: 600 }}>Skip TLS certificate verification</span>
                      <span style={{ display: 'block', color: 'var(--text3)', fontSize: 11, marginTop: 2, lineHeight: 1.5 }}>
                        Disables certificate trust checks on the OAuth + chat calls. Turn this on
                        only when the corporate proxy fronting <code>api.zoom.us</code> terminates
                        TLS with a private CA the pod doesn't trust. Equivalent to <code>curl -k</code> —
                        public Zoom hosts should leave it off.
                      </span>
                    </span>
                  </label>
                </div>
              </details>
              {/* Legacy webhook nudge — surfaces when editing a
                  channel that still carries the pre-v0.4.78 shape. */}
              {initial?.config.webhookUrl && !initial?.config.accountId && (
                <div style={{
                  fontSize: 11, color: 'var(--warn)', padding: '6px 10px',
                  borderRadius: 4, background: 'rgba(245,158,11,0.10)',
                  border: '1px solid rgba(245,158,11,0.30)',
                  marginTop: 4,
                }}>
                  ⚠ This channel still uses the legacy webhook URL. Fill the fields
                  above and save to migrate; the webhook URL will be cleared.
                </div>
              )}
            </>
          )}
          {type === 'webhook' && (
            <Field label="Webhook URL (raw Problem JSON is POSTed here)">
              <input required value={url} placeholder="https://your-receiver.example.com/incidents"
                onChange={e => setUrl(e.target.value)} style={{ width: '100%' }} />
            </Field>
          )}
          {type === 'whatsapp' && (
            <>
              <Row>
                <Field label="Twilio Account SID" flex={1}>
                  <input required value={twilioSid} placeholder="ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
                    onChange={e => setTwilioSid(e.target.value)} style={{ width: '100%' }} />
                </Field>
                <Field label="Auth Token" flex={1}>
                  <input required type="password" value={twilioToken} placeholder="32-char Auth Token"
                    onChange={e => setTwilioToken(e.target.value)} style={{ width: '100%' }} />
                </Field>
              </Row>
              <Field label="Sender number (with whatsapp: prefix)">
                <input required value={waFrom} placeholder="whatsapp:+14155238886 (Twilio sandbox) or your approved number"
                  onChange={e => setWaFrom(e.target.value)} style={{ width: '100%' }} />
              </Field>
              <Field label="Recipient numbers (comma-separated, E.164)">
                <input required value={waTo} placeholder="+905XXXXXXXXX, +1XXXXXXXXXX"
                  onChange={e => setWaTo(e.target.value)} style={{ width: '100%' }} />
              </Field>
              <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: -4 }}>
                Twilio is the de-facto WhatsApp Business API broker. The sandbox lets you test for free
                (recipients must opt in by texting the join code). Production usage requires a Twilio-approved sender.
              </p>
            </>
          )}

          {/* Routing predicates — gate this channel to a
              subset of services / SRE teams / owner teams.
              Empty = catch-all; populated lists AND together
              with the existing severity threshold. Each
              channel can pin to a specific team's Zoom Chat
              while a "default" channel without rules still
              fires for everything. */}
          <details style={{ marginTop: 16, fontSize: 12, color: 'var(--text2)' }}>
            <summary style={{ cursor: 'pointer', fontWeight: 600 }}>
              Routing rules (leave empty for catch-all)
            </summary>
            <div style={{ marginTop: 8 }}>
              <Field label="Match services (comma-separated)">
                <input value={matchServices}
                  placeholder="payments, order-service"
                  onChange={e => setMatchServices(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Match SRE teams (comma-separated)">
                <input value={matchSREs}
                  placeholder="platform, sre-storefront"
                  onChange={e => setMatchSREs(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Match owner teams (comma-separated)">
                <input value={matchOwners}
                  placeholder="payments, ml"
                  onChange={e => setMatchOwners(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <Field label="Match k8s/openshift clusters (comma-separated)">
                <input value={matchClusters}
                  placeholder="prod-eu-west, prod-eu-central"
                  onChange={e => setMatchClusters(e.target.value)}
                  style={{ width: '100%' }} />
              </Field>
              <div style={{ display: 'grid',
                            gridTemplateColumns: '1fr 1fr', gap: 8 }}>
                <Field label="Quiet hours (HH:MM-HH:MM, may cross midnight)">
                  <input value={matchQuietHours}
                    placeholder="22:00-07:00"
                    onChange={e => setMatchQuietHours(e.target.value)}
                    style={{ width: '100%' }} />
                </Field>
                <Field label="Quiet hours timezone (IANA, default UTC)">
                  <input value={matchQuietHoursTz}
                    placeholder="Europe/Istanbul"
                    onChange={e => setMatchQuietHoursTz(e.target.value)}
                    style={{ width: '100%' }} />
                </Field>
              </div>
              <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
                Predicates AND together — every non-empty rule must match.
                e.g. services=<code>payments</code> +
                clusters=<code>prod-eu-west</code> +
                quietHours=<code>22:00-07:00</code> means "fire only on the
                payments service in eu-west, AND only outside the 10pm–7am
                window". Service catalog metadata is the source of truth for
                sreTeam / ownerTeam lookup; clusters come from the problem's
                enriched cluster list (k8s.cluster.name or
                openshift.cluster.name resource attrs).
              </p>
            </div>
          </details>

          <label style={{ display: 'flex', gap: 6, alignItems: 'center',
                          color: 'var(--text2)', fontSize: 12, marginTop: 6 }}>
            <input type="checkbox" checked={enabled}
              onChange={e => setEnabled(e.target.checked)} />
            Enabled
          </label>

          {error && <FlashBox kind="err">{error}</FlashBox>}
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 18 }}>
            <button type="button" className="sec" onClick={onClose}>Cancel</button>
            <button type="submit" disabled={busy}>
              {busy ? 'Saving…' : initial ? 'Update' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Tiny shared primitives ──────────────────────────────────────────────────

function Field({ label, children, flex }: { label: string; children: React.ReactNode; flex?: number }) {
  return (
    <label style={{ display: 'block', marginBottom: 12, flex }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

function Row({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
      {children}
    </div>
  );
}

function FlashBox({ kind, children }: { kind: 'ok' | 'err'; children: React.ReactNode }) {
  const colors = kind === 'ok'
    ? { fg: 'var(--ok)',  bg: 'rgba(63,185,80,0.08)',  bd: 'rgba(63,185,80,0.3)' }
    : { fg: 'var(--err)', bg: 'rgba(220,38,38,0.08)',  bd: 'rgba(220,38,38,0.3)' };
  return (
    <div style={{
      color: colors.fg, fontSize: 12, marginTop: 12,
      padding: '6px 10px', background: colors.bg,
      border: `1px solid ${colors.bd}`, borderRadius: 4,
    }}>{children}</div>
  );
}

// AITab — editable AI Copilot configuration. Admin picks a provider,
// pastes their key, optionally sets a model, hits Save. Server stores
// the override in system_settings and updates the live service so the
// next Explain call uses the new creds without restart.
//
// Two providers:
//   - Anthropic: classic sk-ant-… key.
//   - GitHub Copilot: GitHub OAuth token (ghu_…) with Copilot access;
//     server exchanges it for a session token and calls
//     api.githubcopilot.com (OpenAI-compatible).
function AITab() {
  const [loaded, setLoaded] = useState(false);
  const [provider, setProvider] = useState<AIProvider>('anthropic');
  const [model, setModel] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [hasKey, setHasKey] = useState(false);
  const [apiKey, setApiKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAISettings().then(s => {
      setProvider(s.provider || 'anthropic');
      setModel(s.model || '');
      setBaseUrl(s.baseUrl || '');
      setHasKey(s.hasKey);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey, model, baseUrl });
      setHasKey(next.hasKey);
      setApiKey('');
      setMsg({ kind: 'ok', text: next.hasKey ? 'Saved — Copilot is live.' : 'Saved — Copilot disabled.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const clearKey = async () => {
    if (!confirm('Remove the saved API key? Copilot buttons will disappear until a new key is set.')) return;
    setBusy(true); setMsg(null);
    try {
      const next = await api.putAISettings({ provider, apiKey: '', model, baseUrl });
      setHasKey(next.hasKey);
      setApiKey('');
      setMsg({ kind: 'ok', text: 'Key cleared — Copilot is dormant.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Clear failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  // Per-provider hint shown under the key field — explains where to
  // get the token + what shape it has, so users don't paste the wrong
  // thing.
  const keyHint = provider === 'github' ? (
    <>
      Paste a GitHub OAuth token with Copilot access (starts with{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>ghu_</code>).
      You can copy it from{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>~/.config/github-copilot/hosts.json</code>{' '}
      or run your own OAuth flow. Coremetry exchanges it for a Copilot session token automatically.
    </>
  ) : provider === 'openai' ? (
    <>
      Drives any OpenAI-compatible <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>/v1/chat/completions</code> endpoint —
      real OpenAI, Ollama, LM Studio, vLLM, llama.cpp server, LocalAI, OpenWebUI.
      Set <b>Base URL</b> below to your endpoint (e.g. <code>http://ollama:11434/v1</code>).
      API key is optional for local endpoints that don't gate on it (Ollama default).
    </>
  ) : (
    <>
      Paste your Anthropic API key (starts with{' '}
      <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3 }}>sk-ant-</code>).
      Get one at{' '}
      <a href="https://console.anthropic.com/settings/keys" target="_blank" rel="noopener"
         style={{ color: 'var(--accent2)' }}>console.anthropic.com</a>.
    </>
  );

  const modelPlaceholder =
    provider === 'github' ? 'gpt-4o (default)' :
    provider === 'openai' ? 'gpt-4o-mini / llama3.1 / qwen2.5-coder …' :
    'claude-sonnet-4-6 (default)';

  const providerLabel =
    provider === 'github' ? 'GitHub Copilot' :
    provider === 'openai' ? 'OpenAI-compatible' :
    'Anthropic';

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>AI Copilot</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Inline natural-language explanations for traces, Problems and exceptions.
        Pick a provider, paste your key, save — buttons appear automatically on the
        trace detail page and the Problems table. The OpenAI-compatible provider
        targets self-hosted local LLMs (Ollama / LM Studio / vLLM …) so traces
        never leave your perimeter.
      </p>

      <div className={`status-banner status-banner-${hasKey || (provider === 'openai' && baseUrl) ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${hasKey || (provider === 'openai' && baseUrl) ? 'operational' : 'degraded'}`}>
          {hasKey || (provider === 'openai' && baseUrl) ? 'CONFIGURED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {hasKey
            ? `Provider: ${providerLabel} — ready.`
            : provider === 'openai' && baseUrl
              ? `Provider: ${providerLabel} (no auth) — ready at ${baseUrl}.`
              : 'Not configured. Paste a key (or set a local endpoint URL) below.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Provider</div>
          <select value={provider}
                  onChange={e => setProvider(e.target.value as AIProvider)}
                  style={{ width: '100%' }}>
            <option value="anthropic">Anthropic (Claude)</option>
            <option value="github">GitHub Copilot</option>
            <option value="openai">OpenAI-compatible (Ollama / LM Studio / vLLM / OpenAI)</option>
          </select>
        </label>

        {/* Base URL — only meaningful for the openai provider. The
            field is rendered for all providers but the openai branch
            is the only one that consumes it server-side; harmless
            otherwise (saved + ignored). Keeps the form layout
            stable when switching providers. */}
        {provider === 'openai' && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              Base URL
            </div>
            <input value={baseUrl} onChange={e => setBaseUrl(e.target.value)}
                   placeholder="http://ollama:11434/v1   (or https://api.openai.com/v1 for real OpenAI)"
                   autoComplete="off" style={{ width: '100%', fontFamily: 'monospace' }} />
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>
              Endpoint must serve <code>/chat/completions</code> in OpenAI's request shape.
              Common paths: Ollama → <code>http://&lt;host&gt;:11434/v1</code>,
              LM Studio → <code>http://&lt;host&gt;:1234/v1</code>,
              vLLM → <code>http://&lt;host&gt;:8000/v1</code>.
            </div>
          </label>
        )}

        <label style={{ display: 'block', marginBottom: 6 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            API key {hasKey && <span style={{ color: 'var(--text3)' }}>(saved — leave empty to keep current)</span>}
            {provider === 'openai' && (
              <span style={{ color: 'var(--text3)' }}> (optional for local endpoints)</span>
            )}
          </div>
          <input type="password" value={apiKey} onChange={e => setApiKey(e.target.value)}
                 placeholder={hasKey ? '••••••••••••••••' :
                   provider === 'github' ? 'ghu_…' :
                   provider === 'openai' ? 'sk-… (optional)' : 'sk-ant-…'}
                 autoComplete="off" style={{ width: '100%' }} />
        </label>
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 14, lineHeight: 1.5 }}>
          {keyHint}
        </div>

        <label style={{ display: 'block', marginBottom: 14 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Model (optional)</div>
          <input value={model} onChange={e => setModel(e.target.value)}
                 placeholder={modelPlaceholder} style={{ width: '100%' }} />
        </label>

        {msg && (
          <div style={{
            marginBottom: 12, padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <button type="submit" disabled={busy || (!apiKey && !hasKey)}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          {hasKey && (
            <button type="button" className="sec" onClick={clearKey} disabled={busy}
                    style={{ color: 'var(--err)' }}>
              Remove key
            </button>
          )}
        </div>
      </form>

      {hasKey && (
        <div style={{ marginTop: 18, padding: 16, borderRadius: 8,
          background: 'var(--bg2)', border: '1px solid var(--border)' }}>
          <h3 style={{ fontSize: 13, fontWeight: 600, marginBottom: 8 }}>What it does</h3>
          <ul style={{ fontSize: 13, lineHeight: 1.7, color: 'var(--text)', paddingLeft: 18 }}>
            <li><b><IconSparkles /> Explain this trace</b> — on any trace detail page.</li>
            <li><b><IconSparkles /></b> column on the <a href="/problems" style={{ color: 'var(--accent2)' }}>Problems</a> page —
              plain-language meaning + ranked likely causes + first three things to check.</li>
          </ul>
        </div>
      )}
    </div>
  );
}

// LDAPTab — enterprise auth configuration. Three sections:
//   1. Connection — host/port/TLS/bind credentials (Test button verifies
//      the service-account bind without saving).
//   2. Group → Role mapping — admin lists AD groups whose members
//      should land as admin / editor / viewer in Coremetry. First
//      match wins (admin > editor > viewer).
//   3. Directory user picker — search the LDAP for a user, pick a
//      role, post to /api/users/from-ldap to pre-provision them so
//      they show up in /users with explicit role even before first
//      sign-in.
//
// Bind password is never echoed back from the API; the GET endpoint
// returns the literal "__SET__" sentinel when one is saved, which we
// translate into a "leave empty to keep current" affordance.
function LDAPTab() {
  const [cfg, setCfg] = useState<LDAPConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // Track whether the bind password input has been touched. We wipe
  // the "__SET__" sentinel on first focus so the user types a fresh
  // value into an empty box, not on top of the placeholder.
  const [pwTouched, setPwTouched] = useState(false);

  useEffect(() => {
    api.getLDAPSettings().then(c => setCfg(c)).catch(() => setCfg(emptyLDAP()));
  }, []);

  if (!cfg) return <Spinner />;

  const update = (patch: Partial<LDAPConfig>) => setCfg({ ...cfg, ...patch });

  // Strip the "__SET__" sentinel before any outbound call. Server
  // treats empty BindPassword as "preserve saved one".
  const outbound = (): LDAPConfig => {
    const c = { ...cfg };
    if (!pwTouched && c.bindPassword === '__SET__') c.bindPassword = '';
    return c;
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putLDAPSettings(outbound());
      setCfg(next);
      setPwTouched(false);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.testLDAPConnection(outbound());
      if (r.ok) setMsg({ kind: 'ok', text: 'Connection OK — bind succeeded.' });
      else setMsg({ kind: 'err', text: r.error || 'Connection failed' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Test failed' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ maxWidth: 800 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>LDAP / Active Directory</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Authenticate users against your corporate directory. Domain users sign in
        with their normal credentials; their Coremetry role is resolved from AD
        group membership via the mapping table below. Local accounts (bootstrap
        admin) keep working as a fallback.
      </p>

      <form onSubmit={save} style={{
        padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        {/* ── Section 1: Enable + connection ─────────────────────────── */}
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
          <input type="checkbox" checked={cfg.enabled}
                 onChange={e => update({ enabled: e.target.checked })} />
          <span style={{ fontSize: 13, fontWeight: 600 }}>Enable LDAP authentication</span>
        </label>

        <SectionTitle>Connection</SectionTitle>
        <Row>
          <Field2 label="Host" hint="e.g. ldap.corp.example.com">
            <input value={cfg.host} onChange={e => update({ host: e.target.value })}
                   placeholder="ldap.example.com" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Port" hint="636 for LDAPS, 389 for plain/StartTLS" small>
            <input type="number" value={cfg.port || ''}
                   onChange={e => update({ port: parseInt(e.target.value, 10) || 0 })}
                   placeholder="636" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.useTLS}
                   onChange={e => update({ useTLS: e.target.checked, startTLS: e.target.checked ? false : cfg.startTLS })} />
            Use LDAPS (direct TLS, default port 636)
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.startTLS} disabled={cfg.useTLS}
                   onChange={e => update({ startTLS: e.target.checked })} />
            StartTLS upgrade (legacy, port 389)
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.skipVerify}
                   onChange={e => update({ skipVerify: e.target.checked })} />
            Skip cert verification (dev only)
          </label>
        </Row>
        <Field2 label="Internal CA certificate (PEM, optional)"
                hint="Paste the PEM bundle if your AD uses an internal CA. Leave empty to use system roots.">
          <textarea value={cfg.caCert || ''} onChange={e => update({ caCert: e.target.value })}
                    rows={3}
                    style={{ width: '100%', fontFamily: 'monospace', fontSize: 11 }}
                    placeholder="-----BEGIN CERTIFICATE-----..." />
        </Field2>

        <Row>
          <Field2 label="Bind DN (service account)" hint="Used to look up users + groups">
            <input value={cfg.bindDN} onChange={e => update({ bindDN: e.target.value })}
                   placeholder="CN=svc-coremetry,OU=Service Accounts,DC=corp,DC=example"
                   style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="Bind password"
                  hint={cfg.bindPassword === '__SET__' && !pwTouched
                    ? 'A password is saved. Leave empty to keep it; type a new one to rotate.'
                    : 'Service-account password.'}>
            <input type="password"
                   value={pwTouched ? cfg.bindPassword : (cfg.bindPassword === '__SET__' ? '' : cfg.bindPassword)}
                   onFocus={() => { if (!pwTouched && cfg.bindPassword === '__SET__') { setPwTouched(true); update({ bindPassword: '' }); } }}
                   onChange={e => { setPwTouched(true); update({ bindPassword: e.target.value }); }}
                   placeholder={cfg.bindPassword === '__SET__' && !pwTouched ? '••••••••••••' : ''}
                   autoComplete="off" style={{ width: '100%' }} />
          </Field2>
        </Row>

        {/* ── Section 2: Search ──────────────────────────────────────── */}
        <SectionTitle>User search</SectionTitle>
        <Row>
          <Field2 label="Base DN" hint="Where to search for users">
            <input value={cfg.baseDN} onChange={e => update({ baseDN: e.target.value })}
                   placeholder="OU=Users,DC=corp,DC=example" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="User search filter" hint="{{username}} is replaced at runtime">
            <input value={cfg.userSearchFilter}
                   onChange={e => update({ userSearchFilter: e.target.value })}
                   placeholder="(sAMAccountName={{username}})" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="User attribute" small>
            <input value={cfg.userAttribute} onChange={e => update({ userAttribute: e.target.value })}
                   placeholder="sAMAccountName" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Email attribute" small>
            <input value={cfg.emailAttribute} onChange={e => update({ emailAttribute: e.target.value })}
                   placeholder="mail" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Display attribute" small>
            <input value={cfg.displayAttribute} onChange={e => update({ displayAttribute: e.target.value })}
                   placeholder="displayName" style={{ width: '100%' }} />
          </Field2>
        </Row>

        {/* ── Section 3: Groups ──────────────────────────────────────── */}
        <SectionTitle>Group lookup (optional)</SectionTitle>
        <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          Some directories don't populate <code>memberOf</code> on user entries.
          Set a group base + filter to do a separate group search; otherwise leave blank.
        </p>
        <Row>
          <Field2 label="Group search base">
            <input value={cfg.groupSearchBase}
                   onChange={e => update({ groupSearchBase: e.target.value })}
                   placeholder="OU=Groups,DC=corp,DC=example" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="Group filter" hint="{{userDN}} is replaced at runtime">
            <input value={cfg.groupFilter} onChange={e => update({ groupFilter: e.target.value })}
                   placeholder="(member={{userDN}})" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <label style={{
            display: 'flex', alignItems: 'flex-start', gap: 8,
            fontSize: 12, cursor: 'pointer', padding: '8px 0',
          }}>
            <input type="checkbox"
              checked={!!cfg.skipMemberOfFetch}
              onChange={e => update({ skipMemberOfFetch: e.target.checked })}
              style={{ marginTop: 2 }} />
            <span>
              <span style={{ fontWeight: 600 }}>
                Skip memberOf in user search (AD MaxValRange / 1MB workaround)
              </span>
              <span style={{ display: 'block', color: 'var(--text3)', fontSize: 11, marginTop: 2, lineHeight: 1.5 }}>
                Set when users with very large nested-group memberships trip
                AD's <code>MaxValRange</code> (1500) or <code>MaxReceiveBuffer</code>
                (~1MB) caps and login fails with "size limit" / "1MB area" /
                <code>LDAP_ADMIN_LIMIT_EXCEEDED</code>. Drops the
                <code>memberOf</code> attribute from the user search; the
                separate group search (above) then becomes the authoritative
                role source. <b>Requires Group search base + filter to be set.</b>
              </span>
            </span>
          </label>
        </Row>

        {/* ── Section 4: Role mapping ────────────────────────────────── */}
        <SectionTitle>Group → role mapping</SectionTitle>
        <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          When a user signs in, we walk their group memberships and pick the
          highest-privilege match. Group string can be a full DN or a CN
          fragment — match is case-insensitive substring.
        </p>
        <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse', marginBottom: 8 }}>
          <thead>
            <tr style={{ background: 'var(--bg)', color: 'var(--text2)' }}>
              <th style={{ padding: 6, textAlign: 'left' }}>Group (DN or CN substring)</th>
              <th style={{ padding: 6, textAlign: 'left', width: 160 }}>Role</th>
              <th style={{ padding: 6, width: 32 }}></th>
            </tr>
          </thead>
          <tbody>
            {(cfg.groupRoleMap || []).map((m, i) => (
              <tr key={i}>
                <td style={{ padding: 4 }}>
                  <input value={m.group}
                         onChange={e => updateMapping(cfg, setCfg, i, { group: e.target.value })}
                         placeholder="CN=Coremetry-Admins,OU=Groups,DC=corp,DC=example"
                         style={{ width: '100%' }} />
                </td>
                <td style={{ padding: 4 }}>
                  <select value={m.role}
                          onChange={e => updateMapping(cfg, setCfg, i, { role: e.target.value as Role })}>
                    <option value="admin">admin</option>
                    <option value="editor">editor</option>
                    <option value="viewer">viewer</option>
                  </select>
                </td>
                <td style={{ padding: 4, textAlign: 'center' }}>
                  <button type="button" className="sec"
                          onClick={() => removeMapping(cfg, setCfg, i)}
                          style={{ padding: '2px 8px', color: 'var(--err)' }}>×</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <button type="button" className="sec"
                onClick={() => addMapping(cfg, setCfg)}
                style={{ padding: '4px 10px', fontSize: 12 }}>
          + Add mapping
        </button>

        <Row>
          <Field2 label="Default role" hint="Applied when no group matches">
            <select value={cfg.defaultRole}
                    onChange={e => update({ defaultRole: e.target.value as Role })}
                    style={{ width: '100%' }}>
              <option value="viewer">viewer</option>
              <option value="editor">editor</option>
              <option value="admin">admin</option>
            </select>
          </Field2>
        </Row>

        {msg && (
          <div style={{
            margin: '14px 0 12px', padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
          <button type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          <button type="button" className="sec" disabled={busy} onClick={test}>
            Test connection
          </button>
        </div>
      </form>

      {cfg.enabled && (
        <LDAPUserPicker />
      )}
    </div>
  );
}

function emptyLDAP(): LDAPConfig {
  return {
    enabled: false, host: '', port: 636, useTLS: true, startTLS: false,
    skipVerify: false, caCert: '',
    bindDN: '', bindPassword: '', baseDN: '',
    userSearchFilter: '(sAMAccountName={{username}})',
    userAttribute: 'sAMAccountName', emailAttribute: 'mail',
    displayAttribute: 'displayName',
    groupSearchBase: '', groupFilter: '(member={{userDN}})',
    defaultRole: 'viewer', groupRoleMap: [],
  };
}

function updateMapping(
  cfg: LDAPConfig, set: (c: LDAPConfig) => void,
  i: number, patch: Partial<LDAPGroupRoleMapping>,
) {
  const next = [...cfg.groupRoleMap];
  next[i] = { ...next[i], ...patch };
  set({ ...cfg, groupRoleMap: next });
}
function addMapping(cfg: LDAPConfig, set: (c: LDAPConfig) => void) {
  set({ ...cfg, groupRoleMap: [...cfg.groupRoleMap, { group: '', role: 'viewer' }] });
}
function removeMapping(cfg: LDAPConfig, set: (c: LDAPConfig) => void, i: number) {
  set({ ...cfg, groupRoleMap: cfg.groupRoleMap.filter((_, j) => j !== i) });
}

// LDAPUserPicker — admin types a name/email, hits Search, picks a
// directory entry, picks a role, hits Provision. Pre-creates the
// users row so first-login lands them with the right access without
// having to wait for the group mapping to apply.
function LDAPUserPicker() {
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
        <button type="submit" disabled={busy}>{busy ? 'Searching…' : 'Search'}</button>
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
                  <button className="sec" type="button"
                          onClick={() => setProvisionFor(u)}
                          style={{ padding: '2px 8px', fontSize: 11 }}>
                    Provision
                  </button>
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
              <button type="button" className="sec" onClick={() => setProvisionFor(null)}>Cancel</button>
              <button type="button" onClick={provision} disabled={busy}>
                {busy ? 'Saving…' : 'Provision'}
              </button>
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

// ── shared form atoms (LDAP tab only — keep here so they don't drift
//    away from their consumer).
function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      marginTop: 16, marginBottom: 8,
      fontSize: 12, fontWeight: 600, color: 'var(--text2)',
      textTransform: 'uppercase', letterSpacing: '0.5px',
    }}>{children}</div>
  );
}
function LDAPRow({ children }: { children: React.ReactNode }) {
  return <div style={{ display: 'flex', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>{children}</div>;
}
function Field2({ label, hint, small, children }: {
  label: string; hint?: string; small?: boolean; children: React.ReactNode;
}) {
  return (
    <div style={{ flex: small ? '0 1 180px' : 1, minWidth: 200 }}>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.4 }}>{hint}</div>}
    </div>
  );
}

// RetentionTab — per-signal TTL controls. Each signal (spans / logs /
// metrics / profiles) takes a number + unit (hours / days). Save calls
// PUT /api/settings/retention which runs ALTER TABLE ... MODIFY TTL on
// the underlying ClickHouse tables. Effect is online — ClickHouse
// re-evaluates TTL on next merge so deletions catch up within ~10 min.
function RetentionTab() {
  type Unit = 'h' | 'd';
  type Row = { value: string; unit: Unit };
  const empty: Row = { value: '', unit: 'd' };
  const [spans,    setSpans]    = useState<Row>(empty);
  const [logs,     setLogs]     = useState<Row>(empty);
  const [metrics,  setMetrics]  = useState<Row>(empty);
  const [profiles, setProfiles] = useState<Row>(empty);
  const [busy, setBusy] = useState(false);
  const [msg,  setMsg]  = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const decode = (s?: string): Row => {
    const m = s?.match(/^(\d+)([hd])$/);
    return m ? { value: m[1], unit: m[2] as Unit } : empty;
  };
  const encode = (r: Row): string => r.value ? `${r.value}${r.unit}` : '';

  useEffect(() => {
    api.getRetention().then(sp => {
      setSpans(decode(sp.spans));
      setLogs(decode(sp.logs));
      setMetrics(decode(sp.metrics));
      setProfiles(decode(sp.profiles));
    }).catch(() => {});
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await api.putRetention({
        spans:    encode(spans),
        logs:     encode(logs),
        metrics:  encode(metrics),
        profiles: encode(profiles),
      });
      setMsg({ kind: 'ok', text: 'Applied — ClickHouse will re-evaluate TTL on next merge (~10 min).' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} style={{ maxWidth: 560 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Data retention</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Per-signal TTL on the underlying ClickHouse tables. Older data is dropped
        on the next merge cycle. Leave a field blank to keep the current value
        (initial defaults come from the config file: spans 30d, logs 30d,
        metrics 7d, profiles 7d).
      </p>

      <RetentionRow label="Spans"    row={spans}    setRow={setSpans} />
      <RetentionRow label="Logs"     row={logs}     setRow={setLogs} />
      <RetentionRow label="Metrics"  row={metrics}  setRow={setMetrics} />
      <RetentionRow label="Profiles" row={profiles} setRow={setProfiles} />

      <div style={{ marginTop: 14, display: 'flex', gap: 8, alignItems: 'center' }}>
        <button type="submit" disabled={busy}>{busy ? 'Applying…' : 'Apply'}</button>
        {msg && (
          <span style={{ fontSize: 12, color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </span>
        )}
      </div>

      <p style={{ marginTop: 18, fontSize: 11, color: 'var(--text3)' }}>
        Hour-precision TTL is supported (e.g. <code>36h</code>) but ClickHouse
        partitions data per day, so very short retention windows still
        process at day-boundary granularity. Examples: <code>48h</code> = last 2 days,
        <code> 2d</code> = same thing, <code>30d</code> = last 30 days.
      </p>
    </form>
  );
}

function RetentionRow({ label, row, setRow }: {
  label: string;
  row: { value: string; unit: 'h' | 'd' };
  setRow: (r: { value: string; unit: 'h' | 'd' }) => void;
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 10 }}>
      <span style={{ width: 90, fontSize: 13 }}>{label}</span>
      <input type="number" min={1} value={row.value}
        onChange={e => setRow({ ...row, value: e.target.value })}
        placeholder="(unchanged)"
        style={{ width: 100 }} />
      <select value={row.unit}
        onChange={e => setRow({ ...row, unit: e.target.value as 'h' | 'd' })}>
        <option value="h">hours</option>
        <option value="d">days</option>
      </select>
    </div>
  );
}

function humanize(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') return j.error;
  } catch {}
  return body || msg;
}

// ── Anomaly promotion tab ───────────────────────────────────────
//
// Tunes the evaluator's anomaly auto-promotion (v0.5.59). The
// detector continuously flags "pattern X is occurring N× more
// than baseline" rows on /anomalies; when they sustain past
// the configured thresholds the evaluator graduates them to
// first-class Problems so the existing notify pipeline pages
// the on-call. Master enable flag lets operators kill the
// feature for a chatty detector without changing thresholds.
function AnomalyPromotionTab() {
  type Cfg = {
    enabled: boolean; minPeakRatio: number;
    minSustainedSec: number; minCount: number;
  };
  const [cfg, setCfg] = useState<Cfg | null>(null);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getAnomalyPromotion()
      .then(c => setCfg(c))
      .catch(err => setFlash({ kind: 'err', text: humanize(err) }));
  }, []);

  const save = async () => {
    if (!cfg) return;
    setBusy(true); setFlash(null);
    try {
      const saved = await api.putAnomalyPromotion(cfg);
      setCfg(saved);
      setFlash({ kind: 'ok', text: 'Saved — next evaluator tick picks it up automatically.' });
    } catch (err) {
      setFlash({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  if (!cfg) {
    return (
      <div style={{ maxWidth: 640 }}>
        <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Anomaly auto-promotion</h2>
        {flash ? <FlashBox kind={flash.kind}>{flash.text}</FlashBox> : <Spinner />}
      </div>
    );
  }

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Anomaly auto-promotion</h2>
      <p style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 18, lineHeight: 1.55 }}>
        The anomaly detector flags patterns that exceed their rolling baseline; this
        promoter graduates the strong, sustained ones into first-class Problems so
        the on-call pager fires. Tighten the thresholds when the detector is too
        chatty, or disable the whole feature while you calibrate it.
      </p>

      <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}>
        <input type="checkbox" checked={cfg.enabled}
          onChange={e => setCfg({ ...cfg, enabled: e.target.checked })} />
        <span style={{ fontSize: 13, color: 'var(--text)' }}>
          Promote strong anomalies into Problems
        </span>
      </label>

      <div style={{ display: 'grid', gap: 12, opacity: cfg.enabled ? 1 : 0.5 }}>
        <Field label="Minimum peak ratio (× baseline)">
          <input type="number" min={1} max={1000} step={0.5}
            value={cfg.minPeakRatio}
            onChange={e => setCfg({ ...cfg, minPeakRatio: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            5× = pattern is occurring at least 5 times more than its
            rolling baseline. Default 5.
          </div>
        </Field>

        <Field label="Minimum sustained (seconds since started_at)">
          <input type="number" min={60} max={86400} step={60}
            value={cfg.minSustainedSec}
            onChange={e => setCfg({ ...cfg, minSustainedSec: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Filters out one-tick flares. Default 300s (5 min).
          </div>
        </Field>

        <Field label="Minimum count">
          <input type="number" min={1} max={1000000} step={1}
            value={cfg.minCount}
            onChange={e => setCfg({ ...cfg, minCount: Number(e.target.value) })}
            disabled={!cfg.enabled} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Absolute volume floor — a 100× ratio on 2 occurrences is
            meaningless. Default 10.
          </div>
        </Field>
      </div>

      <div style={{ marginTop: 18, display: 'flex', gap: 8, alignItems: 'center' }}>
        <button onClick={save} disabled={busy}
          style={{ padding: '6px 16px', fontSize: 13 }}>
          {busy ? 'Saving…' : 'Save'}
        </button>
        {flash && <FlashBox kind={flash.kind}>{flash.text}</FlashBox>}
      </div>
    </div>
  );
}

// ── Sampling tab ────────────────────────────────────────────────────────────
//
// Hot-path policy editor. Default ratio applies to every service
// that doesn't have its own override; per-service rows let the
// admin pin a heavy service to 0.05 (drop 95%) while a low-volume
// one runs at 1.0. AlwaysKeep* are big-stick defaults — turning
// them off trades observability for raw storage savings, almost
// never worth it.
function SamplingTab() {
  const [s, setS] = useState<import('@/lib/types').SamplingSettings | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [newSvc, setNewSvc] = useState('');
  const [newRatio, setNewRatio] = useState('1');

  useEffect(() => {
    api.getSampling().then(d => setS(d ?? null)).catch(() => setS(null));
  }, []);

  if (s === undefined) return <Spinner />;
  if (s === null) {
    return <Empty icon="!" title="Failed to load sampling settings">
      Check that the backend is up and you have admin access.
    </Empty>;
  }

  const save = async () => {
    setBusy(true);
    try {
      const next = await api.putSampling({
        default:          s.default,
        services:         s.services,
        alwaysKeepErrors: s.alwaysKeepErrors,
        alwaysKeepRoots:  s.alwaysKeepRoots,
        tail:             s.tail,
      });
      setS(next);
    } catch (err) { alert(humanize(err)); }
    finally { setBusy(false); }
  };

  const updateTail = (partial: Partial<NonNullable<typeof s.tail>>) => {
    const cur = s.tail ?? { enabled: false, windowSec: 30, slowMs: 1000, maxTraces: 200_000 };
    setS({ ...s, tail: { ...cur, ...partial } });
  };

  const addOverride = () => {
    const r = parseFloat(newRatio);
    if (!newSvc.trim() || isNaN(r) || r < 0 || r > 1) return;
    setS({ ...s, services: { ...s.services, [newSvc.trim()]: r } });
    setNewSvc(''); setNewRatio('1');
  };
  const removeOverride = (svc: string) => {
    const next = { ...s.services };
    delete next[svc];
    setS({ ...s, services: next });
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <h3 style={{ marginTop: 0 }}>Trace sampling</h3>
      <p style={{ color: 'var(--text2)', fontSize: 12 }}>
        Head-sampling rules applied at OTLP ingest. Errors and root spans are
        always kept (toggle below). Probabilistic ratio applies to the rest;
        same trace_id always gets the same decision so partial traces don't
        leak through.
      </p>

      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12,
        display: 'grid', gridTemplateColumns: '180px 1fr', gap: '10px 14px',
        alignItems: 'center', fontSize: 13,
      }}>
        <label>Default ratio</label>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <input type="number" min={0} max={1} step={0.01}
                 value={s.default}
                 onChange={e => setS({ ...s, default: parseFloat(e.target.value) || 0 })}
                 style={{ width: 100 }} />
          <span style={{ color: 'var(--text3)', fontSize: 11 }}>
            (0 = drop all probabilistic spans · 1 = keep everything)
          </span>
        </div>

        <label>Always keep errors</label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={s.alwaysKeepErrors}
                 onChange={e => setS({ ...s, alwaysKeepErrors: e.target.checked })} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            status_code = ERROR spans bypass the ratio
          </span>
        </label>

        <label>Always keep roots</label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <input type="checkbox" checked={s.alwaysKeepRoots}
                 onChange={e => setS({ ...s, alwaysKeepRoots: e.target.checked })} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            parent_span_id == "" spans bypass the ratio (preserves RPS counts)
          </span>
        </label>
      </div>

      <h4 style={{ marginBottom: 8 }}>Per-service overrides</h4>
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12,
      }}>
        {Object.keys(s.services).length === 0 && (
          <div style={{ color: 'var(--text3)', fontSize: 12, marginBottom: 8 }}>
            No overrides — every service uses the default ratio above.
          </div>
        )}
        {Object.entries(s.services).map(([svc, r]) => (
          <div key={svc} style={{
            display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6,
          }}>
            <code style={{ flex: 1, fontSize: 12 }}>{svc}</code>
            <input type="number" min={0} max={1} step={0.01}
                   value={r}
                   onChange={e => setS({
                     ...s,
                     services: { ...s.services, [svc]: parseFloat(e.target.value) || 0 },
                   })}
                   style={{ width: 80 }} />
            <button className="sec" style={{ padding: '2px 8px', fontSize: 11 }}
                    onClick={() => removeOverride(svc)}>Remove</button>
          </div>
        ))}

        <div style={{
          display: 'flex', alignItems: 'center', gap: 8,
          borderTop: '1px solid var(--border)', paddingTop: 10, marginTop: 8,
        }}>
          <input value={newSvc} onChange={e => setNewSvc(e.target.value)}
                 placeholder="service-name" style={{ flex: 1 }} />
          <input type="number" min={0} max={1} step={0.01}
                 value={newRatio} onChange={e => setNewRatio(e.target.value)}
                 style={{ width: 80 }} />
          <button className="sec" style={{ padding: '4px 10px', fontSize: 12 }}
                  onClick={addOverride}>Add</button>
        </div>
      </div>

      <h4 style={{ marginBottom: 8 }}>Tail sampling (buffered)</h4>
      <div style={{
        background: 'var(--bg2)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 12, marginBottom: 12, fontSize: 13,
      }}>
        <p style={{ color: 'var(--text2)', fontSize: 12, marginTop: 0 }}>
          Buffers each trace for the decision window, then keeps it if any
          span had an error, the root duration exceeded the slow-trace
          threshold, or it falls under the probabilistic ratio. Late-
          arriving spans of decided traces follow the prior verdict.
        </p>
        <div style={{
          display: 'grid', gridTemplateColumns: '180px 1fr', gap: '10px 14px',
          alignItems: 'center',
        }}>
          <label>Enabled</label>
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <input type="checkbox" checked={s.tail?.enabled ?? false}
                   onChange={e => updateTail({ enabled: e.target.checked })} />
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              when on, head ratios are bypassed for traces — tail decides instead
            </span>
          </label>

          <label>Decision window</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={5} max={300}
                   value={s.tail?.windowSec ?? 30}
                   onChange={e => updateTail({ windowSec: parseInt(e.target.value) || 30 })}
                   style={{ width: 80 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>seconds (default 30)</span>
          </div>

          <label>Slow trace threshold</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={50} max={60000}
                   value={s.tail?.slowMs ?? 1000}
                   onChange={e => updateTail({ slowMs: parseInt(e.target.value) || 1000 })}
                   style={{ width: 80 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>
              ms (root duration above this = always keep)
            </span>
          </div>

          <label>Max in-flight traces</label>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="number" min={1000} max={1_000_000} step={1000}
                   value={s.tail?.maxTraces ?? 200_000}
                   onChange={e => updateTail({ maxTraces: parseInt(e.target.value) || 200_000 })}
                   style={{ width: 100 }} />
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>
              memory bound (~5 spans × 500 B per trace)
            </span>
          </div>
        </div>
        {s.tailStats && s.tailStats.enabled && (
          <div style={{
            marginTop: 10, padding: 8,
            background: 'var(--bg1)', borderRadius: 4,
            fontFamily: 'ui-monospace, monospace', fontSize: 11, color: 'var(--text3)',
          }}>
            open: {s.tailStats.openTraces.toLocaleString()} traces ·
            flushed: {s.tailStats.flushedSpans.toLocaleString()} spans ·
            dropped: {s.tailStats.droppedSpans.toLocaleString()} spans ·
            evicted: {s.tailStats.evictedTraces.toLocaleString()} traces
          </div>
        )}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <button onClick={save} disabled={busy}>
          {busy ? 'Saving…' : 'Save & apply'}
        </button>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          Head-stage drops since boot: <b>{s.droppedSinceBoot.toLocaleString()}</b>
        </span>
      </div>
    </div>
  );
}

// SSOPresetsTab — provider-template reference for OIDC + the
// trusted-header proxy mode. Today OIDC + trusted-header are
// configured via config.yaml / env vars (the runtime-persisted
// flow lives next to LDAP / AI / Branding and is queued for a
// follow-up); meanwhile operators paste a known-good snippet
// per provider here, apply it to their deployment, and
// restart the pod.
//
// Each card is a copy-paste-ready YAML block with the issuer
// URL pattern, recommended scopes, and any provider-specific
// notes (e.g. Azure AD's tenant placeholder, Keycloak's realm
// segment, oauth2-proxy's trusted-proxy CIDR). Same shape as
// the Profiling setup recipes that ship per-language.
function SSOPresetsTab() {
  type Preset = { key: string; label: string; description: string; yaml: string };
  const presets: Preset[] = [
    {
      key: 'keycloak',
      label: 'Keycloak',
      description: 'Most common self-hosted identity provider for banks. Replace <realm> with your realm name; Coremetry discovers the rest via /.well-known/openid-configuration.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://keycloak.example.com/realms/<realm>"
    client_id: "coremetry"
    client_secret: "<from-keycloak-client-credentials>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Keycloak"
    default_role: "viewer"
    allowed_domains: []   # optional ["bank.com"]`,
    },
    {
      key: 'dex',
      label: 'Dex',
      description: 'CoreOS Dex — popular OIDC bridge in front of LDAP/SAML/GitHub for k8s shops. Issuer URL is the public host:port the SPA can reach.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://dex.example.com"
    client_id: "coremetry"
    client_secret: "<dex-static-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile", "groups"]
    display_name: "Dex"
    default_role: "viewer"`,
    },
    {
      key: 'google',
      label: 'Google Workspace',
      description: 'Hosted Google. Restrict to a single GSuite domain via allowed_domains so anyone with a personal gmail.com can\'t sign in.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://accounts.google.com"
    client_id: "<google-cloud-oauth-client-id>"
    client_secret: "<google-cloud-oauth-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Google"
    default_role: "viewer"
    allowed_domains: ["yourcompany.com"]`,
    },
    {
      key: 'azure-ad',
      label: 'Azure AD (Entra)',
      description: 'Microsoft Entra ID (formerly Azure AD). Replace <tenant-id> with your tenant GUID; the v2.0 endpoint is the one to use.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://login.microsoftonline.com/<tenant-id>/v2.0"
    client_id: "<app-registration-client-id>"
    client_secret: "<app-registration-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Microsoft"
    default_role: "viewer"`,
    },
    {
      key: 'okta',
      label: 'Okta',
      description: 'Okta-as-a-service. Replace <your-okta-domain> with the host Okta assigned you (e.g. acme.okta.com).',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://<your-okta-domain>"
    client_id: "<okta-app-client-id>"
    client_secret: "<okta-app-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Okta"
    default_role: "viewer"`,
    },
    {
      key: 'auth0',
      label: 'Auth0',
      description: 'Hosted Auth0. Issuer URL includes the tenant slug.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://<your-tenant>.auth0.com/"
    client_id: "<auth0-application-client-id>"
    client_secret: "<auth0-application-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Auth0"
    default_role: "viewer"`,
    },
    {
      key: 'oauth2-proxy',
      label: 'oauth2-proxy / IAP (trusted headers)',
      description: 'Banks running oauth2-proxy / Google IAP / Cloudflare Access in front of every internal app — Coremetry trusts the upstream identity headers without re-doing OIDC itself. trusted_proxies CIDR is REQUIRED so an attacker bypassing the proxy can\'t spoof X-Auth-Request-Email.',
      yaml:
`auth:
  trusted_header:
    enabled: true
    email_header: "X-Auth-Request-Email"
    user_header: "X-Auth-Request-User"
    groups_header: "X-Auth-Request-Groups"
    auto_provision: true        # first-sight email lands as DefaultRole
    default_role: "viewer"
    trusted_proxies:            # ← REQUIRED — your oauth2-proxy node CIDRs
      - "10.0.0.0/8"
      - "172.16.0.0/12"`,
    },
  ];
  const [activeKey, setActiveKey] = useState(presets[0].key);
  const active = presets.find(p => p.key === activeKey) ?? presets[0];
  const [copied, setCopied] = useState(false);
  const copy = () => {
    navigator.clipboard.writeText(active.yaml)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => {
        // Clipboard API can reject when the page isn't in a
        // secure context, when the tab loses focus mid-call,
        // or when the user denied permission. Silent fail —
        // the operator still has the visible YAML to copy
        // manually. Without the catch this surfaces as an
        // unhandled promise rejection in the console.
      });
  };
  return (
    <div style={{ maxWidth: 920 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>SSO presets</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16, lineHeight: 1.6 }}>
        Provider-specific config snippets for OIDC + the oauth2-proxy trusted-header mode.
        Paste the YAML into your <code>config.yaml</code> (or the equivalent
        <code>COREMETRY_OIDC_*</code> / <code>COREMETRY_TRUSTED_HEADER_*</code> env vars in your
        deployment), then restart the pod. Live runtime persistence of OIDC config is queued for
        a follow-up — for now the file-driven path keeps things auditable in source control.
      </p>
      <div style={{ display: 'flex', gap: 4, marginBottom: 12, borderBottom: '1px solid var(--border)' }}>
        {presets.map(p => (
          <button key={p.key} onClick={() => setActiveKey(p.key)}
            style={{
              padding: '5px 14px', fontSize: 12, fontWeight: 600, cursor: 'pointer',
              background: 'transparent', border: 'none', borderBottom: '2px solid',
              borderColor: activeKey === p.key ? 'var(--accent)' : 'transparent',
              color: activeKey === p.key ? 'var(--text)' : 'var(--text3)',
            }}>
            {p.label}
          </button>
        ))}
      </div>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 10, lineHeight: 1.6 }}>
        {active.description}
      </div>
      <div style={{ position: 'relative' }}>
        <button onClick={copy} className="sec"
          style={{
            position: 'absolute', top: 8, right: 8, fontSize: 10, padding: '2px 8px',
            background: 'var(--bg3)',
          }}>
          {copied ? '✓ copied' : 'Copy'}
        </button>
        <pre style={{
          margin: 0, padding: 14, background: 'var(--bg)',
          border: '1px solid var(--border)', borderRadius: 6,
          fontSize: 12, lineHeight: 1.6, overflowX: 'auto',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>
          <code>{active.yaml}</code>
        </pre>
      </div>
      <div style={{
        marginTop: 14, padding: '10px 12px', borderRadius: 6,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        fontSize: 12, color: 'var(--text2)', lineHeight: 1.6,
      }}>
        <b>Notes:</b>
        <ul style={{ paddingLeft: 18, margin: '6px 0 0' }}>
          <li>Restart the pod after applying — OIDC discovery runs at boot.</li>
          <li>Local username/password login stays available alongside OIDC so admins always have a fallback.</li>
          <li>Trusted-header mode <b>requires</b> <code>trusted_proxies</code> — empty list = boot refused. Source-IP gate prevents header spoofing from any caller outside the proxy mesh.</li>
          <li>First-sight OIDC / trusted-header users land with <code>default_role</code> (viewer). Admins promote via <code>/users</code>.</li>
        </ul>
      </div>
    </div>
  );
}

// BrandingTab — white-label / customisation form. Admin paints the
// login page (logo + title + button label + footer) and the
// browser tab title. Everything is optional; an empty value
// reverts to the bundled Coremetry default. Saved overlay is
// applied immediately via invalidateBranding() so the operator
// doesn't have to reload to see the result.
//
// Logo upload reads the local file as a data URI and caps the
// raw size at 200 KB — big enough for a 200 px PNG, small
// enough that the system_settings row stays cheap to fetch on
// every login page render.
function BrandingTab() {
  const [loaded, setLoaded] = useState(false);
  const [b, setB] = useState<BrandingSettings>({});
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getBranding()
      .then(v => { setB(v ?? {}); setLoaded(true); })
      .catch(() => setLoaded(true));
  }, []);

  if (!loaded) return <Spinner />;

  const set = (k: keyof BrandingSettings, v: string) =>
    setB(prev => ({ ...prev, [k]: v }));

  const onLogo = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (!f) return;
    if (f.size > 200 * 1024) {
      setMsg({ kind: 'err', text: `Logo file too large (${(f.size / 1024).toFixed(0)} KB) — keep it under 200 KB.` });
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result;
      if (typeof result === 'string') {
        set('logoDataUri', result);
        setMsg(null);
      }
    };
    reader.onerror = () => setMsg({ kind: 'err', text: 'Failed to read file.' });
    reader.readAsDataURL(f);
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      await api.putBranding(b);
      await invalidateBranding();
      setMsg({ kind: 'ok', text: 'Saved — branding applied immediately.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const resetAll = async () => {
    if (!confirm('Reset all branding to the Coremetry defaults? Saved logo + custom strings will be cleared.')) return;
    setBusy(true); setMsg(null);
    try {
      await api.putBranding({});
      setB({});
      await invalidateBranding();
      setMsg({ kind: 'ok', text: 'Reset — Coremetry defaults restored.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Reset failed' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ maxWidth: 720 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Branding</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        White-label the login page + browser tab title. Empty fields fall back to the
        Coremetry defaults shown as placeholders. Changes apply immediately — no
        restart, no reload needed.
      </p>

      <form onSubmit={save} style={{
        padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <Row>
          <Field label="App name" flex={1}>
            <input value={b.appName ?? ''} onChange={e => set('appName', e.target.value)}
                   placeholder={DEFAULT_BRANDING.appName} style={{ width: '100%' }} />
          </Field>
          <Field label="Browser tab title" flex={1}>
            <input value={b.browserTitle ?? ''} onChange={e => set('browserTitle', e.target.value)}
                   placeholder={DEFAULT_BRANDING.browserTitle} style={{ width: '100%' }} />
          </Field>
        </Row>

        <Field label="Login page title">
          <input value={b.loginTitle ?? ''} onChange={e => set('loginTitle', e.target.value)}
                 placeholder="Sign in to Coremetry" style={{ width: '100%' }} />
        </Field>
        <Field label="Login subtitle (optional — shown under the title)">
          <textarea value={b.loginSubtitle ?? ''} onChange={e => set('loginSubtitle', e.target.value)}
                    placeholder='e.g. "Acme Bank observability. Access requires VPN."'
                    rows={2} style={{ width: '100%', resize: 'vertical' }} />
        </Field>

        <Row>
          <Field label="Sign-in button label" flex={1}>
            <input value={b.signInButtonLabel ?? ''} onChange={e => set('signInButtonLabel', e.target.value)}
                   placeholder={DEFAULT_BRANDING.signInButtonLabel} style={{ width: '100%' }} />
          </Field>
          <Field label="Username field label" flex={1}>
            <input value={b.usernameLabel ?? ''} onChange={e => set('usernameLabel', e.target.value)}
                   placeholder='e.g. "Corporate ID" or "Domain user"' style={{ width: '100%' }} />
          </Field>
        </Row>

        <Field label="Footer text (small line at the bottom of the login card)">
          <input value={b.footerText ?? ''} onChange={e => set('footerText', e.target.value)}
                 placeholder='e.g. "© Acme Bank · Internal use only"' style={{ width: '100%' }} />
        </Field>

        <Field label="UI language">
          <select value={b.language ?? 'en'}
                  onChange={e => set('language', e.target.value)}
                  style={{ fontSize: 13, padding: '4px 8px', minWidth: 200 }}>
            <option value="en">English (default)</option>
            <option value="tr">Türkçe</option>
          </select>
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Drives sidebar labels, login strings, common buttons, page titles.
            Applies to every operator hitting this Coremetry instance.
          </div>
        </Field>

        <Field label="Primary color (CSS — e.g. #4f46e5, rgb(79,70,229))">
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <input value={b.primaryColor ?? ''} onChange={e => set('primaryColor', e.target.value)}
                   placeholder="leave empty for the bundled accent"
                   style={{ flex: 1 }} />
            {b.primaryColor && (
              <span style={{
                width: 32, height: 28, borderRadius: 4,
                background: b.primaryColor,
                border: '1px solid var(--border)',
              }} />
            )}
          </div>
        </Field>

        <Field label="Logo (PNG / SVG / JPG, ≤ 200 KB)">
          <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
            <input type="file" accept="image/png,image/svg+xml,image/jpeg,image/webp"
                   onChange={onLogo} />
            {b.logoDataUri && (
              <>
                <img src={b.logoDataUri} alt="logo preview"
                     style={{ maxHeight: 48, maxWidth: 140, objectFit: 'contain',
                              border: '1px solid var(--border)', borderRadius: 4,
                              padding: 4, background: 'var(--bg)' }} />
                <button type="button" className="sec"
                  onClick={() => set('logoDataUri', '')}
                  style={{ fontSize: 11, padding: '3px 8px', color: 'var(--err)' }}>
                  Remove
                </button>
              </>
            )}
          </div>
        </Field>

        {msg && (
          <div style={{
            marginTop: 14, padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 14 }}>
          <button type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
          <button type="button" className="sec" onClick={resetAll} disabled={busy}
                  style={{ color: 'var(--err)' }}>
            Reset to defaults
          </button>
        </div>
      </form>
    </div>
  );
}

// ZoomChannel mirrors the backend ZoomChannel struct.
interface ZoomChannelRow {
  id: string;
  jid: string;
  name: string;
  type?: number;
}

// ZoomChannelPicker — small button next to the Channel ID input
// that fetches every channel the configured S2S OAuth app can
// see and opens a searchable picker. Removes the
// memorise-the-JID requirement: a Zoom workspace can have
// hundreds of channels, so the modal includes an inline search
// box that filters by name / id / JID as the operator types.
// Click a row to inject the JID into the form.
function ZoomChannelPicker({
  existingChannelId,
  accountId, clientId, clientSecret,
  oauthBaseUrl, apiBaseUrl, insecureSkipVerify, onPick,
}: {
  existingChannelId?: string;
  accountId: string;
  clientId: string;
  clientSecret: string;
  oauthBaseUrl: string;
  apiBaseUrl: string;
  insecureSkipVerify?: boolean;
  onPick: (jid: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [rows, setRows] = useState<ZoomChannelRow[] | null>(null);
  const [search, setSearch] = useState('');

  const canFetch = (
    // For an unsaved channel we need all three credential fields
    // inline; for an existing channel the saved (redacted)
    // secret can be reused server-side via existingChannelId.
    (accountId.trim() && clientId.trim() && clientSecret.trim()) ||
    !!existingChannelId
  );

  const load = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await fetch('/api/channels/zoom/list-channels', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          existingChannelId: existingChannelId,
          accountId: accountId.trim(),
          clientId: clientId.trim(),
          clientSecret: clientSecret.trim(),
          oauthBaseUrl: oauthBaseUrl.trim(),
          apiBaseUrl: apiBaseUrl.trim(),
          insecureSkipVerify: !!insecureSkipVerify,
        }),
      });
      if (!r.ok) {
        const body = await r.text();
        // Backend returns partial channels on truncation. Try to
        // surface those alongside the warning so the operator
        // can still pick from what we got.
        try {
          const parsed = JSON.parse(body);
          if (Array.isArray(parsed.channels) && parsed.channels.length > 0) {
            setRows(parsed.channels);
          }
          setErr(parsed.error ?? `HTTP ${r.status}`);
        } catch {
          setErr(`HTTP ${r.status}: ${body.slice(0, 200)}`);
        }
        return;
      }
      const j = await r.json();
      setRows(j.channels ?? []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const onOpen = () => {
    setOpen(true);
    if (!rows && !busy) load();
  };

  const filtered = (rows ?? []).filter(r => {
    if (!search.trim()) return true;
    const t = search.toLowerCase();
    return (
      r.name.toLowerCase().includes(t) ||
      r.jid.toLowerCase().includes(t) ||
      r.id.toLowerCase().includes(t)
    );
  });

  const channelType = (t?: number) =>
    t === 1 ? 'DM'
    : t === 2 ? 'Group'
    : t === 3 ? 'Public'
    : t === 4 ? 'Private'
    : '—';

  return (
    <>
      <button type="button" className="sec"
        disabled={!canFetch}
        title={canFetch
          ? 'List channels via the configured S2S OAuth app'
          : 'Enter Account ID / Client ID / Client Secret (or save first), then try again'}
        onClick={onOpen}
        style={{ whiteSpace: 'nowrap', fontSize: 12 }}>
        List my channels…
      </button>
      {open && (
        <div onClick={() => setOpen(false)} style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
          display: 'grid', placeItems: 'center', zIndex: 250,
        }}>
          <div onClick={e => e.stopPropagation()} style={{
            width: 720, maxWidth: '94vw', maxHeight: '82vh',
            display: 'flex', flexDirection: 'column',
            padding: 18, borderRadius: 8,
            background: 'var(--bg2)', border: '1px solid var(--border)',
          }}>
            <div style={{
              display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 10,
            }}>
              <div style={{ fontSize: 14, fontWeight: 700 }}>
                Pick a Zoom channel
              </div>
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                {rows ? `${filtered.length} of ${rows.length}` : ''}
              </span>
              <span style={{ marginLeft: 'auto' }}>
                <button type="button" className="sec" disabled={busy}
                  onClick={load} style={{ marginRight: 8, fontSize: 11 }}>
                  Refresh
                </button>
                <button type="button" onClick={() => setOpen(false)}
                  style={{ fontSize: 12 }}>Close</button>
              </span>
            </div>

            <input value={search} onChange={e => setSearch(e.target.value)}
              placeholder="Filter by name, ID, or JID…"
              autoFocus
              style={{ marginBottom: 10, fontSize: 13 }} />

            {busy && <div style={{ fontSize: 12, color: 'var(--text3)' }}>Loading channels…</div>}
            {err && (
              <div style={{
                fontSize: 11, color: 'var(--err)', padding: '6px 8px',
                borderRadius: 4, marginBottom: 8,
                background: 'rgba(220,38,38,0.08)', border: '1px solid rgba(220,38,38,0.3)',
              }}>{err}</div>
            )}
            {rows && rows.length === 0 && !busy && !err && (
              <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                No channels visible to this S2S app. The bot user must be a
                member of the channel for it to appear here.
              </div>
            )}

            <div style={{ flex: 1, overflowY: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
              <table style={{ width: '100%' }}>
                <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
                  <tr>
                    <th style={{ textAlign: 'left' }}>Name</th>
                    <th style={{ textAlign: 'left' }}>Type</th>
                    <th style={{ textAlign: 'left' }}>JID</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map(r => (
                    <tr key={r.id || r.jid}
                      onClick={() => { onPick(r.jid); setOpen(false); }}
                      style={{ cursor: 'pointer' }}
                      onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg3)')}
                      onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                      <td style={{ fontSize: 12, fontWeight: 600 }}>{r.name || '(unnamed)'}</td>
                      <td style={{
                        fontSize: 10, color: 'var(--text3)',
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      }}>{channelType(r.type)}</td>
                      <td style={{
                        fontSize: 11, color: 'var(--text2)',
                        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      }}>{r.jid}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 8 }}>
              The bot user behind your S2S OAuth app must be a member of a channel
              for it to appear in this list. If a channel is missing, add the bot
              from the Zoom web UI (channel settings → People → Add) and click
              Refresh.
            </div>
          </div>
        </div>
      )}
    </>
  );
}

// TempoTab — external Grafana Tempo backend (v0.5.208). When
// configured, /api/traces/{id} falls back to Tempo on a CH miss,
// so operators running Coremetry at low sampling + Tempo at 100%
// retention can still resolve long-tail trace IDs in the same
// /trace URL the rest of the UI links to. Admin-only — the saved
// token reads every trace in the operator's Tempo cluster.
function TempoTab() {
  const [loaded, setLoaded] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [baseUrl, setBaseUrl] = useState('');
  const [authType, setAuthType] = useState<TempoAuthType>('none');
  const [username, setUsername] = useState('');
  const [orgId, setOrgId] = useState('');
  const [token, setToken] = useState('');
  const [hasToken, setHasToken] = useState(false);
  const [insecureSkipVerify, setInsecureSkipVerify] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getTempoSettings().then(s => {
      setEnabled(s.enabled);
      setBaseUrl(s.baseUrl || '');
      setAuthType((s.authType || 'none') as TempoAuthType);
      setUsername(s.username || '');
      setOrgId(s.orgId || '');
      setHasToken(s.hasToken);
      setInsecureSkipVerify(!!s.insecureSkipVerify);
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putTempoSettings({
        enabled, baseUrl, authType,
        token, // empty preserved on the server side
        username, orgId, insecureSkipVerify,
      });
      setHasToken(next.hasToken);
      setToken('');
      setMsg({ kind: 'ok',
        text: next.enabled
          ? 'Saved — Tempo fallback live for trace-by-id lookups.'
          : 'Saved — Tempo disabled.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const clearToken = async () => {
    if (!confirm('Remove the saved Tempo token? Lookups will fail with 401 until a new one is set.')) return;
    setBusy(true); setMsg(null);
    try {
      // Server contract: empty token = preserve. To explicitly
      // CLEAR we send a sentinel and the server compares cur vs
      // payload. Without a sentinel, the simplest path is to
      // flip authType to "none" — drops the Authorization
      // header even if the token is still stored. That's enough
      // for "stop using my creds".
      const next = await api.putTempoSettings({
        enabled, baseUrl, authType: 'none',
        username, orgId, insecureSkipVerify,
      });
      setAuthType('none');
      setHasToken(next.hasToken);
      setMsg({ kind: 'ok', text: 'Auth disabled — lookups now go anonymously.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Clear failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  const ready = enabled && baseUrl.trim().length > 0;

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>External Tempo backend</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Use case: Coremetry at low sampling (e.g. 5%) for fast hot-path
        observability + an external Grafana Tempo cluster at 100% retention
        for forensics. When a trace ID isn't in Coremetry's store,
        <code style={{ background: 'var(--bg0)', padding: '1px 5px', borderRadius: 3, margin: '0 4px' }}>/trace?id=…</code>
        silently falls back to Tempo. The trace renders in the same
        waterfall with a small banner so it's clear where the data came from.
        Trace-by-id only — search / aggregations / topology still hit Coremetry.
      </p>

      <div className={`status-banner status-banner-${ready ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${ready ? 'operational' : 'degraded'}`}>
          {ready ? 'ENABLED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {ready
            ? `Pointing at ${baseUrl}${orgId ? ` (orgId=${orgId})` : ''}.`
            : 'Disabled — CH misses return empty without trying Tempo.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={enabled}
            onChange={e => setEnabled(e.target.checked)} />
          <span style={{ fontSize: 13 }}>Enable Tempo fallback</span>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Base URL</div>
          <input value={baseUrl}
            onChange={e => setBaseUrl(e.target.value)}
            placeholder="https://tempo.example.com  ·  Grafana Cloud: https://tempo-prod-XX.grafana.net/tempo"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Trailing slash optional. We call <code>{`{baseUrl}/api/traces/{id}`}</code> with
            <code style={{ marginLeft: 4 }}>Accept: application/json</code>.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Auth</div>
          <select value={authType}
            onChange={e => setAuthType(e.target.value as TempoAuthType)}
            style={{ width: '100%' }}>
            <option value="none">None (open Tempo behind VPN / mTLS)</option>
            <option value="bearer">Bearer token (Grafana Cloud API key)</option>
            <option value="basic">Basic auth (self-hosted + nginx htpasswd)</option>
          </select>
        </label>

        {authType === 'basic' && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Username</div>
            <input value={username}
              onChange={e => setUsername(e.target.value)}
              style={{ width: '100%' }} />
          </label>
        )}

        {(authType === 'bearer' || authType === 'basic') && (
          <label style={{ display: 'block', marginBottom: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              {authType === 'bearer' ? 'Bearer token' : 'Password'}
              {hasToken && <span style={{ color: 'var(--ok)', marginLeft: 8 }}>· stored</span>}
            </div>
            <input type="password" value={token}
              onChange={e => setToken(e.target.value)}
              placeholder={hasToken ? '(leave empty to keep stored value)' : 'paste token…'}
              style={{ width: '100%' }} />
          </label>
        )}

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            X-Scope-OrgID (multi-tenant Tempo / Grafana Cloud)
          </div>
          <input value={orgId}
            onChange={e => setOrgId(e.target.value)}
            placeholder="leave empty for single-tenant"
            style={{ width: '100%' }} />
        </label>

        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={insecureSkipVerify}
            onChange={e => setInsecureSkipVerify(e.target.checked)} />
          <span style={{ fontSize: 13 }}>
            Skip TLS verification
            <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
              (self-signed certs / POC only)
            </span>
          </span>
        </label>

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
          {hasToken && authType !== 'none' && (
            <Button type="button" variant="secondary" disabled={busy} onClick={clearToken}>
              Disable auth
            </Button>
          )}
        </div>
      </form>
    </div>
  );
}

// KibanaTab — external Kibana deep-link config (v0.5.236).
// Operator pastes the base URL of their Kibana install; the
// Logs page then renders an "Open in Kibana Discover" link
// per row + a global one in the topbar. Empty / disabled =
// no link rendered.
//
// OpenShift's "Discover in Kibana" pattern: pass a KQL clause
// + time bounds via the _g / _a state params so the Kibana
// landing surface matches the row's context.
function KibanaTab() {
  const [loaded, setLoaded] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [baseUrl, setBaseUrl] = useState('');
  const [dataView, setDataView] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  useEffect(() => {
    api.getKibanaSettings().then(s => {
      setEnabled(!!s.enabled);
      setBaseUrl(s.baseUrl || '');
      setDataView(s.dataView || '');
      setLoaded(true);
    }).catch(() => setLoaded(true));
  }, []);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next: KibanaSettings = { enabled, baseUrl, dataView: dataView || undefined };
      const r = await api.putKibanaSettings(next);
      setMsg({ kind: 'ok', text: r.enabled
        ? 'Saved — Kibana link is live on /logs.'
        : 'Saved — Kibana link disabled.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  if (!loaded) return <Spinner />;

  const ready = enabled && baseUrl.trim() !== '';

  return (
    <div style={{ maxWidth: 640 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>Kibana deep-link</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Operators who use Kibana alongside Coremetry can jump out
        to Kibana Discover with the current Logs filter pre-applied
        — same pattern as OpenShift's "Discover in Kibana" affordance.
        Coremetry never proxies Kibana; only mints the deep-link.
      </p>

      <div className={`status-banner status-banner-${ready ? 'operational' : 'degraded'}`}>
        <span className={`status-pill status-pill-${ready ? 'operational' : 'degraded'}`}>
          {ready ? 'ENABLED' : 'NOT CONFIGURED'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 14 }}>
          {ready
            ? `Logs page will render a Kibana link pointing at ${baseUrl}.`
            : 'Disabled — no Kibana link rendered on the Logs page.'}
        </span>
      </div>

      <form onSubmit={save} style={{
        marginTop: 18, padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <input type="checkbox" checked={enabled}
            onChange={e => setEnabled(e.target.checked)} />
          <span style={{ fontSize: 13 }}>Show "Open in Kibana" link on Logs</span>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Kibana base URL</div>
          <input value={baseUrl}
            onChange={e => setBaseUrl(e.target.value)}
            placeholder="https://kibana.example.com  (no trailing /app/...)"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Just the host (or host + path prefix if Kibana lives under one,
            e.g. <code>https://openshift-console.example.com/monitoring/kibana</code>).
            Coremetry appends <code>/app/discover#/?…</code>.
          </div>
        </label>

        <label style={{ display: 'block', marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
            Data view id <span style={{ color: 'var(--text3)' }}>(optional)</span>
          </div>
          <input value={dataView}
            onChange={e => setDataView(e.target.value)}
            placeholder="e.g. logs-*  or  the data-view UUID"
            style={{ width: '100%' }} />
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
            Pins the Discover panel to a specific index pattern. Empty =
            Kibana picks the default, fine for most single-pattern installs.
          </div>
        </label>

        {msg && (
          <div style={{ marginBottom: 12, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}

        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </form>
    </div>
  );
}
