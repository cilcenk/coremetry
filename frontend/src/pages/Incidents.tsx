import { useState, FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { ServicePicker } from '@/components/ServicePicker';
import { ClusterChips as ClusterChipsRef } from '@/components/ClusterChips';
import { Modal, Button, Field, SelectField, TextareaField, Row } from '@/components/ui';
import { useIncidents, useCreateIncident } from '@/lib/queries';
import { tsLong, fmtNum } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { Incident, IncidentStatus } from '@/lib/types';

// Columns for the shared sortable + resizable DataTable. Ongoing
// incidents (no resolvedAt) sort as longest-duration.
const INCIDENT_COLS: DataTableColumn<Incident>[] = [
  { id: 'status',   label: 'Status',   sortValue: i => i.status,   naturalDir: 'asc', width: 120 },
  { id: 'severity', label: 'Severity', sortValue: i => i.severity, naturalDir: 'asc', width: 120 },
  { id: 'title',    label: 'Title',    sortValue: i => i.title,    naturalDir: 'asc', width: 320 },
  { id: 'service',  label: 'Service',  sortValue: i => i.service,  naturalDir: 'asc', width: 180 },
  { id: 'started',  label: 'Started',  sortValue: i => i.startedAt, naturalDir: 'desc', width: 170 },
  { id: 'duration', label: 'Duration', sortValue: i => (i.resolvedAt ? i.resolvedAt - i.startedAt : Number.MAX_SAFE_INTEGER), numeric: true, naturalDir: 'desc', width: 120 },
];

// /incidents — declared events the oncall acknowledges and drives to
// resolution. Auto-grouped from same-service same-severity Problems
// firing within a 30-min window; can also be created manually for
// non-alert sourced events (e.g. customer-reported issue).
export default function IncidentsPage() {
  const navigate = useNavigate();
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  const [statusFilter, setStatusFilter] = useState<'all' | IncidentStatus>('all');
  const [serviceFilter, setServiceFilter] = useState('');
  const [showNew, setShowNew] = useState(false);

  const incidentsQ = useIncidents({
    status: statusFilter === 'all' ? '' : statusFilter,
    service: serviceFilter || undefined,
    limit: 200,
  });
  const items: Incident[] | null | undefined = incidentsQ.isLoading
    ? undefined
    : incidentsQ.isError
      ? null
      : incidentsQ.data ?? [];

  // Shared sortable + resizable table (unconditional hook).
  // onOpen wires the app-wide keyboard nav: j/k select a row,
  // Enter/o open the incident detail.
  const dt = useDataTable<Incident>({
    storageKey: 'incidents', columns: INCIDENT_COLS,
    rows: items ?? [], initialSort: { id: 'started', dir: 'desc' },
    onOpen: i => navigate(`/incident?id=${i.id}`),
  });

  const counts = { open: 0, acknowledged: 0, resolved: 0 };
  for (const i of items ?? []) counts[i.status]++;

  return (
    <>
      <Topbar title="Incidents" />
      <div id="content">
        <div className="controls" style={{ marginBottom: 12 }}>
          <select value={statusFilter} onChange={e => setStatusFilter(e.target.value as 'all' | IncidentStatus)}>
            <option value="all">All statuses</option>
            <option value="open">Open</option>
            <option value="acknowledged">Acknowledged</option>
            <option value="resolved">Resolved</option>
          </select>
          <ServicePicker value={serviceFilter} onChange={setServiceFilter}
            placeholder="Filter by service…" width={220} />
          {isAdmin && (
            <button onClick={() => setShowNew(true)}>+ Declare incident</button>
          )}
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            <b style={{ color: 'var(--err)' }}>{counts.open}</b> open ·
            {' '}<b style={{ color: 'var(--warn)' }}>{counts.acknowledged}</b> ack ·
            {' '}<b style={{ color: 'var(--ok)' }}>{counts.resolved}</b> resolved
          </span>
        </div>
        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="⚠" title="No incidents">
            Incidents auto-create from same-service same-severity Problems firing within 30 minutes.
            {isAdmin && ' Or click "+ Declare incident" to create one manually.'}
          </Empty>
        )}
        {items && items.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map((i, idx) => (
                  <tr key={i.id} {...dt.rowProps(idx)}
                      style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 40px' }}
                      onMouseEnter={() => dt.nav.setSelected(idx)}
                      onClick={() => navigate(`/incident?id=${i.id}`)}>
                    <td><StatusPill s={i.status} /></td>
                    <td><SeverityPill s={i.severity} /></td>
                    <td>
                      <Link to={`/incident?id=${i.id}`} style={{ fontWeight: 600, color: 'var(--text)' }}
                            onClick={e => e.stopPropagation()}>
                        {i.title}
                      </Link>
                      {i.assignee && (
                        <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 8 }}>
                          assigned to {i.assignee}
                        </span>
                      )}
                    </td>
                    <td className="mono" style={{ fontSize: 12 }}>
                      {i.service || '—'}
                      <ClusterChipsRef clusters={i.clusters} />
                    </td>
                    <td className="mono" style={{ fontSize: 11 }}>{tsLong(i.startedAt)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {fmtDuration(i.startedAt, i.resolvedAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {showNew && (
          <NewIncidentModal onClose={() => setShowNew(false)} onCreated={() => setShowNew(false)} />
        )}
      </div>
    </>
  );
}

function StatusPill({ s }: { s: IncidentStatus }) {
  const cls = s === 'open' ? 'outage' : s === 'acknowledged' ? 'degraded' : 'operational';
  const label = s === 'open' ? 'OPEN' : s === 'acknowledged' ? 'ACK' : 'RESOLVED';
  return <span className={`status-pill status-pill-${cls}`}>{label}</span>;
}
function SeverityPill({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}

function fmtDuration(start: number, end?: number): string {
  const ns = (end ?? Date.now() * 1_000_000) - start;
  const sec = Math.floor(ns / 1e9);
  if (sec < 60)   return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm';
  if (sec < 86400) return (sec / 3600).toFixed(1) + 'h';
  return Math.floor(sec / 86400) + 'd';
}

function NewIncidentModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [i, setI] = useState<Partial<Incident>>({ severity: 'warning', title: '', service: '' });
  const [error, setError] = useState<string | null>(null);
  // Mutation hook handles busy state + cache invalidation;
  // onSuccess in the hook fires invalidateQueries(keys.incidents.all)
  // so the parent list refreshes automatically.
  const createIncident = useCreateIncident();
  const busy = createIncident.isPending;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try { await createIncident.mutateAsync(i); onCreated(); }
    catch (err: unknown) { setError(err instanceof Error ? err.message : 'Save failed'); }
  };
  return (
    <Modal
      open={true}
      onClose={onClose}
      title="Declare incident"
      size="md"
      initialFocus="input[name=title]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-incident-form" loading={busy}>Declare</Button>
        </>
      }>
      <form id="new-incident-form" onSubmit={submit} className="stack gap-3">
        <Field
          label="Title"
          name="title"
          required
          value={i.title ?? ''}
          onChange={e => setI({ ...i, title: e.target.value })}
          placeholder="Checkout service degraded — high error rate" />
        <Row gap={3}>
          <div style={{ flex: 1 }}>
            <SelectField
              label="Severity"
              value={i.severity ?? 'warning'}
              onChange={e => setI({ ...i, severity: e.target.value as 'info' | 'warning' | 'critical' })}>
              <option value="info">Info</option>
              <option value="warning">Warning</option>
              <option value="critical">Critical</option>
            </SelectField>
          </div>
          <div style={{ flex: 1 }} className="field">
            <label className="field-label">Service (optional)</label>
            <ServicePicker
              value={i.service ?? ''}
              onChange={v => setI({ ...i, service: v })}
              placeholder="Service…"
              width="100%" />
          </div>
        </Row>
        <TextareaField
          label="Summary (optional)"
          rows={3}
          value={i.summary ?? ''}
          onChange={e => setI({ ...i, summary: e.target.value })}
          placeholder="Brief one-paragraph context for the oncall — what's the impact?" />
        {error && <div className="trp-error">{error}</div>}
      </form>
    </Modal>
  );
}

