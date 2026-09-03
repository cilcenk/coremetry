// PodContextTables — v0.10.160 (A anatomisi §6, §8, §9). Konteynerler (Thanos
// KSM anlık — zaman serisi YOK), kardeş pod'lar (entity siblings ≤50 ×
// /api/clusters/pods topk 500; listede olmayan pod'da faz/restart/cpu/mem
// BİLİNMİYOR), etiketler ve ömür tarihçesi. v0.10.135 PodEntityPanel'in çip/
// link satırları tabloya terfi etti; kardeş tablosunda «Node» sütunu bilinçli
// YOK (siblings EntityRecord döner, runs_on yalnız hedef pod için çözülür —
// inceleme must-fix). Konteyner/etiket tabloları ≤ ~10 satır ve sıralanmaz →
// ham <table> meşru; kardeşler useDataTable.
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Badge, Button } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { fmtDateTime, fmtBytes } from '@/lib/utils';
import { fmtCores, podPhaseBadge } from '@/pages/clusters/thresholds';
import { entityHref, entityLiveness } from '@/lib/entityHref';
import type { EntityContainersResponse, EntityRecord, TimeRange } from '@/lib/types';
import type { SiblingRow } from './podPage';

function containerTone(c: { readyKnown: boolean; ready: boolean; restarts: number; lastTermReason?: string }): 'neutral' | 'success' | 'danger' | 'warning' {
  if (!c.readyKnown) return 'neutral';
  if (!c.ready) return 'danger';
  return c.restarts > 0 || c.lastTermReason ? 'warning' : 'success';
}

