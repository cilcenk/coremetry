import { useEffect, useState, FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { IconShield } from '@/components/icons';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { Button } from '@/components/ui/Button';
import {
  useMonitors,
  useStatusPageConfig, useUpdateStatusPageConfig,
  useStatusPageComponents, useCreateStatusComponent,
  useUpdateStatusComponent, useDeleteStatusComponent,
  useStatusPageSubscribers, useDeleteStatusSubscriber,
} from '@/lib/queries';
import { tsLong } from '@/lib/utils';
import type { StatusPageConfig, StatusComponent, StatusSubscriber, MonitorRow } from '@/lib/types';

// /admin/status-page — operator config for the public /public-status
// page: page header, list of components (each tied to a monitor or
// service), and the email subscriber list.

export default function StatusPageAdmin() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [tab, setTab] = useState<'config' | 'components' | 'subs'>('components');

  if (!isAdmin) {
    return (
      <>
        <Topbar title="Status Page" />
        <div id="content"><Empty icon={<IconShield size={28} />} title="Admin only" /></div>
      </>
    );
  }
  return (
    <>
      <Topbar title="Status Page (admin)" />
      <div id="content">
        <div style={{ display: 'flex', alignItems: 'center', marginBottom: 14 }}>
          <div className="tab-strip" style={{ marginBottom: 0, flex: 1 }}>
            <button onClick={() => setTab('components')}
                    className={tab === 'components' ? 'active' : ''}>Components</button>
            <button onClick={() => setTab('config')}
                    className={tab === 'config' ? 'active' : ''}>Page header</button>
            <button onClick={() => setTab('subs')}
                    className={tab === 'subs' ? 'active' : ''}>Subscribers</button>
          </div>
          <Link to="/public-status" target="_blank" style={{
            fontSize: 12, padding: '5px 12px',
            background: 'var(--bg3)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--accent2)', textDecoration: 'none',
          }}>
            View public page ↗
          </Link>
        </div>
        {tab === 'components' && <ComponentsTab />}
        {tab === 'config' && <ConfigTab />}
        {tab === 'subs' && <SubsTab />}
      </div>
    </>
  );
}

function ConfigTab() {
  const cfgQ = useStatusPageConfig();
  const putConfig = useUpdateStatusPageConfig();
  // Local editable draft, hydrated from the loaded config. While dirty
  // we don't re-hydrate so a background refetch can't clobber edits
  // (same pattern as the Runbook editor).
  // null = fetch failed (don't mask a load error with a default object —
  // saving over the top would clobber the real persisted config).
  const [c, setC] = useState<StatusPageConfig | null | undefined>(undefined);
  const [dirty, setDirty] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const busy = putConfig.isPending;
  useEffect(() => {
    if (cfgQ.isError) { setC(null); return; }
    if (cfgQ.data !== undefined && !dirty) setC(cfgQ.data ?? { title: 'Service Status' });
  }, [cfgQ.data, cfgQ.isError, dirty]);
  if (c === undefined) return <Spinner />;
  if (c === null) return (
    <Empty icon="⚠" title="Couldn't load page header">
      The status-page config failed to load. Reload the tab — editing now would
      risk overwriting the stored header.
    </Empty>
  );
  const edit = (patch: Partial<StatusPageConfig>) => { setC({ ...c, ...patch }); setDirty(true); };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    try { await putConfig.mutateAsync(c); setDirty(false); setMsg('Saved'); }
    catch (err) { setMsg(err instanceof Error ? err.message : 'Save failed'); }
  };
  return (
    <form onSubmit={submit} style={{ maxWidth: 480 }}>
      <Field label="Page title">
        <input required value={c.title} onChange={e => edit({ title: e.target.value })} style={{ width: '100%' }} />
      </Field>
      <Field label="Description (shown under banner)">
        <textarea value={c.description ?? ''} onChange={e => edit({ description: e.target.value })}
          rows={3} style={{ width: '100%', resize: 'vertical' }} />
      </Field>
      <Field label="Support URL (optional)">
        <input type="url" value={c.supportUrl ?? ''} onChange={e => edit({ supportUrl: e.target.value })}
          placeholder="https://support.example.com" style={{ width: '100%' }} />
      </Field>
      <Button type="submit" variant="primary" disabled={busy} style={{ marginTop: 12 }}>{busy ? 'Saving…' : 'Save'}</Button>
      {msg && <span style={{ marginLeft: 10, fontSize: 12, color: 'var(--text2)' }}>{msg}</span>}
    </form>
  );
}

