import { useMemo, useRef, useState, FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { Modal, Field, Button, Stack } from '@/components/ui';
import { Sparkline } from '@/components/Sparkline';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { parseDashboardImport } from '@/lib/dashboardIO';
import { toast } from '@/lib/toast';
import { tsLong, fmtNum } from '@/lib/utils';
import type { DashboardSummary } from '@/lib/types';

// Columns for the shared sortable + resizable DataTable primitive. The
// list is a small fetched array (saved dashboards), so client-side sort
// applies. Default = most-recently-updated first (matches the operator's
// "what did I touch last" mental model).
const DASH_COLS: DataTableColumn<DashboardSummary>[] = [
  { id: 'name',        label: 'Dashboard',  sortValue: r => r.name,        naturalDir: 'asc', width: 240 },
  { id: 'description', label: 'Description', sortValue: r => r.description, naturalDir: 'asc', width: 360 },
  { id: 'updatedAt',   label: 'Updated',    sortValue: r => r.updatedAt,   numeric: true,     width: 150 },
];

export default function DashboardsPage() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const searchRef = useRef<HTMLInputElement>(null);
  const [showNew, setShowNew] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);
  const [q, setQ] = useState('');

  // v0.6.50 — import a dashboard from a previously-exported JSON
  // file. Reuses POST /api/dashboards (createDashboard) so the
  // imported board lands as a fresh dashboard with a new id; no
  // new backend route needed. Validation lives in
  // parseDashboardImport so it's unit-testable + shared with any
  // future drag-drop surface.
  const onImportFile = async (file: File) => {
    setImporting(true);
    try {
      const text = await file.text();
      const payload = parseDashboardImport(text); // throws on bad shape
      const d = await api.createDashboard(payload);
      toast.success(`Imported "${payload.name}"`);
      navigate(`/dashboard?id=${d.id}&edit=1`);
    } catch (err) {
      toast.error('Import failed: ' + (err instanceof Error ? err.message : String(err)));
    } finally {
      setImporting(false);
      if (fileRef.current) fileRef.current.value = ''; // allow re-import of same file
    }
  };

  const dashboardsQ = useQuery<DashboardSummary[]>({
    queryKey: ['dashboards', 'list'],
    queryFn: async () => (await api.listDashboards()) ?? [],
    staleTime: 60_000,
  });
  const items = dashboardsQ.isLoading
    ? undefined
    : dashboardsQ.isError
      ? null
      : dashboardsQ.data ?? [];

  // Substring filter over name + description, case-insensitive.
  const filtered = useMemo(() => {
    if (!items) return items;
    const needle = q.trim().toLowerCase();
    if (!needle) return items;
    return items.filter(d =>
      d.name.toLowerCase().includes(needle) ||
      (d.description ?? '').toLowerCase().includes(needle));
  }, [items, q]);

  // Single global "spans/min over last 1h" series. Every row
  // renders the same sparkline because the metric is system-
  // wide; fetching once and sharing avoids N parallel requests
  // when the dashboard list is long. Refresh every minute.
  const activityQ = useQuery({
    queryKey: ['dashboards', 'activity'],
    queryFn: async () => {
      const now = Date.now() * 1e6;
      const from = now - 60 * 60 * 1e9; // last 1h
      const series = await api.spanMetric({
        agg: 'count',
        from, to: now,
        step: 60, // 1-min buckets, ~60 points
      });
      return series?.[0]?.points ?? [];
    },
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
  const activity = (activityQ.data ?? []).map(p => p.value);
  const totalSpans = activity.reduce((a, b) => a + b, 0);

  const isAdmin = user?.role === 'admin' || user?.role === 'editor';

  // Shared sortable + resizable table with operator-speed keyboard nav
  // (j/k move, Enter/o open, "/" focuses the filter). Called
  // unconditionally (hooks rule) with [] while loading.
  const dt = useDataTable<DashboardSummary>({
    storageKey: 'dashboards',
    columns: DASH_COLS,
    rows: filtered ?? [],
    initialSort: { id: 'updatedAt', dir: 'desc' },
    onOpen: d => navigate(`/dashboard?id=${d.id}`),
    searchRef,
  });

  return (
    <>
      <Topbar title="Dashboards" />
      <div id="content">
        <div className="controls">
          <input ref={searchRef} value={q} onChange={e => setQ(e.target.value)}
            placeholder="Filter dashboards…" aria-label="Filter dashboards"
            style={{ width: 220 }} />
          {q && (
            <Button variant="secondary" size="sm" onClick={() => setQ('')}>Clear</Button>
          )}
          {isAdmin && (
            <>
              <Button onClick={() => setShowNew(true)}>+ New dashboard</Button>
              <Button variant="secondary" onClick={() => fileRef.current?.click()}
                disabled={importing}
                title="Import a dashboard from an exported JSON file">
                {importing ? 'Importing…' : '↑ Import JSON'}
              </Button>
              <input ref={fileRef} type="file" accept="application/json,.json"
                style={{ display: 'none' }}
                onChange={e => {
                  const f = e.target.files?.[0];
                  if (f) onImportFile(f);
                }} />
            </>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {filtered?.length ?? 0} dashboard{(filtered?.length ?? 0) === 1 ? '' : 's'}
          </span>
        </div>

        {items === undefined && <Spinner />}
        {items === null && <Empty icon="⚠" title="Failed to load dashboards" />}
        {items && items.length === 0 && (
          <Empty icon="◫" title="No dashboards yet"
            action={isAdmin ? <Button onClick={() => setShowNew(true)}>+ New dashboard</Button> : undefined}>
            {isAdmin ? 'Create one to combine metrics, traces and logs into a single view.'
                     : 'Ask an admin to create dashboards.'}
          </Empty>
        )}
        {items && items.length > 0 && filtered && filtered.length === 0 && (
          <Empty icon="◇" title="No matching dashboards">
            No saved dashboard matches “{q}”.
          </Empty>
        )}
        {filtered && filtered.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map((d, i) => (
                  <tr key={d.id}
                      {...dt.rowProps(i)}
                      onMouseEnter={() => dt.nav.setSelected(i)}
                      onClick={() => navigate(`/dashboard?id=${d.id}`)}
                      style={{ cursor: 'pointer' }}>
                    <td>
                      <span style={{ fontWeight: 600, color: 'var(--text)' }}>{d.name}</span>
                    </td>
                    <td style={{ color: 'var(--text2)' }} title={d.description || undefined}>
                      {d.description || <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                    <td className="num mono" style={{ color: 'var(--text2)' }}
                        title={`Updated ${tsLong(d.updatedAt)}`}>
                      {tsLong(d.updatedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Shared system-wide activity strip — same data as the
            former per-card thumbnails, lifted out so the table rows
            stay scannable. Tells the operator at a glance whether
            ingest traffic is steady, ramping, or quiet. */}
        {filtered && filtered.length > 0 && (
          <div className="row gap-3" style={{
            marginTop: 12, padding: '8px 12px',
            border: '1px solid var(--border)', borderRadius: 8,
            background: 'var(--bg1)',
          }}>
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>Ingest · last 1h</span>
            {activity.length > 1 ? (
              <Sparkline values={activity} width={180} height={28}
                title={`Spans/min · last 1h · total ${fmtNum(totalSpans)}`} />
            ) : (
              <span style={{ color: 'var(--text3)', fontSize: 11 }}>—</span>
            )}
            <span style={{ flex: 1 }} />
            <span style={{ fontSize: 11, color: 'var(--text2)' }} className="mono">
              {fmtNum(totalSpans)} spans/h
            </span>
          </div>
        )}

        {showNew && isAdmin && (
          <NewDashboardModal
            onClose={() => setShowNew(false)}
            onCreated={(id) => { setShowNew(false); navigate(`/dashboard?id=${id}&edit=1`); }}
          />
        )}
      </div>
    </>
  );
}

function NewDashboardModal({ onClose, onCreated }: {
  onClose: () => void; onCreated: (id: string) => void;
}) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const d = await api.createDashboard({ name, description, panels: [] });
      onCreated(d.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={true}
      onClose={onClose}
      title="New dashboard"
      size="sm"
      initialFocus="input[name=name]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-dashboard-form" loading={busy}>Create</Button>
        </>
      }>
      <form id="new-dashboard-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="Name"
            name="name"
            required
            value={name}
            onChange={e => setName(e.target.value)} />
          <Field
            label="Description (optional)"
            value={description}
            onChange={e => setDescription(e.target.value)} />
          {error && (
            <div style={{ color: 'var(--err)', fontSize: 12 }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
