// ServiceKafkaClientsPanel.tsx — v0.10.552 (Messaging Kafka Faz 3; operatör
// yer kararı 2026-09-08: "önerin" = Infra sekmesi). Servisin Kafka İSTEMCİ
// sağlığı (topic'e değil istemciye ait metrikler): StatTile şeridi + üç
// MetricArea (sekmenin CPU/Mem panelleriyle aynı bileşen, aynı imleç senkronu
// `infra:<service>`). Thanos'tan BAĞIMSIZ: sekme Thanos yokken Empty çizse de
// bu panel kendi verisiyle çizilir. Metrik yoksa tek satır soluk not.
import { useMemo } from 'react';
import { StatTile } from '@/components/ui/StatTile';
import { Spinner } from '@/components/Spinner';
import { MetricArea } from '@/pages/clusters/MetricArea';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { useServiceKafkaClients } from '@/lib/queries/messaging';
import { kafkaDegradeTR } from '@/features/dependencies/kafkaClients';
import { kafkaStrip, kafkaStripEmpty, kafkaToNamedSeries } from './serviceKafkaClients';
import type { TimeRange } from '@/lib/types';

const fmtOr = (v: number | null, f: (n: number) => string = n => fmtNum(n)) => (v === null ? '—' : f(v));
const fmtMs = (n: number) => `${fmtNum(Math.round(n * 10) / 10)} ms`;

export function ServiceKafkaClientsPanel({ service, range, onZoom, onZoomReset }: {
  service: string; range: TimeRange;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
}) {
  const [env] = useUrlEnv();
  const win = useMemo(() => timeRangeToNs(range), [range]); // v0.5.184: memo içinde
  const q = useServiceKafkaClients({ service, fromNs: win.from, toNs: win.to, env: env || undefined });
  if (q.isPending) {
    return <div className="kc-line" role="status" aria-busy="true"><Spinner /> Kafka istemci metrikleri…</div>;
  }
  const data = q.isError ? null : (q.data ?? null);
  const degrade = kafkaDegradeTR(data);
  if (degrade || !data) {
    return <div className="kc-line" title={data?.note}>◌ {degrade}</div>;
  }
  const strip = kafkaStrip(data.blocks);
  const err = kafkaToNamedSeries(data.blocks.producer_error_rate);
  const lag = kafkaToNamedSeries(data.blocks.consumer_lag_max);
  const lat = kafkaToNamedSeries(data.blocks.producer_request_latency_avg);
  const sync = `infra:${service}`;
  return (
    <section className="kc-sec" aria-label="Kafka client (metrik)" style={{ marginTop: 14 }}>
      <h3 style={{ fontSize: 13, margin: '4px 0 8px' }}>
        Kafka client · {service}
        <span className="badge b-gray" style={{ marginLeft: 8 }} title={data.note}>
          {data.source} · OTel Java agent
        </span>
        {data.envAmbiguous && (
          <span className="badge b-warn" style={{ marginLeft: 6 }}
            title="env filtresi bu depoda ifade edilemiyor; seriler TÜM ortamları kapsıyor.">
            env uygulanmadı
          </span>
        )}
      </h3>
      <div className="kc-note">{data.note}</div>
      {!kafkaStripEmpty(strip) && (
        <div className="kc-grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))' }}>
          <StatTile label="Açık bağlantı · üretici">{fmtOr(strip.producerConnections)}</StatTile>
          <StatTile label="Açık bağlantı · tüketici">{fmtOr(strip.consumerConnections)}</StatTile>
          <StatTile label="Broker gecikme · ort.">{fmtOr(strip.latencyAvgMs, fmtMs)}</StatTile>
          <StatTile label="Broker gecikme · maks." tone={strip.latencyMaxMs !== null && strip.latencyMaxMs > 1000 ? 'warn' : undefined}>{fmtOr(strip.latencyMaxMs, fmtMs)}</StatTile>
          <StatTile label="Rebalance / saat" tone={strip.rebalancePerHour !== null && strip.rebalancePerHour > 0 ? 'warn' : undefined}>{fmtOr(strip.rebalancePerHour)}</StatTile>
          <StatTile label="Son poll · maks." tone={strip.lastPollSecMax !== null && strip.lastPollSecMax > 300 ? 'err' : undefined}>{fmtOr(strip.lastPollSecMax, n => `${fmtNum(Math.round(n))} s`)}</StatTile>
        </div>
      )}
      <div className="grid-2" style={{ display: 'grid', gap: 14 }}>
        <MetricArea title="Gönderim hatası (kayıt/sn) · topic" subtitle="kafka.producer.record_error_rate · by topic"
          series={err.series} seriesName="hata/sn" totalSeries={err.total} maxSeries={12}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={sync} />
        <MetricArea title="İstemcinin gördüğü en yüksek lag · topic · istemci" subtitle="kafka.consumer.records_lag_max · partition maks., group lag değil"
          series={lag.series} seriesName="lag" totalSeries={lag.total} maxSeries={12}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={sync} />
        <MetricArea title="Broker istek gecikmesi (ms) · istemci" subtitle="kafka.producer.request_latency_avg · by client_id"
          series={lat.series} seriesName="gecikme" unit="ms" totalSeries={lat.total} maxSeries={12}
          onZoom={onZoom} onZoomReset={onZoomReset} syncKey={sync} />
      </div>
    </section>
  );
}
