import { useEffect, useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { X } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Sparkline } from '@/components/Sparkline';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtBytes, fmtAgoNs } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { HostRow, HostDetail, TimeRange } from '@/lib/types';

// /hosts — host/pod inventory (v0.8.449, SigNoz/Uptrace gap-closure
// Wave 3 / A4). One row per host_name emitting metrics in the window:
// latest CPU / memory, which services run there, liveness. The global
// sibling of the Service Overview "Instances" card; row click opens a
// URL-first ?host= drawer with the per-minute CPU/mem trend and the
// per-service breakdown. Window clamps to ≤6h server-side — this page
// answers "what runs where NOW", not archaeology.

const HOST_COLS: DataTableColumn<HostRow>[] = [
  { id: 'host',     label: 'Host',      sortValue: r => r.host,      naturalDir: 'asc', width: 230 },
  { id: 'zone',     label: 'Zone',      sortValue: r => r.zone ?? '', naturalDir: 'asc', width: 100 },
  { id: 'services', label: 'Services',  sortValue: r => r.services.join(','), naturalDir: 'asc', width: 220 },
  { id: 'cpuPct',   label: 'CPU %',     sortValue: r => r.cpuPct,    numeric: true, width: 90 },
  { id: 'memBytes', label: 'Memory',    sortValue: r => r.memBytes,  numeric: true, width: 100 },
  { id: 'memPct',   label: 'Mem %',     sortValue: r => r.memPct,    numeric: true, width: 80 },
  { id: 'up',       label: 'Status',    sortValue: r => (r.up ? 1 : 0), numeric: true, width: 80 },
  { id: 'lastSeen', label: 'Last seen', sortValue: r => r.lastSeen,  numeric: true, width: 110 },
];

export default function HostsPage() {
  const [range, setRange] = useUrlRange('15m');
  // Memoized on range identity — the v0.5.184 incident shape.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['hosts', from, to],
    queryFn: () => api.hosts(from, to),
    staleTime: 60_000,
    placeholderData: keepPreviousData,
  });
  const rows: HostRow[] | null | undefined =
    q.isPending ? undefined : q.isError ? null : q.data ?? [];

  // URL-first drawer (house rule §4).
  const [params, setParams] = useSearchParams();
  const openHostParam = params.get('host');
  const openHost = (h: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.set('host', h);
    return next;
  }, { replace: true });
  const closeHost = () => setParams(prev => {
    const next = new URLSearchParams(prev);
    next.delete('host');
    return next;
  }, { replace: true });

  const dt = useDataTable<HostRow>({
    storageKey: 'hosts',
    columns: HOST_COLS,
    rows: rows ?? [],
    initialSort: { id: 'cpuPct', dir: 'desc' },
  });

  return (
    <>
      <Topbar title="Hosts" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Every host/pod that emitted metrics in the window — latest CPU and
          memory per <code>host.name</code>. Click a row for the trend and the
          per-service breakdown. Windows are capped at 6h.
        </div>

        {rows === undefined && <TableSkeleton cols={8} wideFirst />}
        {rows === null && <Empty icon="✗" title="Failed to load hosts" />}
        {rows && rows.length === 0 && (
          <Empty icon="◇" title="No host metrics in this window">
            No <code>host.name</code>-tagged runtime metrics arrived. Enable an
            OTel resource detector (host / k8s) on the SDKs or the collector.
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(r => (
                  <tr key={r.host}
                    onClick={() => openHost(r.host)}
                    style={{
                      cursor: 'pointer',
                      contentVisibility: 'auto',
                      containIntrinsicSize: 'auto 36px',
                    }}>
                    <td>
                      <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, fontWeight: 500 }}>
                        {r.host}
                      </span>
                    </td>
                    <td style={{ fontSize: 11, color: 'var(--text2)' }}>{r.zone || '—'}</td>
                    <td onClick={e => e.stopPropagation()}>
                      <span style={{ fontSize: 11, color: 'var(--text2)' }} title={r.services.join(', ')}>
                        {r.services.slice(0, 2).map((s, i) => (
                          <span key={s}>
                            {i > 0 && ', '}
                            <Link to={`/service?name=${encodeURIComponent(s)}`} style={{ fontSize: 11 }}>{s}</Link>
                          </span>
                        ))}
                        {r.services.length > 2 && ` +${r.services.length - 2}`}
                      </span>
                    </td>
                    <td className="num mono" style={{
                      color: r.cpuPct > 85 ? 'var(--err)' : r.cpuPct > 60 ? 'var(--warn)' : undefined,
                    }}>{r.cpuPct.toFixed(1)}</td>
                    <td className="num mono">{fmtBytes(r.memBytes)}</td>
                    <td className="num mono" style={{
                      color: r.memPct > 85 ? 'var(--err)' : r.memPct > 60 ? 'var(--warn)' : 'var(--text3)',
                    }}>{r.memPct > 0 ? r.memPct.toFixed(0) : '—'}</td>
                    <td>
                      <span className={`badge ${r.up ? 'b-ok' : 'b-gray'}`}>{r.up ? 'up' : 'stale'}</span>
                    </td>
                    <td className="num mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                      {fmtAgoNs(r.lastSeen)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {openHostParam && (
          <HostDrawer host={openHostParam} range={range} onClose={closeHost} />
        )}
      </div>
    </>
  );
}