function ComponentsTab() {
  // undefined = loading, null = fetch failed, [] = loaded-but-empty.
  const itemsQ = useStatusPageComponents();
  const items: StatusComponent[] | null | undefined =
    itemsQ.isPending ? undefined : itemsQ.isError ? null : itemsQ.data ?? [];
  // Monitor names label the component rows + fill the modal dropdown.
  const monitors: MonitorRow[] = useMonitors().data ?? [];
  const deleteComponent = useDeleteStatusComponent();
  const [editing, setEditing] = useState<StatusComponent | null>(null);
  const [showNew, setShowNew] = useState(false);
  if (items === undefined) return <Spinner />;
  if (items === null) return (
    <Empty icon="⚠" title="Couldn't load components">
      The status-page components failed to load. Reload the tab to try again.
    </Empty>
  );
  return (
    <>
      <div className="controls" style={{ marginBottom: 12 }}>
        <Button variant="primary" onClick={() => setShowNew(true)}>+ Add component</Button>
        <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
          {items.length} component{items.length === 1 ? '' : 's'} on the public page
        </span>
      </div>
      {items.length === 0 && (
        <Empty icon="◯" title="No components yet">
          Add components to make them appear on the public status page. Each
          component derives its status from a monitor (HTTP probe / heartbeat)
          or from open Problems on a service.
        </Empty>
      )}
      {items.length > 0 && (
        <div className="status-grid">
          {items.map(c => (
            <div key={c.id} className="status-row">
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, flexWrap: 'wrap' }}>
                <span style={{ fontWeight: 600 }}>{c.name}</span>
                {c.monitorId && (
                  <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                    monitor: {monitors.find(m => m.id === c.monitorId)?.name ?? c.monitorId.slice(0, 8) + '…'}
                  </span>
                )}
                {c.serviceName && (
                  <span style={{ fontSize: 11, color: 'var(--text3)' }}>service: {c.serviceName}</span>
                )}
                {c.description && (
                  <span style={{ fontSize: 11, color: 'var(--text3)' }}>· {c.description}</span>
                )}
              </div>
              <div style={{ display: 'flex', gap: 6 }}>
                <Button variant="secondary" size="sm" onClick={() => setEditing(c)}>Edit</Button>
                <Button variant="danger" size="sm" onClick={async () => {
                  if (!confirm(`Remove "${c.name}"?`)) return;
                  // Mutation auto-invalidates the components list.
                  await deleteComponent.mutateAsync(c.id);
                }}>Remove</Button>
              </div>
            </div>
          ))}
        </div>
      )}
      {(showNew || editing) && (
        <ComponentModal initial={editing} monitors={monitors}
          onClose={() => { setShowNew(false); setEditing(null); }}
          onSaved={() => { setShowNew(false); setEditing(null); }} />
      )}
    </>
  );
}

