// KafkaAlertModal.tsx — v0.10.554 (Messaging Kafka Faz 5). StatementAlertModal
// (v0.10.331) emsali: hedefli kural, tek özne (servis), metrik ailesi kafka_*.
// Kapsam: servis (+ topic, + istemci); değer VictoriaMetrics'ten, kova ≈ 1 dk;
// "En az N kova" = MinSamples. Lag = bu istemcinin gördüğü partition lag'i;
// consumer group lag'i DEĞİL — modal bunu yazar, kural açıklaması da yazar.
import { useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Field } from '@/pages/settings/shared';
import { useCreateAlertRule } from '@/lib/queries/alerts';
import type { RuleTarget } from '@/lib/types';
import { KAFKA_METRICS, SEVERITIES, WINDOWS } from './constants';

export function defaultKafkaRuleName(t: Pick<RuleTarget, 'service' | 'topic' | 'clientId'>, metric: string): string {
  const what = metric === 'kafka_producer_error_rate' ? 'Kafka gönderim hatası' : 'Kafka lag';
  const scope = [t.service ?? '', t.topic ? `topic ${t.topic}` : '', t.clientId ? `istemci ${t.clientId}` : ''].filter(Boolean).join(' · ');
  return `${what}: ${scope || 'servis'}`;
}

export function KafkaAlertModal({ open, onClose, target, services, defaultMetric = 'kafka_lag_max' }: {
  open: boolean; onClose: () => void;
  /** service boşsa `services` listesinden seçilir (çekmece: tüketiciler). */
  target: Omit<RuleTarget, 'kind'>;
  services?: string[];
  defaultMetric?: 'kafka_lag_max' | 'kafka_producer_error_rate';
}) {
  const create = useCreateAlertRule();
  const [service, setService] = useState(target.service || services?.[0] || '');
  const [metric, setMetric] = useState<string>(defaultMetric);
  const [name, setName] = useState(() => defaultKafkaRuleName({ ...target, service: target.service || services?.[0] || '' }, defaultMetric));
  const [threshold, setThreshold] = useState(defaultMetric === 'kafka_lag_max' ? 1000 : 1);
  const [windowSec, setWindowSec] = useState(600);
  const [minSamples, setMinSamples] = useState(3);
  const [forSec, setForSec] = useState(300);
  const [severity, setSeverity] = useState('warning');
  const [err, setErr] = useState<string | null>(null);
  const t: RuleTarget = { kind: 'kafka_client', service, topic: target.topic || undefined, clientId: target.clientId || undefined };
  const save = async () => {
    setErr(null);
    try {
      await create.mutateAsync({ name, service: '', metric, comparator: '>', threshold, windowSec, severity, enabled: true, minSamples, forSec, cooldownSec: 900, target: t });
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'save failed');
    }
  };
  const unit = metric === 'kafka_lag_max' ? 'kayıt' : 'kayıt/sn';
  return (
    <Modal open={open} onClose={onClose} title="Kafka istemcisi için alarm kuralı"
      footer={<>
        <Button variant="secondary" onClick={onClose}>Vazgeç</Button>
        <Button variant="primary" onClick={save} loading={create.isPending} disabled={!name || !service || threshold <= 0}>Kaydet</Button>
      </>}>
      <div style={{ display: 'grid', gap: 10 }}>
        <div className="mono" style={{ fontSize: 11 }}>
          {service || '—'}{target.topic ? ` · topic ${target.topic}` : ' · tüm topic\'ler'}{target.clientId ? ` · istemci ${target.clientId}` : ''}
        </div>
        {!target.service && (services?.length ?? 0) > 1 && (
          <Field label="Servis (tüketici)">
            <select value={service} onChange={e => { setService(e.target.value); setName(defaultKafkaRuleName({ ...target, service: e.target.value }, metric)); }}>
              {services!.map(s => <option key={s} value={s}>{s}</option>)}
            </select>
          </Field>
        )}
        <Field label="Kural adı"><input value={name} onChange={e => setName(e.target.value)} style={{ width: '100%' }} /></Field>
        <div className="grid-3" style={{ display: 'grid', gap: 10 }}>
          <Field label="Ölçü">
            <select value={metric} onChange={e => { setMetric(e.target.value); setThreshold(e.target.value === 'kafka_lag_max' ? 1000 : 1); setName(defaultKafkaRuleName({ ...target, service }, e.target.value)); }}>
              {KAFKA_METRICS.map(m => <option key={m.v} value={m.v}>{m.label}</option>)}
            </select>
          </Field>
          <Field label={`Eşik (${unit}, >)`}><input type="number" min={0.1} step={metric === 'kafka_lag_max' ? 100 : 0.5} value={threshold} onChange={e => setThreshold(Number(e.target.value))} /></Field>
          <Field label="Pencere">
            <select value={windowSec} onChange={e => setWindowSec(Number(e.target.value))}>
              {WINDOWS.map(w => <option key={w.v} value={w.v}>{w.label}</option>)}
            </select>
          </Field>
          <Field label="En az N kova"><input type="number" min={1} max={60} value={minSamples} onChange={e => setMinSamples(Number(e.target.value))} /></Field>
          <Field label="Süreklilik (sn)"><input type="number" min={0} step={60} value={forSec} onChange={e => setForSec(Number(e.target.value))} /></Field>
          <Field label="Şiddet">
            <select value={severity} onChange={e => setSeverity(e.target.value)}>
              {SEVERITIES.map(sv => <option key={sv} value={sv}>{sv}</option>)}
            </select>
          </Field>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Değer VictoriaMetrics'ten (OTel Java agent kafka-clients-metrics), kova ≈ 1 dk; pencere değeri en kötü kova.
          Lag = bu istemcinin gördüğü partition lag'i, consumer group lag'i DEĞİL. Problem servis öznesiyle açılır.
        </div>
        {err && <div style={{ fontSize: 11, color: 'var(--err)' }}>{err}</div>}
      </div>
    </Modal>
  );
}
