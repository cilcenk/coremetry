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
  kafkaBlockItems, kafkaDegradeTR, kafkaLastRows, kafkaPanelUnit, kafkaScopeNoteTR,
  KAFKA_CONSUMER_COLS, KAFKA_PRODUCER_COLS, type KafkaLastRow,
} from './kafkaClients';

const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

export function KafkaClientsSection({ system, cluster, destination, range, xRange, syncKey, set, title, enabled }: {
  system: string; cluster: string; destination: string; range: TimeRange;
  xRange: { from: number; to: number }; syncKey?: string;
  /**
   * v0.10.575 — istenecek SORU KÜMESİ. Verilmezse tel bugünkü hâlinde kalır
   * (çekmece hiçbirini geçmiyor, davranışı bayt-bayt aynı).
   *   • 'topic'   → çekmecenin beş sorusu, açıkça istenmiş hâli
   *   • 'clients' → bağlantı/gecikme/rebalance; blok ADLARI ÖNCEDEN BİLİNMİYOR,
   *                 o yüzden bu kipte bölüm bloklar üzerinde GENEL çizer
   *                 (blok başına panel + tek "son değer" tablosu). Sabit
   *                 kolon listesi yazmak, sunucu bir aile eklediğinde onu
   *                 sessizce görünmez yapardı.
   */
  set?: 'topic' | 'clients';
  /** Başlık metni (sayfa "Kafka istemcileri" sekmesinde kendi başlığını verir). */
  title?: string;
  /** Sorgu kapısı — sekme seçili değilken VM'e hiç gidilmez (ES/VM maliyet disiplini). */
  enabled?: boolean;
}) {
  // timeRangeToNs MEMO içinde (v0.5.184 sonsuz refetch sınıfı).
  const win = useMemo(() => timeRangeToNs(range), [range]);
  const q = useMessagingClients({ system, cluster, destination, fromNs: win.from, toNs: win.to, set, enabled });
  const [alertOpen, setAlertOpen] = useState(false); // v0.10.554 — lag alarmı modalı
  // Kapı kapalıyken sorgu HİÇ koşmaz; RQ bunu 'pending' diye raporlar ve
  // aşağıdaki dal sonsuz bir spinner çizerdi ("yükleniyor" diyen ama hiçbir
  // şey beklemeyen bir durum, v0.9.748 sınıfı).
  if (enabled === false) return null;
  if (q.isPending) {
    return <div className="kc-line" role="status" aria-busy="true"><Spinner /> Kafka istemci metrikleri…</div>;
  }
  const data = q.isError ? null : (q.data ?? null);
  const degrade = kafkaDegradeTR(data);
  if (degrade || !data) {
    return <div className="kc-line" title={data?.note}>◌ {degrade}</div>;
  }
  // v0.10.575 — `set=clients` kipinde blok ADLARI sunucudan gelir; sabit bir
  // liste yazmak, sunucu yeni bir aile eklediğinde onu sessizce görünmez
  // yapardı ("boş küme kaybolur" değil, GÖRÜNMEYEN küme).
  const blockKeys = Object.keys(data.blocks ?? {});
  const err = kafkaBlockItems(data.blocks.producer_error_rate);
  const lag = kafkaBlockItems(data.blocks.consumer_lag_max);
  const producers = kafkaLastRows(data.blocks, KAFKA_PRODUCER_COLS.map(c => c.id));
  const consumers = kafkaLastRows(data.blocks, KAFKA_CONSUMER_COLS.map(c => c.id));
  const errBlock = data.blocks.producer_error_rate;
  const lagBlock = data.blocks.consumer_lag_max;
  const scopeNote = kafkaScopeNoteTR(data.scope);
  return (
    <section className="kc-sec" aria-label="Kafka istemcileri (metrik)">
      <div className="kc-head">
        <span aria-hidden className="kc-dot" />
        {title ?? 'Kafka istemcileri'} · METRİK ({data.source})
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
      {/* v0.10.575 — KAPSAM BEYANI notun ÜSTÜNDE. `set=clients` aileleri Kafka
          istemcisinde topic etiketi taşımıyor: seriler bu topic'e dokunan
          SERVİSLERİN tamamı. Beyansız bir panel, başka bir topic'in yükünü
          buranınmış gibi okutur. */}
      {scopeNote && <div className="kc-scope">{scopeNote}</div>}
      <div className="kc-note">
        {data.note}
      </div>
      {set === 'clients' ? (
        <>
          {blockKeys.length === 0 ? (
            <div className="kc-empty">Bu pencerede istemci metriği serisi yok.</div>
          ) : (
            <>
              <div className="kc-grid">
                {blockKeys.map(k => {
                  const b = data.blocks[k];
                  const it = kafkaBlockItems(b);
                  return (
                    <LazyMount key={k} minHeight={170}>
                      <Suspense fallback={<div className="kc-fallback"><Spinner /></div>}>
                        <CorePanelMultiLazy
                          title={b.label || k}
                          storageKey={`msg-topic-kafka-${k}`}
                          height={150} unit={kafkaPanelUnit(b.unit)} xRange={xRange} syncKey={syncKey}
                          note={b.error ? `sorgu hatası: ${b.error}`
                            : it.truncated ? `+${it.truncated} seri gösterilmiyor`
                            : b.groupBy.join(' · ') || undefined}
                          items={it.items} />
                      </Suspense>
                    </LazyMount>
                  );
                })}
              </div>
              <div className="kc-grid">
                <KafkaLastTable storageKey="msg-topic-clients-last" title="Son değer" keyLabel="servis · istemci"
                  rows={kafkaLastRows(data.blocks, blockKeys)}
                  cols={blockKeys.map(k => ({ id: k, label: data.blocks[k].label || k }))} />
              </div>
            </>
          )}
        </>
      ) : (
      <>
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
      </>
      )}
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
