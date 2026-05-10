import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { useAuth } from '@/components/AuthProvider';
import type { ServiceMetadata } from '@/lib/types';

// ServiceCatalogPill — Datadog "service catalog" lite. Shows
// "team / oncall / runbook / repo" metadata as a single
// chip strip on the Service detail page; lets editor+ users
// curate it via a small inline form. Empty state shows an
// "Add metadata" CTA so the catalog grows organically.
//
// Data shape mirrors the backend: every field is optional;
// missing rows return `{ service }` only and the pill
// renders as a single edit CTA.

export function ServiceCatalogPill({ service }: { service: string }) {
  const { user } = useAuth();
  const isEditor = user?.role === 'admin' || user?.role === 'editor';
  const [meta, setMeta] = useState<ServiceMetadata | null | undefined>(undefined);
  const [editing, setEditing] = useState(false);

  useEffect(() => {
    if (!service) return;
    setMeta(undefined);
    api.serviceMetadata(service)
      .then(m => setMeta(m ?? null))
      .catch(() => setMeta(null));
  }, [service]);

  if (meta === undefined) return null; // hide while loading
  if (meta === null) return null;      // failed silently — non-critical

  // Empty state — no curation yet. Render a tiny CTA for
  // editors only; viewers see nothing (the rest of the page
  // works without metadata).
  const hasAny = !!(meta.ownerTeam || meta.sreTeam || meta.description || meta.repository
    || meta.runbookUrl || meta.oncallUrl || meta.slackChannel);

  if (!hasAny && !editing) {
    if (!isEditor) return null;
    return (
      <button className="sec"
        onClick={() => setEditing(true)}
        style={{ fontSize: 11, padding: '3px 10px' }}>
        + Add catalog metadata
      </button>
    );
  }

  if (editing) {
    return (
      <CatalogEditor
        initial={meta}
        onSave={async (next) => {
          await api.putServiceMetadata(service, next);
          setMeta(next);
          setEditing(false);
        }}
        onCancel={() => setEditing(false)} />
    );
  }

  return (
    <span style={{
      display: 'inline-flex', flexWrap: 'wrap', gap: 8, alignItems: 'center',
      fontSize: 13, color: 'var(--text2)',
    }}>
      {meta.ownerTeam && (
        <Pill title="Owner team">👥 {meta.ownerTeam}</Pill>
      )}
      {meta.sreTeam && (
        <Pill title="SRE team">🛡 SRE: {meta.sreTeam}</Pill>
      )}
      {meta.oncallUrl && (
        <Link href={meta.oncallUrl} title="Open oncall page">📟 oncall</Link>
      )}
      {meta.runbookUrl && (
        <Link href={meta.runbookUrl} title="Open runbook">📘 runbook</Link>
      )}
      {meta.slackChannel && (
        <Pill title="Slack channel">#{meta.slackChannel.replace(/^#/, '')}</Pill>
      )}
      {meta.repository && (
        <Link href={meta.repository} title="Open repository">⌥ repo</Link>
      )}
      {isEditor && (
        <button onClick={() => setEditing(true)}
          style={{
            background: 'transparent', border: 0, cursor: 'pointer',
            color: 'var(--text3)', padding: '0 6px', fontSize: 14,
          }}
          title="Edit catalog metadata">✎</button>
      )}
    </span>
  );
}

function Pill({ children, title }: { children: React.ReactNode; title?: string }) {
  return (
    <span title={title} style={{
      padding: '4px 12px', borderRadius: 14,
      background: 'var(--bg3)', border: '1px solid var(--border)',
      color: 'var(--text)', whiteSpace: 'nowrap',
      fontWeight: 500,
    }}>
      {children}
    </span>
  );
}

function Link({ href, title, children }: {
  href: string; title?: string; children: React.ReactNode;
}) {
  return (
    <a href={href} target="_blank" rel="noopener" title={title} style={{
      padding: '4px 12px', borderRadius: 14,
      background: 'rgba(56,139,253,0.10)',
      border: '1px solid rgba(56,139,253,0.35)',
      color: 'var(--accent2)', textDecoration: 'none',
      whiteSpace: 'nowrap',
      fontWeight: 500,
    }}>
      {children} ↗
    </a>
  );
}

function CatalogEditor({ initial, onSave, onCancel }: {
  initial: ServiceMetadata;
  onSave: (m: ServiceMetadata) => Promise<void>;
  onCancel: () => void;
}) {
  const [m, setM] = useState<ServiceMetadata>({ ...initial });
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<ServiceMetadata>) => setM({ ...m, ...patch });

  const submit = async () => {
    setBusy(true);
    try { await onSave(m); }
    finally { setBusy(false); }
  };

  return (
    <div style={{
      display: 'grid', gridTemplateColumns: 'repeat(2, minmax(200px, 1fr))',
      gap: 8, padding: 12,
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, marginTop: 6,
    }}>
      <Field label="Owner team">
        <input value={m.ownerTeam ?? ''} placeholder="payments / search / ml"
          onChange={e => update({ ownerTeam: e.target.value })} />
      </Field>
      <Field label="SRE team">
        <input value={m.sreTeam ?? ''} placeholder="platform / sre-storefront"
          onChange={e => update({ sreTeam: e.target.value })} />
      </Field>
      <Field label="Slack channel">
        <input value={m.slackChannel ?? ''} placeholder="#payments-oncall"
          onChange={e => update({ slackChannel: e.target.value })} />
      </Field>
      <Field label="Runbook URL">
        <input value={m.runbookUrl ?? ''} placeholder="https://wiki.internal/runbook/payments"
          onChange={e => update({ runbookUrl: e.target.value })} />
      </Field>
      <Field label="Oncall URL">
        <input value={m.oncallUrl ?? ''} placeholder="https://pagerduty.com/schedules/abc"
          onChange={e => update({ oncallUrl: e.target.value })} />
      </Field>
      <Field label="Repository">
        <input value={m.repository ?? ''} placeholder="https://github.com/org/payments"
          onChange={e => update({ repository: e.target.value })} />
      </Field>
      <Field label="Description">
        <input value={m.description ?? ''} placeholder="What this service does — one line"
          onChange={e => update({ description: e.target.value })} />
      </Field>
      <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 8, marginTop: 4 }}>
        <button onClick={submit} disabled={busy}
          style={{ fontSize: 12, padding: '4px 12px' }}>
          {busy ? 'Saving…' : 'Save'}
        </button>
        <button className="sec" onClick={onCancel} disabled={busy}
          style={{ fontSize: 12, padding: '4px 12px' }}>
          Cancel
        </button>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <span style={{
        fontSize: 10, color: 'var(--text3)',
        fontWeight: 600, letterSpacing: '0.4px', textTransform: 'uppercase',
      }}>{label}</span>
      {children}
    </label>
  );
}
