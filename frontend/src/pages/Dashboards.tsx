import { useEffect, useRef, useState, FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { Modal, Field, Button, Stack } from '@/components/ui';
import { Sparkline } from '@/components/Sparkline';
import { api } from '@/lib/api';
import { parseDashboardImport } from '@/lib/dashboardIO';
import { toast } from '@/lib/toast';
import { tsLong, fmtNum } from '@/lib/utils';
import type { DashboardSummary } from '@/lib/types';

export default function DashboardsPage() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const [showNew, setShowNew] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);

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

  // Single global "spans/min over last 1h" series. Every card
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

  return (
    <>
      <Topbar title="Dashboards" />
      <div id="content">
        {isAdmin && (
          <div className="controls">
            <button onClick={() => setShowNew(true)}>+ New dashboard</button>
            <button className="sec" onClick={() => fileRef.current?.click()}
              disabled={importing}
              title="Import a dashboard from an exported JSON file">
              {importing ? 'Importing…' : '↑ Import JSON'}
            </button>
            <input ref={fileRef} type="file" accept="application/json,.json"
              style={{ display: 'none' }}
              onChange={e => {
                const f = e.target.files?.[0];
                if (f) onImportFile(f);
              }} />
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
              {items?.length ?? 0} dashboards
            </span>
          </div>
        )}
        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="◫" title="No dashboards yet">
            {isAdmin ? 'Create one to combine metrics, traces and logs into a single view.'
                     : 'Ask an admin to create dashboards.'}
          </Empty>
        )}
        {items && items.length > 0 && (
          <div style={{
            display: 'grid', gap: 12,
            gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))',
          }}>
            {items.map(d => (
              <Link key={d.id} to={`/dashboard?id=${d.id}`} className="dashboard-card" style={{
                display: 'flex', flexDirection: 'column',
                padding: 14, borderRadius: 8,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                color: 'inherit', textDecoration: 'none',
                transition: 'border-color 120ms, background 120ms',
                gap: 8,
              }}>
                <div style={{ fontWeight: 600, fontSize: 14 }}>{d.name}</div>
                {d.description && (
                  <div style={{ fontSize: 12, color: 'var(--text2)',
                                overflow: 'hidden', textOverflow: 'ellipsis',
                                display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical' }}>
                    {d.description}
                  </div>
                )}
                {/* Activity thumbnail: shared spans/min sparkline so
                    a glance at the card list shows whether traffic
                    is steady, ramping, or quiet. Same data across
                    cards because the metric is system-wide; per-
                    dashboard scoping would need backend support. */}
                <div style={{
                  display: 'flex', alignItems: 'center', gap: 8,
                  paddingTop: 6, borderTop: '1px solid var(--border)',
                }}>
                  {activity.length > 1 ? (
                    <Sparkline values={activity}
                               width={140} height={28}
                               title={`Spans/min · last 1h · total ${fmtNum(totalSpans)}`} />
                  ) : (
                    <span style={{ width: 140, height: 28, display: 'inline-block', color: 'var(--text3)', fontSize: 11 }}>
                      —
                    </span>
                  )}
                  <span style={{ flex: 1 }} />
                  <span style={{ fontSize: 10, color: 'var(--text3)', textAlign: 'right' }}>
                    {fmtNum(totalSpans)} spans/h
                  </span>
                </div>
                <div style={{ fontSize: 10, color: 'var(--text3)' }}>
                  Updated {tsLong(d.updatedAt)}
                </div>
              </Link>
            ))}
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
