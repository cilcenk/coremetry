// PartitionLagTable.tsx — v0.10.589. Topic sayfası "Partition'lar" sekmesi:
// en kötü 20 partition (lag azalan) + lead. Kendi VM sorgusunu (set=
// partitions) YALNIZ mount olunca kurar — sekme seçili değilken bileşen
// hiç mount edilmez (fetch-on-open). Sunucu pencereyi son 10 dk'ya
// daraltıp seriyi 50'ye tavanlar; burada 20 gösterilir, kalan sayı yazılır.
import { useMemo } from 'react';
import type { TimeRange } from '@/lib/types';
import { useMessagingClients } from '@/lib/queries/messaging';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { Spinner, Empty } from '@/components/Spinner';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { worstPartitions, PARTITION_LAG_ROWS, type PartitionLagRow } from './partitionLag';

const COLS: DataTableColumn<PartitionLagRow>[] = [
  { id: 'partition', label: 'Partition', width: 100, sortValue: r => Number(r.partition), numeric: true },
  { id: 'service', label: 'Servis', width: 220, sortValue: r => r.service },
  { id: 'client', label: 'İstemci', width: 260, sortValue: r => r.client },
  { id: 'lag', label: 'Lag', width: 110, sortValue: r => r.lag ?? -1, numeric: true },
  { id: 'lead', label: 'Lead', width: 110, sortValue: r => r.lead ?? -1, numeric: true },
];

export function PartitionLagTable({ system, cluster, destination, range }: {
  system: string; cluster: string; destination: string; range: TimeRange;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useMessagingClients({ system, cluster, destination, fromNs: from, toNs: to, set: 'partitions', enabled: true });
  const { rows, total } = useMemo(() => worstPartitions(q.data?.blocks, PARTITION_LAG_ROWS), [q.data]);
  const dt = useDataTable<PartitionLagRow>({ storageKey: 'msg-topic-partitions', columns: COLS, rows, initialSort: { id: 'lag', dir: 'desc' } });

  if (q.isPending) return <Spinner />;
  if (q.isError) return <Empty icon="⚠" title="Partition metrikleri okunamadı" />;
  if (!q.data?.available || rows.length === 0) {
    return (
      <Empty icon="◌" title="Partition lag verisi yok">
        {q.data?.note ?? 'kafka.consumer.records_lag bu topic için VM\'de bulunamadı.'}
      </Empty>
    );
  }
  const hidden = total - rows.length;
  return (
    <div className="sec">
      <div className="kc-line">
        <span>Partition'lar · en kötü {rows.length} / {total}</span>
        <span style={{ color: 'var(--text3)', marginLeft: 8 }}>son 10 dk · son değer</span>
        {hidden > 0 && <span style={{ color: 'var(--text3)', marginLeft: 8 }}>({hidden} partition daha)</span>}
        <span style={{ marginLeft: 'auto' }}><ResetLayoutButton dt={dt} /></span>
      </div>
      <div className="table-wrap">
        <table>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map(r => (
              <tr key={`${r.service}|${r.client}|${r.partition}`}>
                <td style={{ fontFamily: 'monospace' }}>{r.partition}</td>
                <td>{r.service}</td>
                <td style={{ fontFamily: 'monospace', fontSize: 12 }}>{r.client}</td>
                <td className="num mono" style={r.lag !== null && r.lag > 1000 ? { color: 'var(--warn)' } : undefined}>{r.lag === null ? '—' : fmtNum(r.lag)}</td>
                <td className="num mono" style={r.lead !== null && r.lead < 100 ? { color: 'var(--err)' } : undefined}>{r.lead === null ? '—' : fmtNum(r.lead)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
