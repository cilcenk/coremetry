import { useMemo, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { useOperatorEvents, useDeleteOperatorEvent } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange } from '@/lib/types';

// v0.6.15 — Operator events list/delete UI.
//
// Pairs with v0.5.476 (event ingest schema) + v0.5.478
// (EventMarkers overlay). Before this page, events could be
// created via Cmd-K + show up as chart markers, but there was no
// place to LIST them, audit who marked what, or DELETE a marker
// that turned out to be wrong. This closes the loop: editors can
// see every event from the last N hours, filter by service/kind,
// click delete on any row.
//
// Permissions:
//   • Viewer  — sees the list, no delete button.
//   • Editor+ — sees + deletes (auth.RequireAnyRole(editorRoles) on
//               the backend route).
//
// UX choices:
//   • Time window picked at the page level via Topbar — same
//     pattern as /problems and /anomalies.
//   • Default ordering: newest first (ListEvents server-side
//     orders by `time` desc).
//   • Service column links to /service?name=X (same as the
//     EventMarkers tooltip), kind column shows a coloured chip.
//   • Link column opens in a new tab if present.
//
// No bulk-delete affordance yet — events are intentionally
// few-per-day, so per-row delete is enough. If the list ever
// grows past a few hundred a day the schema is the bottleneck,
// not this page.

type Event = {
  id: string;
  kind: string;
  label: string;
  time: number;       // unix ns
  service: string;
  link: string;
  owner: string;
  createdAt: number;  // unix ns
};

// Kind→colour palette matches EventMarkers.tsx so the same event
// reads consistently on the chart overlay and on this page.
const KIND_COLOURS: Record<string, string> = {
  deploy:      'rgb(46,160,67)',
  config:      'rgb(56,139,253)',
  incident:    'rgb(220,38,38)',
  maintenance: 'rgb(217,143,28)',
  custom:      'var(--text2)',
};

