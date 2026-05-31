import { useEffect, useMemo, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { useAuth } from '@/components/AuthProvider';
import { IconShield } from '@/components/icons';
import { api } from '@/lib/api';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { ServiceMetadata } from '@/lib/types';

// /admin/catalog — admin-only bulk editor for the service
// catalog. Pulls every catalog row + every service that has
// recent traffic, joins them locally so the operator can:
//   • see at a glance which services are "yet to curate"
//   • spot-edit owner team / SRE team / chat / oncall / repo
//     across the whole catalog without bouncing into each
//     service detail page
//   • search by service or owner team
//
// We don't paginate — at ~1000 services the table scrolls
// fine inside a max-height container, and at 5000+ services
// the search input narrows it to a few rows anyway.

type Row = {
  service: string;
  meta: ServiceMetadata;
  hasTraffic: boolean;
};

// Shared sortable + resizable columns (v0.7.53 primitive). Body
// cell ORDER must mirror this list. The four link/action columns
// (runbook / oncall / repo / actions) are intentionally NOT
// sortable — they carry an "open ↗" link or buttons, not a value
// the operator would order by; they still resize. Default sort is
// Service asc (the prior client default from the localeCompare in
// reload()). headerHidden carries the previous empty actions
// header without an extra label.
const CATALOG_COLS: DataTableColumn<Row>[] = [
  { id: 'service',  label: 'Service',           sortValue: r => r.service,              naturalDir: 'asc', width: 220 },
  { id: 'owner',    label: 'Owner team',        sortValue: r => r.meta.ownerTeam ?? '', naturalDir: 'asc', width: 150 },
  { id: 'sre',      label: 'SRE team',          sortValue: r => r.meta.sreTeam ?? '',   naturalDir: 'asc', width: 150 },
  { id: 'chat',     label: 'Zoom Chat channel', sortValue: r => r.meta.chatChannel ?? '', naturalDir: 'asc', width: 170 },
  { id: 'runbook',  label: 'Runbook',           width: 90 },
  { id: 'oncall',   label: 'Oncall',            width: 90 },
  { id: 'repo',     label: 'Repo',              width: 90 },
  { id: 'actions',  label: '',                  width: 130 },
];

export default function AdminCatalogPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [rows, setRows] = useState<Row[] | null | undefined>(undefined);
  const [search, setSearch] = useState('');
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<ServiceMetadata | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  // Composite load — services-list (for the service set)
  // + services-metadata (for the catalog rows). Two parallel
  // shots; the page renders as soon as both land.
  const reload = async () => {
    setRows(undefined);
    try {
      const [svcResp, mdMap] = await Promise.all([
        api.serviceNames(),
        api.servicesMetadata(),
      ]);
      const trafficSet = new Set<string>(svcResp?.names ?? []);
      // Include catalog rows whose service no longer has
      // recent traffic (operator-curated history); flag them
      // visually so it's clear they're "stale".
      const all = new Set<string>(trafficSet);
      for (const k of Object.keys(mdMap ?? {})) all.add(k);
      const out: Row[] = [];
      for (const name of all) {
        out.push({
          service: name,
          meta: (mdMap ?? {})[name] ?? { service: name },
          hasTraffic: trafficSet.has(name),
        });
      }
      out.sort((a, b) => a.service.localeCompare(b.service));
      setRows(out);
    } catch {
      setRows(null);
    }
  };

  useEffect(() => { if (isAdmin) reload(); }, [isAdmin]);

  const filtered = useMemo(() => {
    if (!rows) return rows;
    const q = search.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter(r =>
      r.service.toLowerCase().includes(q) ||
      (r.meta.ownerTeam ?? '').toLowerCase().includes(q) ||
      (r.meta.sreTeam ?? '').toLowerCase().includes(q));
  }, [rows, search]);

  // Shared table primitive. Hook is UNCONDITIONAL and lives ABOVE
  // the admin early-return below (rules-of-hooks). Keep the
  // client-side FILTER (search) — feed the filtered array as rows.
  const dt = useDataTable<Row>({
    storageKey: 'admin-catalog',
    columns: CATALOG_COLS,
    rows: filtered ?? [],
    initialSort: { id: 'service', dir: 'asc' },
  });

  const startEdit = (r: Row) => {
    setEditing(r.service);
    setDraft({ ...r.meta, service: r.service });
  };

  const save = async () => {
    if (!draft || !editing) return;
    setBusy(editing);
    try {
      await api.putServiceMetadata(editing, draft);
      setRows(rs => rs ? rs.map(r =>
        r.service === editing ? { ...r, meta: { ...draft, service: editing } } : r
      ) : rs);
      setEditing(null);
      setDraft(null);
    } catch {
      // Re-fetch on save failure so the row reflects the
      // server's actual state instead of the stale optimistic
      // copy.
      reload();
    } finally {
      setBusy(null);
    }
  };

  if (!user) return null;
  if (!isAdmin) {
    return (
      <>
        <Topbar title="Service catalog" />
        <div id="content">
          <Empty icon={<IconShield size={28} />} title="Admin access required">
            The service catalog editor is admin-only — same gate as the rest of /admin/*.
          </Empty>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Service catalog" />
      <div id="content">
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 14 }}>
          <input value={search} onChange={e => setSearch(e.target.value)}
            placeholder="Filter by service / owner / SRE team…"
            style={{ minWidth: 280, fontSize: 13, padding: '4px 8px' }} />
          <span style={{ flex: 1 }} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            {rows ? `${rows.length} services` : 'Loading…'}
            {' · '}
            {rows ? `${rows.filter(r => r.meta.ownerTeam || r.meta.sreTeam).length} curated` : ''}
          </span>
          <button className="sec" onClick={reload}
            style={{ fontSize: 12, padding: '4px 12px' }}>
            Refresh
          </button>
        </div>

        {rows === undefined && <TableSkeleton rows={12} cols={7} />}
        {rows === null && (
          <Empty icon="!" title="Failed to load catalog" />
        )}
        {filtered && filtered.length === 0 && (
          <Empty icon="◇" title="No services match your filter" />
        )}
        {filtered && filtered.length > 0 && (
          <div className="table-wrap catalog-scroll"
               style={{ maxHeight: 'calc(100vh - 220px)', overflowY: 'auto' }}>
            {/* The catalog scrolls inside a tall max-height container,
                so its header must stick — the shared DataTableHead
                renders a plain <thead>, so we re-apply the prior
                sticky behaviour scoped to this wrapper only. */}
            <style>{`.catalog-scroll thead th { position: sticky; top: 0; background: var(--bg1); z-index: 1; }`}</style>
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(r => (
                  editing === r.service && draft
                    ? <EditRow key={r.service}
                        draft={draft}
                        busy={busy === r.service}
                        onChange={setDraft}
                        onSave={save}
                        onCancel={() => { setEditing(null); setDraft(null); }} />
                    : <DisplayRow key={r.service}
                        row={r}
                        onEdit={() => startEdit(r)} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function DisplayRow({ row, onEdit }: { row: Row; onEdit: () => void }) {
  const m = row.meta;
  const hasAny = m.ownerTeam || m.sreTeam || m.chatChannel || m.runbookUrl || m.oncallUrl || m.repository;
  return (
    <tr style={{
      opacity: row.hasTraffic ? 1 : 0.55,
      // content-visibility: auto lets the browser skip rendering
      // off-screen rows (v0.5.199). At 1000+ services the catalog
      // table locked the page on initial paint without this.
      // intrinsicSize is a single-row placeholder so the scrollbar
      // stays accurate before measurement.
      contentVisibility: 'auto',
      containIntrinsicSize: 'auto 36px',
    }}>
      <td className="mono">
        <span style={{ fontWeight: 600 }}>{row.service}</span>
        {!row.hasTraffic && (
          <span style={{ fontSize: 10, color: 'var(--text3)', marginLeft: 6 }}>
            no recent traffic
          </span>
        )}
      </td>
      <td>{m.ownerTeam || <em style={{ color: 'var(--text3)' }}>—</em>}</td>
      <td>{m.sreTeam || <em style={{ color: 'var(--text3)' }}>—</em>}</td>
      <td>{m.chatChannel || <em style={{ color: 'var(--text3)' }}>—</em>}</td>
      <td><LinkCell url={m.runbookUrl} /></td>
      <td><LinkCell url={m.oncallUrl} /></td>
      <td><LinkCell url={m.repository} /></td>
      <td>
        <button className="sec" onClick={onEdit}
          style={{ fontSize: 11, padding: '2px 8px' }}>
          {hasAny ? 'Edit' : 'Add'}
        </button>
      </td>
    </tr>
  );
}

function EditRow({ draft, busy, onChange, onSave, onCancel }: {
  draft: ServiceMetadata;
  busy: boolean;
  onChange: (m: ServiceMetadata) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const u = (patch: Partial<ServiceMetadata>) => onChange({ ...draft, ...patch });
  // AI suggest state — only used when the operator clicks the
  // ✨ button. Hint = the reasoning + confidence the model
  // returned so the operator can sanity-check the pre-fill.
  const [aiBusy, setAiBusy]   = useState(false);
  const [aiHint, setAiHint]   = useState<string | null>(null);
  const [aiError, setAiError] = useState<string | null>(null);

  const aiSuggest = async () => {
    setAiBusy(true); setAiError(null); setAiHint(null);
    try {
      const r = await api.copilotSuggestServiceTags(draft.service);
      if (!r.suggestions) {
        setAiError(r.note ?? 'No suggestions returned.');
        return;
      }
      // Only fill fields the operator hasn't already typed —
      // never overwrite their judgement. Description goes into
      // the catalog row; criticality + reasoning surface as a
      // hint line below but aren't persisted (no column yet).
      const s = r.suggestions;
      u({
        ownerTeam:   draft.ownerTeam   || s.ownerTeam || '',
        sreTeam:     draft.sreTeam     || s.sreTeam   || '',
        description: draft.description || s.description || '',
      });
      const parts: string[] = [];
      if (s.confidence)  parts.push(`${s.confidence} confidence`);
      if (s.criticality) parts.push(`tier: ${s.criticality}`);
      if (s.reasoning)   parts.push(s.reasoning);
      setAiHint(parts.join(' · '));
    } catch (e: unknown) {
      setAiError(e instanceof Error ? e.message : 'Suggest failed');
    } finally {
      setAiBusy(false);
    }
  };

  return (
    <>
      <tr style={{ background: 'var(--bg2)' }}>
        <td className="mono"><b>{draft.service}</b></td>
        <td><Inp v={draft.ownerTeam} on={v => u({ ownerTeam: v })} ph="payments" /></td>
        <td><Inp v={draft.sreTeam}   on={v => u({ sreTeam: v })}   ph="platform" /></td>
        <td><Inp v={draft.chatChannel} on={v => u({ chatChannel: v })} ph="payments-oncall" /></td>
        <td><Inp v={draft.runbookUrl} on={v => u({ runbookUrl: v })} ph="https://wiki/runbook" /></td>
        <td><Inp v={draft.oncallUrl}  on={v => u({ oncallUrl: v })}  ph="https://pagerduty/..." /></td>
        <td><Inp v={draft.repository} on={v => u({ repository: v })} ph="https://github/..." /></td>
        <td>
          <span style={{ display: 'inline-flex', gap: 4 }}>
            <button className="sec" onClick={aiSuggest} disabled={busy || aiBusy}
              title="Auto-fill owner / SRE team / description from this service's recent telemetry"
              style={{ fontSize: 11, padding: '2px 8px', color: 'var(--accent2)' }}>
              {aiBusy ? '…' : '✨ AI'}
            </button>
            <button onClick={onSave} disabled={busy}
              style={{ fontSize: 11, padding: '2px 8px' }}>
              {busy ? '…' : 'Save'}
            </button>
            <button className="sec" onClick={onCancel} disabled={busy}
              style={{ fontSize: 11, padding: '2px 8px' }}>
              Cancel
            </button>
          </span>
        </td>
      </tr>
      {(aiHint || aiError) && (
        <tr style={{ background: 'var(--bg2)' }}>
          <td colSpan={8} style={{
            fontSize: 11, paddingTop: 0, paddingBottom: 8,
            color: aiError ? 'var(--err)' : 'var(--text3)',
            fontStyle: 'italic',
          }}>
            {aiError ?? `✨ ${aiHint}`}
          </td>
        </tr>
      )}
    </>
  );
}

function Inp({ v, on, ph }: { v?: string; on: (v: string) => void; ph?: string }) {
  return (
    <input value={v ?? ''} placeholder={ph} onChange={e => on(e.target.value)}
      style={{ width: '100%', minWidth: 110, fontSize: 12, padding: '2px 6px' }} />
  );
}

function LinkCell({ url }: { url?: string }) {
  if (!url) return <em style={{ color: 'var(--text3)' }}>—</em>;
  return (
    <a href={url} target="_blank" rel="noopener"
      style={{ fontSize: 11, color: 'var(--accent2)' }}
      title={url}>
      open ↗
    </a>
  );
}