// HostDrawer — the shared drawer language (overlay + slide-in, Esc
// closes). Payload fetched on open only; trends via Sparkline.
function HostDrawer({ host, range, onClose }: {
  host: string;
  range: TimeRange;
  onClose: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['host-detail', host, from, to],
    queryFn: () => api.hostDetail(host, from, to),
    staleTime: 60_000,
  });
  const detail: HostDetail | null | undefined =
    q.isPending ? undefined : q.isError ? null : q.data;

  const trend = detail?.trend ?? [];

  return (
    <>
      <div onClick={onClose}
        style={{
          position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
          zIndex: 30, animation: 'fadeIn 120ms ease-out',
        }} />
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: 'min(560px, 100vw)',
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
        zIndex: 31, overflowY: 'auto', padding: 16,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
          <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 14, fontWeight: 600 }}>
            {host}
          </span>
          {detail?.zone && (
            <span className="badge b-gray" title="cloud.availability_zone">{detail.zone}</span>
          )}
          <span style={{ flex: 1 }} />
          <button className="sec" onClick={onClose} aria-label="Close"
            style={{ padding: '4px 6px', display: 'inline-flex' }}>
            <X size={14} />
          </button>
        </div>

        {detail === undefined && <Spinner />}
        {detail === null && <Empty icon="✗" title="Failed to load host detail" />}
        {detail && (
          <>
            <DrawerSection title="Trend (per minute)">
              {trend.length === 0 ? (
                <div style={{ fontSize: 12, color: 'var(--text3)' }}>No samples in this window.</div>
              ) : (
                <div style={{ display: 'grid', gap: 6 }}>
                  <TrendRow label="CPU %" values={trend.map(p => p.cpuPct)} color="var(--warn)" />
                  <TrendRow label="Memory" values={trend.map(p => p.memBytes)} color="var(--accent2)" />
                </div>
              )}
            </DrawerSection>

            <DrawerSection title={`Services on this host (${detail.services.length})`}>
              {detail.services.length === 0 ? (
                <div style={{ fontSize: 12, color: 'var(--text3)' }}>No services in this window.</div>
              ) : (
                <table style={{ width: '100%', fontSize: 12 }}>
                  <thead>
                    <tr style={{ color: 'var(--text3)', fontSize: 11, textAlign: 'left' }}>
                      <th>Service</th>
                      <th className="num">CPU %</th>
                      <th className="num">Memory</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.services.map(s => (
                      <tr key={s.service}>
                        <td>
                          <Link to={`/service?name=${encodeURIComponent(s.service)}`}
                            style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                            {s.service}
                          </Link>
                        </td>
                        <td className="num mono">{s.cpuPct.toFixed(1)}</td>
                        <td className="num mono">{fmtBytes(s.memBytes)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </DrawerSection>
          </>
        )}
      </div>
    </>
  );
}

function DrawerSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 18 }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase',
        letterSpacing: 0.5, marginBottom: 6, fontWeight: 600,
      }}>{title}</div>
      {children}
    </div>
  );
}

function TrendRow({ label, values, color }: { label: string; values: number[]; color: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <span style={{ fontSize: 11, color: 'var(--text2)', width: 60 }}>{label}</span>
      <Sparkline values={values} width={420} height={34} color={color} title={label} />
    </div>
  );
}