export default function EventsPage() {
  const { user } = useAuth();
  const canDelete = user?.role === 'admin' || user?.role === 'editor';

  const [range, setRange] = useUrlRange('24h');
  const [serviceFilter, setServiceFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [busyDelete, setBusyDelete] = useState<string | null>(null);

  // Eagerly compute the bounds so the query key gets a stable pair
  // instead of re-evaluating timeRangeToNs(range) every render (the
  // v0.5.184 incident shape).
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const eventsQ = useOperatorEvents({
    from: Math.floor(from / 1_000_000_000),
    to:   Math.floor(to / 1_000_000_000),
    service: serviceFilter || undefined,
    kind: kindFilter || undefined,
    limit: 500,
  });
  const data: Event[] | null | undefined =
    eventsQ.isPending ? undefined : eventsQ.isError ? null : (eventsQ.data ?? []) as Event[];
  // Deletion drops the row from the cached list in place (no
  // refetch) — same optimistic removal the manual setData did.
  const deleteEvent = useDeleteOperatorEvent();

  const onDelete = async (id: string) => {
    if (!canDelete) return;
    if (!confirm('Delete this event marker?')) return;
    setBusyDelete(id);
    try {
      await deleteEvent.mutateAsync(id);
    } catch (e) {
      alert('Delete failed: ' + (e as Error).message);
    } finally {
      setBusyDelete(null);
    }
  };

  // Shared sortable + resizable table. Actions column only for deleters.
  const eventCols = useMemo<DataTableColumn<Event>[]>(() => [
    { id: 'time',    label: 'Time',    sortValue: e => e.time,    naturalDir: 'desc', width: 150 },
    { id: 'kind',    label: 'Kind',    sortValue: e => e.kind,    naturalDir: 'asc',  width: 120 },
    { id: 'label',   label: 'Label',   sortValue: e => e.label,   naturalDir: 'asc',  width: 300 },
    { id: 'service', label: 'Service', sortValue: e => e.service, naturalDir: 'asc',  width: 170 },
    { id: 'owner',   label: 'Owner',   sortValue: e => e.owner,   naturalDir: 'asc',  width: 130 },
    { id: 'link',    label: 'Link',    width: 80 },
    ...(canDelete ? [{ id: 'actions', label: '', width: 60 } as DataTableColumn<Event>] : []),
  ], [canDelete]);
  const dt = useDataTable<Event>({
    storageKey: 'events', columns: eventCols,
    rows: data ?? [], initialSort: { id: 'time', dir: 'desc' },
  });

  return (
    <>
      <Topbar title="Events" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 12, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>Service:</span>
          <ServicePicker value={serviceFilter} onChange={setServiceFilter}
            placeholder="(all)" width={170} />
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>Kind:</span>
          <select value={kindFilter} onChange={e => setKindFilter(e.target.value)}>
            <option value="">(all)</option>
            <option value="deploy">deploy</option>
            <option value="config">config</option>
            <option value="incident">incident</option>
            <option value="maintenance">maintenance</option>
            <option value="custom">custom</option>
          </select>
          <span style={{ flex: 1 }} />
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            {data?.length ?? 0} events · operator-marked annotations
          </span>
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load events" />}
        {data && data.length === 0 && (
          <Empty icon="◇" title="No events in this window">
            Operators mark events from Cmd-K → "Mark event". They show up as
            vertical markers on every time-series chart.
          </Empty>
        )}
        {data && data.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(ev => (
                  <tr key={ev.id} style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                    <td className="mono" style={{ fontSize: 11, color: 'var(--text3)', whiteSpace: 'nowrap' }}
                        title={new Date(ev.time / 1_000_000).toISOString()}>
                      {fmtRel(ev.time)}
                    </td>
                    <td>
                      <span style={{
                        display: 'inline-block', padding: '2px 8px',
                        fontSize: 10, fontWeight: 600, borderRadius: 4,
                        color: KIND_COLOURS[ev.kind] ?? KIND_COLOURS.custom,
                        border: `1px solid ${KIND_COLOURS[ev.kind] ?? KIND_COLOURS.custom}`,
                      }}>{ev.kind || 'custom'}</span>
                    </td>
                    <td>{ev.label}</td>
                    <td>
                      {ev.service
                        ? <a href={`/service?name=${encodeURIComponent(ev.service)}`}
                             style={{ color: 'var(--accent2)' }}>{ev.service}</a>
                        : <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                    <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                      {ev.owner || '—'}
                    </td>
                    <td>
                      {ev.link
                        ? <a href={ev.link} target="_blank" rel="noopener noreferrer"
                             style={{ color: 'var(--accent2)', fontSize: 11 }}
                             title={ev.link}>open ↗</a>
                        : <span style={{ color: 'var(--text3)' }}>—</span>}
                    </td>
                    {canDelete && (
                      <td>
                        <button
                          type="button"
                          onClick={() => onDelete(ev.id)}
                          disabled={busyDelete === ev.id}
                          title="Delete this event"
                          style={{
                            background: 'transparent', border: '1px solid var(--border)',
                            color: 'var(--err)', padding: '2px 8px', borderRadius: 4,
                            cursor: 'pointer', fontSize: 11,
                          }}
                        >
                          {busyDelete === ev.id ? '…' : '✕'}
                        </button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

// fmtRel — "2 min ago" / "3 h ago" / "Feb 14, 09:30" style.
// Falls back to local-tz short timestamp past 1 day to keep
// older events readable without mental arithmetic.
function fmtRel(ns: number): string {
  const ms = ns / 1_000_000;
  const diff = Date.now() - ms;
  if (diff < 60_000)     return `${Math.round(diff / 1000)}s ago`;
  if (diff < 3_600_000)  return `${Math.round(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)}h ago`;
  return new Date(ms).toLocaleString(undefined, {
    month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit',
  });
}
