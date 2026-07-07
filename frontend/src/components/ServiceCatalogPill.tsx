import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { useAuth } from '@/components/AuthProvider';
import { Button } from '@/components/ui/Button';
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
    || meta.runbookUrl || meta.oncallUrl || meta.chatChannel
    || (meta.customLinks && meta.customLinks.length > 0));

  if (!hasAny && !editing) {
    if (!isEditor) return null;
    return (
      <Button variant="secondary" size="sm"
        onClick={() => setEditing(true)}>
        + Add catalog metadata
      </Button>
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
        <TeamPill label="owner" team={meta.ownerTeam} title="Owner team — click to see members" />
      )}
      {meta.sreTeam && (
        <TeamPill label="sre" team={meta.sreTeam} title="SRE team — click to see members" />
      )}
      {meta.oncallUrl && (
        <Link href={meta.oncallUrl} title="Open oncall page">oncall</Link>
      )}
      {meta.runbookUrl && (
        <Link href={meta.runbookUrl} title="Open runbook">runbook</Link>
      )}
      {meta.chatChannel && (
        <Pill title="Zoom Chat channel">
          <Label>chat</Label> {meta.chatChannel.replace(/^#/, '')}
        </Pill>
      )}
      {meta.repository && (
        <Link href={meta.repository} title="Open repository">repo</Link>
      )}
      {/* Operator-curated extras — Grafana, Kibana, Sensei,
          internal apps. Rendered after the built-in surfaces
          so the consistent ones (oncall / runbook / repo)
          stay anchored on the left. */}
      {(meta.customLinks ?? []).map((l, i) => (
        <Link key={i} href={l.url} title={l.url}>{l.label}</Link>
      ))}
      {isEditor && (
        <Button variant="ghost" size="sm" onClick={() => setEditing(true)}
          title="Edit catalog metadata">✎</Button>
      )}
    </span>
  );
}

// Label — typographic prefix on a pill (e.g. "owner Bob",
// "sre Alice"). Uppercase mini-cap reads as a discrete tag
// rather than a sentence subject; keeps the chips compact
// without leaning on emoji glyphs.
function Label({ children }: { children: React.ReactNode }) {
  return (
    <span style={{
      fontSize: 10, color: 'var(--text3)',
      fontWeight: 700, letterSpacing: '0.5px',
      textTransform: 'uppercase', marginRight: 5,
    }}>
      {children}
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
      <Field label="Zoom Chat channel">
        <input value={m.chatChannel ?? ''} placeholder="payments-oncall"
          onChange={e => update({ chatChannel: e.target.value })} />
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
      {/* Custom links editor — dynamic list with add/remove.
          Span the full grid width so the label/url inputs
          have room. */}
      <div style={{ gridColumn: '1 / -1' }}>
        <CustomLinksEditor
          links={m.customLinks ?? []}
          onChange={ls => update({ customLinks: ls })} />
      </div>
      <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 8, marginTop: 4 }}>
        <Button size="sm" onClick={submit} disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
        <Button variant="secondary" size="sm" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
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

// CustomLinksEditor — dynamic add/remove rows for the
// operator-curated link list. Each row has a label + url
// input and an "×" to drop it; one trailing "+ Add link"
// appends a fresh blank row. Empty rows are dropped
// server-side (see UpsertServiceMetadata) so the operator
// can leave one blank without polluting the catalog.
function CustomLinksEditor({ links, onChange }: {
  links: import('@/lib/types').CustomLink[];
  onChange: (next: import('@/lib/types').CustomLink[]) => void;
}) {
  const set = (i: number, patch: Partial<import('@/lib/types').CustomLink>) => {
    const next = [...links];
    next[i] = { ...next[i], ...patch };
    onChange(next);
  };
  const remove = (i: number) => onChange(links.filter((_, j) => j !== i));
  const add = () => onChange([...links, { label: '', url: '' }]);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <span style={{
        fontSize: 10, color: 'var(--text3)',
        fontWeight: 600, letterSpacing: '0.4px', textTransform: 'uppercase',
      }}>
        Custom links
      </span>
      {links.map((l, i) => (
        <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input value={l.label}
            placeholder="Grafana"
            onChange={e => set(i, { label: e.target.value })}
            style={{ width: 130 }} />
          <input value={l.url}
            placeholder="https://grafana.internal/d/abc"
            onChange={e => set(i, { url: e.target.value })}
            style={{ flex: 1 }} />
          <Button variant="ghost" size="sm" onClick={() => remove(i)}
            title="Remove link">×</Button>
        </div>
      ))}
      <Button variant="secondary" size="sm" onClick={add}
        style={{ alignSelf: 'flex-start', color: 'var(--accent2)' }}>
        + Add link
      </Button>
    </div>
  );
}