function ComponentModal({ initial, monitors, onClose, onSaved }: {
  initial: StatusComponent | null;
  monitors: MonitorRow[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [c, setC] = useState<Partial<StatusComponent>>(initial ?? { displayOrder: 0 });
  const [source, setSource] = useState<'monitor' | 'service'>(initial?.serviceName ? 'service' : 'monitor');
  const [error, setError] = useState<string | null>(null);
  // Both mutations auto-invalidate the components list on success.
  const createComponent = useCreateStatusComponent();
  const updateComponent = useUpdateStatusComponent();
  const busy = createComponent.isPending || updateComponent.isPending;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      // Clear the unused field so we don't double-source the status.
      const payload: Partial<StatusComponent> = { ...c };
      if (source === 'monitor') payload.serviceName = '';
      else                       payload.monitorId = '';
      if (initial) await updateComponent.mutateAsync({ id: initial.id, patch: payload });
      else         await createComponent.mutateAsync(payload);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Save failed');
    }
  };
  return (
    <div onClick={onClose} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', display: 'grid', placeItems: 'center', zIndex: 100 }}>
      <div onClick={e => e.stopPropagation()} style={{ width: 480, padding: 24, borderRadius: 8, background: 'var(--bg2)', border: '1px solid var(--border)' }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>
          {initial ? `Edit component — ${initial.name}` : 'New component'}
        </div>
        <form onSubmit={submit}>
          <Field label="Name (visible to the public)">
            <input required autoFocus value={c.name ?? ''} onChange={e => setC({ ...c, name: e.target.value })}
              placeholder="e.g. Web App, API, Checkout" style={{ width: '100%' }} />
          </Field>
          <Field label="Description (optional)">
            <input value={c.description ?? ''} onChange={e => setC({ ...c, description: e.target.value })}
              placeholder="One-line context for end users" style={{ width: '100%' }} />
          </Field>
          <Field label="Status source">
            <div className="segmented">
              <button type="button" className={source === 'monitor' ? 'active' : ''} onClick={() => setSource('monitor')}>Monitor probe</button>
              <button type="button" className={source === 'service' ? 'active' : ''} onClick={() => setSource('service')}>Open Problems on service</button>
            </div>
          </Field>
          {source === 'monitor' && (
            <Field label="Monitor">
              <select value={c.monitorId ?? ''} onChange={e => setC({ ...c, monitorId: e.target.value })} required style={{ width: '100%' }}>
                <option value="">— pick a monitor —</option>
                {monitors.map(m => <option key={m.id} value={m.id}>{m.name} ({m.type})</option>)}
              </select>
            </Field>
          )}
          {source === 'service' && (
            <Field label="Service">
              <ServicePicker value={c.serviceName ?? ''} onChange={v => setC({ ...c, serviceName: v })} placeholder="Service…" width="100%" />
            </Field>
          )}
          <Field label="Display order (lower = shown first)">
            <input type="number" value={c.displayOrder ?? 0} onChange={e => setC({ ...c, displayOrder: Number(e.target.value) })} style={{ width: 100 }} />
          </Field>
          {error && <div className="trp-error" style={{ marginTop: 10 }}>{error}</div>}
          <div style={{ display: 'flex', gap: 8, marginTop: 16, justifyContent: 'flex-end' }}>
            <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={busy}>{busy ? 'Saving…' : initial ? 'Save' : 'Create'}</Button>
          </div>
        </form>
      </div>
    </div>
  );
}

function SubsTab() {
  // undefined = loading, null = fetch failed, [] = loaded-but-empty.
  const subsQ = useStatusPageSubscribers();
  const subs: StatusSubscriber[] | null | undefined =
    subsQ.isPending ? undefined : subsQ.isError ? null : subsQ.data ?? [];
  const deleteSubscriber = useDeleteStatusSubscriber();
  if (subs === undefined) return <Spinner />;
  if (subs === null) return (
    <Empty icon="⚠" title="Couldn't load subscribers">
      The subscriber list failed to load. Reload the tab to try again.
    </Empty>
  );
  if (subs.length === 0) return <Empty icon="✉" title="No subscribers yet">Subscribers sign up via the public status page.</Empty>;
  return (
    <div className="status-grid">
      {subs.map(s => (
        <div key={s.id} className="status-row">
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{s.email}</span>
            {s.verified
              ? <span className="badge b-ok" title="Confirmed the subscription via email link">verified</span>
              : <span className="badge b-warn"
                  title={s.confirmSentAt
                    ? `Confirmation email sent ${tsLong(s.confirmSentAt)} — subscriber hasn't clicked yet`
                    : 'Confirmation email not yet delivered (SMTP unconfigured at signup)'}>
                  pending
                </span>}
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>· joined {tsLong(s.createdAt)}</span>
          </div>
          <Button variant="danger" size="sm" onClick={async () => {
            if (!confirm(`Remove subscriber ${s.email}?`)) return;
            // Mutation auto-invalidates the subscriber list.
            await deleteSubscriber.mutateAsync(s.email);
          }}>Remove</Button>
        </div>
      ))}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginTop: 10 }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 3 }}>{label}</div>
      {children}
    </label>
  );
}
