// RouteAlertModal.tsx — v0.10.705 (Dynatrace paritesi #3). StatementAlertModal
// (v0.10.331) / KafkaAlertModal (v0.10.554) emsali: hedefli kural, özne servis,
// metrik ailesi http_route_*. Kimlik service + http.route; ölçü spanmetrics_1m
// (env-agnostik — Endpoints satırı env altında okunduysa modal bunu söyler).
// "En az N çağrı" = MinSamples (penceredeki çağrı sayısı).
import { useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Field } from '@/pages/settings/shared';
import { useCreateAlertRule } from '@/lib/queries/alerts';
import type { RuleTarget } from '@/lib/types';
import { HTTP_ROUTE_METRICS, COMPARATORS, SEVERITIES, WINDOWS, httpRouteUnit } from './constants';
import { defaultRouteRuleName, defaultRouteThreshold } from './routeAlert';

export function RouteAlertModal({ open, onClose, target, env, defaultMetric = 'http_route_p95_ms' }: {
  open: boolean; onClose: () => void;
  target: { service: string; route: string };
  /** Satırın okunduğu env (varsa): kural env-agnostik ölçer, modal uyarır. */
  env?: string;
  defaultMetric?: string;
}) {
  const create = useCreateAlertRule();
  const [metric, setMetric] = useState<string>(defaultMetric);
  const [name, setName] = useState(() => defaultRouteRuleName(target, defaultMetric));
  const [comparator, setComparator] = useState('>');
  const [threshold, setThreshold] = useState(defaultRouteThreshold(defaultMetric));
  const [windowSec, setWindowSec] = useState(600);
  const [minSamples, setMinSamples] = useState(20);
  const [forSec, setForSec] = useState(300);
  const [severity, setSeverity] = useState('warning');
  const [err, setErr] = useState<string | null>(null);
  const t: RuleTarget = { kind: 'http_route', service: target.service, route: target.route };
  const save = async () => {
    setErr(null);
    try {
      await create.mutateAsync({ name, service: '', metric, comparator, threshold, windowSec, severity, enabled: true, minSamples, forSec, cooldownSec: 900, target: t });
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'save failed');
    }
  };
  const unit = httpRouteUnit(metric);
  return (
    <Modal open={open} onClose={onClose} title="Endpoint için alarm kuralı"
      footer={<>
        <Button variant="secondary" onClick={onClose}>Vazgeç</Button>
        <Button variant="primary" onClick={save} loading={create.isPending} disabled={!name || !target.service || !target.route || threshold <= 0}>Kaydet</Button>
      </>}>
      <div style={{ display: 'grid', gap: 10 }}>
        <div className="mono" style={{ fontSize: 11 }}>{target.service} · {target.route}</div>
        <Field label="Kural adı"><input value={name} onChange={e => setName(e.target.value)} style={{ width: '100%' }} /></Field>
        <div className="grid-3" style={{ display: 'grid', gap: 10 }}>
          <Field label="Ölçü">
            <select value={metric} onChange={e => { setMetric(e.target.value); setThreshold(defaultRouteThreshold(e.target.value)); setName(defaultRouteRuleName(target, e.target.value)); }}>
              {HTTP_ROUTE_METRICS.map(m => <option key={m.v} value={m.v}>{m.label}</option>)}
            </select>
          </Field>
          <Field label="Karşılaştırma">
            <select value={comparator} onChange={e => setComparator(e.target.value)}>
              {COMPARATORS.map(c => <option key={c} value={c}>{c}</option>)}
            </select>
          </Field>
          <Field label={`Eşik (${unit})`}><input type="number" min={0.01} step={unit === 'ms' ? 50 : 0.5} value={threshold} onChange={e => setThreshold(Number(e.target.value))} /></Field>
          <Field label="Pencere">
            <select value={windowSec} onChange={e => setWindowSec(Number(e.target.value))}>
              {WINDOWS.map(w => <option key={w.v} value={w.v}>{w.label}</option>)}
            </select>
          </Field>
          <Field label="En az N çağrı"><input type="number" min={1} value={minSamples} onChange={e => setMinSamples(Number(e.target.value))} /></Field>
          <Field label="Süreklilik (sn)"><input type="number" min={0} step={60} value={forSec} onChange={e => setForSec(Number(e.target.value))} /></Field>
          <Field label="Şiddet">
            <select value={severity} onChange={e => setSeverity(e.target.value)}>
              {SEVERITIES.map(sv => <option key={sv} value={sv}>{sv}</option>)}
            </select>
          </Field>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Ölçü spanmetrics'ten (service + http.route), 1 dk kovalar, yalnız tam kovalar; hız = çağrı / pencere saniyesi.
          Problem servis öznesiyle açılır ve açıklaması endpoint sayfasını taşır.
          {env && <> <b>Not:</b> satır <code>env={env}</code> altında okundu; kural TÜM env&apos;leri birlikte ölçer (MV&apos;de env boyutu yok).</>}
        </div>
        {err && <div style={{ fontSize: 11, color: 'var(--err)' }}>{err}</div>}
      </div>
    </Modal>
  );
}