// TeamPill — a team chip that opens a popover listing the
// Coremetry users whose `team` field matches. Bridges the
// service catalog's free-text team label to the admin-curated
// user.team values so a service owner can be reached in one
// click. Lazily fetches the member list only when the popover
// opens.
function TeamPill({ label, team, title }: {
  label: string;
  team: string;
  title: string;
}) {
  const [open, setOpen] = useState(false);
  const [members, setMembers] = useState<
    { id: string; email: string; role: string }[] | null | undefined
  >(undefined);

  useEffect(() => {
    if (!open) return;
    if (members !== undefined) return;
    api.usersByTeam(team)
      .then(r => setMembers(r ?? []))
      .catch(() => setMembers(null));
  }, [open, team, members]);

  // Close on outside click. Cheap document listener — only
  // mounted while the popover is open.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as HTMLElement;
      if (!t.closest?.(`[data-team-pill="${team}"]`)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open, team]);

  return (
    <span data-team-pill={team} style={{ position: 'relative', display: 'inline-block' }}>
      <button type="button" onClick={() => setOpen(o => !o)}
        title={title}
        style={{
          all: 'unset', cursor: 'pointer',
          padding: '2px 8px', borderRadius: 999,
          background: 'var(--bg3)', border: '1px solid var(--border)',
          fontSize: 12, lineHeight: 1.4,
        }}>
        <span style={{
          fontSize: 10, textTransform: 'uppercase', color: 'var(--text3)',
          letterSpacing: 0.5, marginRight: 4,
        }}>{label}</span>
        {team}
        <span style={{ color: 'var(--text3)', marginLeft: 4 }}>▾</span>
      </button>
      {open && (
        <div style={{
          position: 'absolute', top: '110%', left: 0, zIndex: 50,
          minWidth: 220, maxWidth: 320,
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 8, boxShadow: '0 6px 18px rgba(0,0,0,0.35)',
        }}>
          <div style={{
            fontSize: 10, color: 'var(--text3)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 6,
          }}>{label} · {team}</div>
          {members === undefined && (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>Loading…</div>
          )}
          {members === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>Lookup failed.</div>
          )}
          {members && members.length === 0 && (
            <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic' }}>
              No Coremetry users tagged with team "{team}". Admin → Users to
              assign team labels.
            </div>
          )}
          {members && members.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {members.map(m => (
                <a key={m.id} href={`mailto:${m.email}`}
                  style={{
                    fontSize: 12, color: 'var(--text)', textDecoration: 'none',
                    display: 'flex', justifyContent: 'space-between',
                    padding: '3px 6px', borderRadius: 3,
                    background: 'var(--bg2)',
                  }}
                  title={`Email ${m.email}`}>
                  <span style={{ fontFamily: 'ui-monospace, monospace' }}>{m.email}</span>
                  <span style={{
                    fontSize: 9, color: 'var(--text3)',
                    textTransform: 'uppercase', letterSpacing: 0.4,
                  }}>{m.role}</span>
                </a>
              ))}
            </div>
          )}
        </div>
      )}
    </span>
  );
}
