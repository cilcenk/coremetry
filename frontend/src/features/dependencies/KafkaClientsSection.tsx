// KafkaClientsSection.tsx — v0.10.551 (Messaging Kafka Faz 2; operatör mockup
// onayı 2026-09-08). Çekmecede Consumers'tan SONRA, Top operations'tan ÖNCE:
// kaynak notu + iki CorePanelMulti (gönderim hatası/sn — servis; en yüksek lag —
// servis·istemci) + iki "son değer" tablosu (useDataTable). available=false →
// TEK satır soluk not (bölüm gizlenmez, sebep title'da). Liste sayfasına grafik
// KONMAZ (v0.9.834 kararı) — yalnız çekmece.
import { lazy, Suspense, useMemo, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { KafkaAlertModal } from '@/pages/alerts/KafkaAlertModal'; // v0.10.554
import { Spinner } from '@/components/Spinner';
import { LazyMount } from '@/components/LazyMount';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange } from '@/lib/types';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useMessagingClients } from '@/lib/queries/messaging';
import {
  kafkaBlockItems, kafkaDegradeTR, kafkaLastRows, kafkaPanelUnit,
  KAFKA_CONSUMER_COLS, KAFKA_PRODUCER_COLS, type KafkaLastRow,
} from './kafkaClients';

const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

export function KafkaClientsSection({ system, cluster, destination, range, xRange, syncKey }: {
  system: string; cluster: string; destination: string; range: TimeRange;
  xRange: { from: number; to: number }; syncKey?: string;
}) {
  // timeRangeToNs MEMO içinde (v0.5.184 sonsuz refetch sınıfı).
  const win = useMemo(() => timeRangeToNs(range), [range]);
  const q = useMessagingClients({ system, cluster, destination, fromNs: win.from, toNs: win.to });
  const [alertOpen, setAlertOpen] = useState(false); // v0.10.554 — lag alarmı modalı
  if (q.isPending) {
    return <div className="kc-line" role="status" aria-busy="true"><Spinner /> Kafka istemci metrikleri…</div>;
  }
  const data = q.isError ? null : (q.data ?? null);
  const degrade = kafkaDegradeTR(data);
  if (degrade || !data) {
    return <div className="kc-line" title={data?.note}>◌ {degrade}</div>;
  }
  const err = kafkaBlockItems(data.blocks.producer_error_rate);
  const lag = kafkaBlockItems(data.blocks.consumer_lag_max);
  const producers = kafkaLastRows(data.blocks, KAFKA_PRODUCER_COLS.map(c => c.id));
  const consumers = kafkaLastRows(data.blocks, KAFKA_CONSUMER_COLS.map(c => c.id));
  const errBlock = data.blocks.producer_error_rate;
  const lagBlock = data.blocks.consumer_lag_max;
  return (
    <section className="kc-sec" aria-label="Kafka istemcileri (metrik)">
      <div className="kc-head">
        <span aria-hidden className="kc-dot" />
        Kafka istemcileri · METRİK ({data.source})
        {data.consumers.length > 0 && (
          <Button variant="secondary" size="sm" style={{ marginLeft: 'auto' }} onClick={() => setAlertOpen(true)}
            title="Bu topic için tüketici lag alarmı (istemcinin gördüğü lag; consumer group lag'i değil)">
            Lag alarmı
          </Button>
        )}
      </div>
      {alertOpen && (
        <KafkaAlertModal open onClose={() => setAlertOpen(false)} services={data.consumers}
          target={{ topic: destination }} defaultMetric="kafka_lag_max" />
      )}
      <div className="kc-note">
        {data.note}
      </div>
      <div className="kc-grid">
        <LazyMount minHeight={170}>
          <Suspense fallback={<div className="kc-fallback"><Spinner /></div>}>
            <CorePanelMultiLazy
              title="Gönderim hatası/sn — servis"
              storageKey="msg-drawer-kafka-err"
              height={150} unit={kafkaPanelUnit(errBlock?.unit)} xRange={xRange} syncKey={syncKey}
              note={errBlock?.error ? `sorgu hatası: ${errBlock.error}` : err.truncated ? `+${err.truncated} seri gösterilmiyor` : undefined}
              items={err.items} />
          </Suspense>
        </LazyMount>
        <LazyMount minHeight={170}>
          <Suspense fallback={<div className="kc-fallback"><Spinner /></div>}>
            <CorePanelMultiLazy
              title="İstemcinin gördüğü en yüksek lag — servis · istemci"
              storageKey="msg-drawer-kafka-lag"
              height={150} unit={kafkaPanelUnit(lagBlock?.unit)} xRange={xRange} syncKey={syncKey}
              note={lagBlock?.error ? `sorgu hatası: ${lagBlock.error}` : lag.truncated ? `+${lag.truncated} seri gösterilmiyor` : 'partition başına, bu istemcinin gördüğü; group lag değil'}
              items={lag.items} />
          </Suspense>
        </LazyMount>
      </div>
      <div className="kc-grid">
        <KafkaLastTable storageKey="deps-kafka-producers" title="Üreticiler (son değer)" keyLabel="servis"
          rows={producers} cols={KAFKA_PRODUCER_COLS} />
        <KafkaLastTable storageKey="deps-kafka-consumers" title="Tüketiciler (son değer)" keyLabel="servis · istemci"
          rows={consumers} cols={KAFKA_CONSUMER_COLS} />
      </div>
    </section>
  );
}

function KafkaLastTable({ storageKey, title, keyLabel, rows, cols }: {
  storageKey: string; title: string; keyLabel: string; rows: KafkaLastRow[];
  cols: ReadonlyArray<{ readonly id: string; readonly label: string }>;
}) {
  const columns = useMemo<DataTableColumn<KafkaLastRow>[]>(() => [
    { id: 'key', label: keyLabel, sortValue: r => r.key, naturalDir: 'asc', width: 200 },
    ...cols.map(c => ({
      id: c.id, label: c.label, sortValue: (r: KafkaLastRow) => r.values[c.id] ?? -1,
      numeric: true, naturalDir: 'desc' as const, width: 96,
    })),
  ], [cols, keyLabel]);
  const dt = useDataTable<KafkaLastRow>({ storageKey, columns, rows, initialSort: { id: cols[0]?.id ?? 'key', dir: 'desc' } });
  return (
    <div className="kc-table">
      <div className="kc-subhead">{title} · {rows.length} <ResetLayoutButton dt={dt} /></div>
      {rows.length === 0 ? (
        <div className="kc-empty">seri yok</div>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(r => (
                <tr key={r.key}>
                  <td className="mono kc-key" title={r.key}>{r.key}</td>
                  {cols.map(c => {
                    const v = r.values[c.id];
                    return <td key={c.id} className="num">{v === null || v === undefined ? '—' : fmtNum(v)}</td>;
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
