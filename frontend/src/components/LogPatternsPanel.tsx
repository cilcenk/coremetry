// LogPatternsPanel.tsx — v0.10.298 (log-search audit Dilim 2b): /logs
// "Desenler" — pencere içindeki mesajlar NormalizeSignature imzasıyla
// gruplanmış (sunucu, ÖRNEKLEMELİ ≤cap satır). Sayımlar örneğe göredir;
// altbilgi bunu SÖYLER. Fetch yalnız panel açıkken (v0.8.270 disiplini).
// Satır / "Ara" → şablondan türetilen tırnaklı AND sorgusu serbest metne
// yazılır (iki backend de anlar; yaklaşıktır, öyle etiketlenir).
import { useMemo } from 'react';
import { useLogsPatterns } from '@/lib/queries';
import type { LogsParams } from '@/lib/api';
import type { LogPatternGroup } from '@/lib/types';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from '@/components/Spinner';
import { sevClass, sevName, tsLong } from '@/lib/utils';

export function agoLabel(ns: number, nowMs = Date.now()): string {
  const s = Math.max(0, Math.round((nowMs - ns / 1e6) / 1000));
  if (s < 60) return `${s} sn önce`;
  if (s < 3600) return `${Math.round(s / 60)} dk önce`;
  if (s < 86400) return `${Math.round(s / 3600)} sa önce`;
  return `${Math.round(s / 86400)} g önce`;
}

const COLS: DataTableColumn<LogPatternGroup>[] = [
  { id: 'template', label: 'Desen', sortValue: r => r.template, naturalDir: 'asc', flex: true },
  { id: 'count', label: 'Sayı', sortValue: r => r.count, numeric: true, width: 110 },
  { id: 'severity', label: 'Seviye', sortValue: r => r.severity, numeric: true, width: 84 },
  { id: 'services', label: 'Servisler', sortValue: r => r.services.join(','), naturalDir: 'asc', width: 170 },
  { id: 'lastSeen', label: 'Son', sortValue: r => r.lastSeen, numeric: true, width: 96 },
  { id: 'act', label: '', sortValue: () => 0, width: 64 },
];

export function LogPatternsPanel({ params, open, onSearch }: {
  params: LogsParams;
  open: boolean;
  onSearch: (query: string) => void;
}) {
  const q = useLogsPatterns({ ...params, limit: 50 }, open);
  const rows = useMemo(() => q.data?.groups ?? [], [q.data]);
  const dt = useDataTable<LogPatternGroup>({
    storageKey: 'logs-patterns',
    columns: COLS,
    rows,
    initialSort: { id: 'count', dir: 'desc' },
    onOpen: r => { if (r.query) onSearch(r.query); },
  });
  if (!open) return null;
  const d = q.data;
  const maxCount = rows.reduce((m, r) => Math.max(m, r.count), 0);
  return (
    <div className="card lp-panel" style={{ padding: '10px 12px', marginBottom: 10 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6 }}>
        <h3 style={{ margin: 0, fontSize: 12 }}>Desenler</h3>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {d ? (
            <>
              {d.sampled.toLocaleString()} örnek satır{d.truncated ? ` (tavan ${d.cap.toLocaleString()})` : ''}
              {' · '}pencere toplamı {d.total.toLocaleString()}{' · '}{d.distinct} desen
              {d.degraded && <span className="badge b-warn" style={{ marginLeft: 6 }}>{d.reason ?? 'degraded'}</span>}
            </>
          ) : 'sayımlar en yeni örnek satırlara göredir'}
        </span>
      </div>
      {q.isPending && <Spinner label="Desenler çıkarılıyor…" />}
      {q.isError && <Empty icon="⚠" title="Desenler alınamadı" compact>{q.error instanceof Error ? q.error.message : ''}</Empty>}
      {d && rows.length === 0 && !q.isPending && <Empty icon="≡" title="Bu pencerede desen yok" compact />}
      {rows.length > 0 && (
        <div className="table-wrap is-fit">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(r => {
                const share = maxCount > 0 ? (r.count / maxCount) * 100 : 0;
                return (
                  <tr key={r.hash} className="lp-row" title={r.sample} tabIndex={0}
                    onClick={() => { if (r.query) onSearch(r.query); }}
                    onKeyDown={e => { if (e.key === 'Enter' && r.query) { e.preventDefault(); onSearch(r.query); } }}
                    style={{ cursor: r.query ? 'pointer' : 'default', contentVisibility: 'auto', containIntrinsicSize: '0 26px' }}>
                    <td className="mono" style={{ fontSize: 11.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.template}</td>
                    <td className="num">
                      <span className="lp-bar" style={{ width: `${share}%` }} aria-hidden="true" />
                      <span style={{ position: 'relative' }}>{r.count.toLocaleString()}</span>
                    </td>
                    <td><span className={sevClass(r.severity)}>{r.severityText || sevName(r.severity)}</span></td>
                    <td className="mono" style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                        title={r.services.join(', ')}>
                      {r.services.join(', ')}{r.serviceCount > r.services.length ? ` +${r.serviceCount - r.services.length}` : ''}
                    </td>
                    <td className="num" style={{ fontSize: 11, color: 'var(--text2)' }} title={tsLong(r.lastSeen)}>{agoLabel(r.lastSeen)}</td>
                    <td>
                      {r.query && (
                        <Button variant="secondary" size="xs" className="lp-search"
                          title={`Ara: ${r.query}`}
                          onClick={e => { e.stopPropagation(); onSearch(r.query); }}>Ara</Button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