export function PodContainersTable({ ctr, pending, containerRecs }: {
  ctr?: EntityContainersResponse;
  pending: boolean;
  /** entity çocukları — KSM serisi yokken en azından adlar */
  containerRecs?: EntityRecord[];
}) {
  if (pending) return <Spinner />;
  const rows = ctr?.containers ?? [];
  return (
    <>
      {ctr?.error && <div className="pod-cap"><Badge tone="warning" title={ctr.error}>Thanos: durum alınamadı</Badge></div>}
      {rows.length === 0 ? (
        <div className="pod-cap">KSM serisi yok{containerRecs && containerRecs.length > 0 ? ` · konteynerler: ${containerRecs.map(c => c.name).join(', ')}` : ''}</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Ad</th><th>Ready</th><th className="num">Restarts</th><th>Waiting</th><th>Son sonlanma</th></tr></thead>
            <tbody>
              {rows.map(c => (
                <tr key={c.name}>
                  <td className="mono">{c.name}</td>
                  <td>{c.readyKnown ? <Badge tone={containerTone(c)}>{c.ready ? 'ready' : 'not ready'}</Badge> : <span className="field-hint" title="kube_pod_container_status_ready serisi yok">?</span>}</td>
                  <td className="num" style={c.restarts > 0 ? { color: 'var(--warn)' } : undefined}>{c.restarts}</td>
                  <td>{c.waitingReason ? <Badge tone="danger">{c.waitingReason}</Badge> : '—'}</td>
                  <td>{c.lastTermReason ? <Badge tone="warning">{c.lastTermReason}</Badge> : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="pod-cap">
        <code className="mono">GET /api/entity/containers</code> · kube_pod_container_status_{'{'}ready,restarts_total,waiting_reason,last_terminated_reason{'}'} · anlık — konteyner başına zaman serisi yok (CPU/Mem pod toplamıdır).
      </div>
    </>
  );
}

const SIB_COLS: DataTableColumn<SiblingRow>[] = [
  { id: 'pod', label: 'Pod', width: 280, sortValue: r => r.name, naturalDir: 'asc' },
  { id: 'status', label: 'Durum', width: 90, sortValue: r => entityLiveness(r.rec) },
  { id: 'phase', label: 'Faz', width: 110, sortValue: r => r.phase ?? '' },
  { id: 'restarts', label: 'Restarts', width: 90, numeric: true, sortValue: r => r.restarts ?? -1 },
  { id: 'cpu', label: 'CPU', width: 90, numeric: true, sortValue: r => r.cpuCores ?? -1 },
  { id: 'mem', label: 'Mem', width: 100, numeric: true, sortValue: r => r.memBytes ?? -1 },
];

export function PodSiblingsTable({ rows, pageRange, at, clusterName, truncated }: {
  rows: SiblingRow[];
  pageRange: TimeRange;
  at?: number;
  clusterName: string;
  /** /api/clusters/pods topk 500'e dayandı — «listede yok» kanıt değil */
  truncated: boolean;
}) {
  const dt = useDataTable<SiblingRow>({ storageKey: 'pod-siblings', columns: SIB_COLS, rows, initialSort: { id: 'restarts', dir: 'desc' } });
  if (rows.length === 0) return <div className="pod-cap">Kardeş pod yok.</div>;
  return (
    <>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(r => {
              const live = entityLiveness(r.rec);
              return (
                <tr key={r.rec.id}>
                  <td className="mono" title={`${clusterName} / ${r.rec.namespace ?? ''} / ${r.name}`}>
                    {/* service TAŞINMAZ (inceleme #16): kardeş o servisi çalıştırmıyor olabilir; yanlış RED kapsamı + yanlış geri linki olurdu. */}
                    <Link to={entityHref(r.rec, { range: pageRange, at: at || undefined, clusterName })} className="sec">{r.name}</Link>
                  </td>
                  <td>{live === 'live' ? <Badge tone="success">live</Badge> : live === 'stale' ? <Badge tone="warning">stale</Badge> : <Badge tone="danger">gone</Badge>}</td>
                  <td>{r.known && r.phase ? <span className={`badge ${podPhaseBadge(r.phase)}`}>{r.phase}</span> : <span className="field-hint" title="topk 500 listesinde yok — faz bilinmiyor">—</span>}</td>
                  <td className="num" style={(r.restarts ?? 0) > 0 ? { color: 'var(--warn)' } : undefined} title={r.lastTermReason ? `son: ${r.lastTermReason}` : undefined}>{r.restarts === null ? '—' : r.restarts}</td>
                  <td className="num mono">{r.cpuCores === null ? '—' : fmtCores(r.cpuCores)}</td>
                  <td className="num mono">{r.memBytes === null ? '—' : fmtBytes(r.memBytes)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="pod-cap">
        Kardeşler <code className="mono">/api/entity</code> (sunucu ≤ 50) · faz/restart/CPU/Mem <code className="mono">/api/clusters/pods</code> (topk 500) ile ad üzerinden{truncated ? ' — liste kesik: «—» yok demek değil' : ''}. <ResetLayoutButton dt={dt} />
      </div>
    </>
  );
}

export function PodLabelsTable({ labels }: { labels: Record<string, string> | undefined }) {
  const [all, setAll] = useState(false);
  const entries = Object.entries(labels ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return <div className="pod-cap">Etiket yok (kube_pod_labels).</div>;
  const shown = all ? entries : entries.slice(0, 8);
  return (
    <>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Anahtar</th><th>Değer</th></tr></thead>
          <tbody>{shown.map(([k, v]) => <tr key={k}><td className="mono">{k}</td><td className="mono">{v}</td></tr>)}</tbody>
        </table>
      </div>
      {entries.length > 8 && (
        <div className="pod-cap"><Button variant="secondary" size="xs" onClick={() => setAll(v => !v)}>{all ? 'daha az' : `+${entries.length - 8} etiket daha`}</Button></div>
      )}
    </>
  );
}

export function PodLifetimesTable({ lifetimes, atMatch, at }: { lifetimes: EntityRecord[]; atMatch?: boolean; at?: number }) {
  return (
    <>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Valid from</th><th>Valid to</th><th>Kaynak</th><th>uid</th></tr></thead>
          <tbody>
            {lifetimes.map(l => (
              <tr key={`${l.validFrom}|${l.uid ?? ''}`}>
                <td className="mono">{fmtDateTime(new Date(l.validFrom))}</td>
                <td className="mono">{l.validTo ? fmtDateTime(new Date(l.validTo)) : '(açık)'}</td>
                <td>{l.source}</td>
                <td className="mono">{l.uid ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="pod-cap">
        {lifetimes.length} ömür · aynı ad{!!at && atMatch === false ? ' · ?at= verilip o an geçerli ömür yoksa en yakın ömür gösterilir (atMatch=false)' : ''}.
      </div>
    </>
  );
}
