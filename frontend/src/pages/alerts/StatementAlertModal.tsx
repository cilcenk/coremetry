// StatementAlertModal.tsx — v0.10.331: Statement sayfasından "Alarm oluştur".
// Hedef sayfadan gelir (hash + db); kalan alanlar kural sözleşmesi: metrik
// (p95 varsayılan / max seçilebilir), eşik ms, pencere, yürütme tabanı,
// süreklilik, şiddet. Kaydet → POST /api/alert-rules (mevcut hook).
import { useState } from 'react';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Field } from '@/pages/settings/shared';
import { useCreateAlertRule } from '@/lib/queries/alerts';
import type { RuleTarget } from '@/lib/types';
import { DB_STMT_METRICS, SEVERITIES, WINDOWS } from './constants';

export function defaultStatementRuleName(sample: string): string {
  const one = sample.replace(/\s+/g, ' ').trim();
  return `Slow SQL: ${one.length > 60 ? one.slice(0, 60) + '…' : one || 'statement'}`;
}

export function StatementAlertModal({ open, onClose, target }: { open: boolean; onClose: () => void; target: RuleTarget }) {
  const create = useCreateAlertRule();
  const [name, setName] = useState(() => defaultStatementRuleName(target.sample || ''));
  const [metric, setMetric] = useState('db_stmt_p95_ms');
  const [threshold, setThreshold] = useState(1000);
  const [windowSec, setWindowSec] = useState(600);
  const [minSamples, setMinSamples] = useState(20);
  const [forSec, setForSec] = useState(600);
  const [severity, setSeverity] = useState('warning');
  const [err, setErr] = useState<string | null>(null);
  const save = async () => {
    setErr(null);
    try {
      await create.mutateAsync({ name, service: '', metric, comparator: '>', threshold, windowSec, severity, enabled: true, minSamples, forSec, cooldownSec: 900, target });
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'save failed');
    }
  };
  return (
    <Modal open={open} onClose={onClose} title="Bu ifade için alarm kuralı"
      footer={<>
        <Button variant="secondary" onClick={onClose}>Vazgeç</Button>
        <Button variant="primary" onClick={save} loading={create.isPending} disabled={!name || threshold <= 0}>Kaydet</Button>
      </>}>
      <div style={{ display: 'grid', gap: 10 }}>
        <code className="mono cell-ellipsis" style={{ fontSize: 11, maxWidth: '100%' }} title={target.sample}>{target.sample || `#${target.stmtHash}`}</code>
        <Field label="Kural adı"><input value={name} onChange={e => setName(e.target.value)} style={{ width: '100%' }} /></Field>
        <div className="grid-3" style={{ display: 'grid', gap: 10 }}>
          <Field label="Ölçü">
            <select value={metric} onChange={e => setMetric(e.target.value)}>
              {DB_STMT_METRICS.map(m => <option key={m.v} value={m.v}>{m.label}</option>)}
            </select>
          </Field>
          <Field label="Eşik (ms, >)"><input type="number" min={1} step={50} value={threshold} onChange={e => setThreshold(Number(e.target.value))} /></Field>
          <Field label="Pencere">
            <select value={windowSec} onChange={e => setWindowSec(Number(e.target.value))}>
              {WINDOWS.filter(w => w.v >= 300).map(w => <option key={w.v} value={w.v}>{w.label}</option>)}
            </select>
          </Field>
          <Field label="Yürütme tabanı (pencerede)"><input type="number" min={0} value={minSamples} onChange={e => setMinSamples(Number(e.target.value))} /></Field>
          <Field label="Süreklilik (sn)"><input type="number" min={0} step={60} value={forSec} onChange={e => setForSec(Number(e.target.value))} /></Field>
          <Field label="Şiddet">
            <select value={severity} onChange={e => setSeverity(e.target.value)}>
              {SEVERITIES.map(sv => <option key={sv} value={sv}>{sv}</option>)}
            </select>
          </Field>
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>Kapsam: ifadeyi çalıştıran tüm servisler. Problem veritabanı öznesiyle açılır; DB sahibi ve SRE takımına mevcut yönlendirmeyle mail gider.</div>
        {err && <div style={{ fontSize: 11, color: 'var(--err)' }}>{err}</div>}
      </div>
    </Modal>
  );
}
