'use client';
import { useEffect, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import type { SLORow, SLIType } from '@/lib/types';

export default function SLOsPage() {
  const { user } = useAuth();
  const [items, setItems] = useState<SLORow[] | null | undefined>(undefined);
  const [services, setServices] = useState<string[]>([]);
  const [showNew, setShowNew] = useState(false);
  const isAdmin = user?.role === 'admin';

  const refresh = () => {
    setItems(undefined);
    api.listSLOs().then(d => setItems(d ?? [])).catch(() => setItems(null));
  };
  useEffect(refresh, []);
  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name))).catch(() => {});
  }, []);

  const onDelete = async (id: string) => {
    if (!confirm('Delete this SLO?')) return;
    await api.deleteSLO(id);
    refresh();
  };

  return (
    <>
      <Topbar title="SLOs" />
      <div id="content">
        <div className="controls">
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Service Level Objectives — track availability and latency targets with error-budget burn down.
          </span>
          {isAdmin && (
            <button onClick={() => setShowNew(true)} style={{ marginLeft: 'auto' }}>+ New SLO</button>
          )}
        </div>

        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="◉" title="No SLOs defined">
            {isAdmin ? 'Create one to start tracking error budgets.' : 'Ask an admin to define SLOs.'}
          </Empty>
        )}
        {items && items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Service</th>
                  <th>Target</th>
                  <th>SLI ({items[0].windowDays}d)</th>
                  <th>Budget left</th>
                  <th>Burn rate</th>
                  <th>Status</th>
                  {isAdmin && <th></th>}
                </tr>
              </thead>
              <tbody>
                {items.map(o => (
                  <tr key={o.id}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{o.name}</div>
                      <div style={{ fontSize: 11, color: 'var(--text3)' }}>
                        {o.sliType === 'latency'
                          ? `latency ≤ ${o.thresholdMs}ms`
                          : 'availability'}
                        {o.operation && <> · op=<code>{o.operation}</code></>}
                      </div>
                    </td>
                    <td className="mono">{o.service}</td>
                    <td className="mono">{(o.target * 100).toFixed(2)}%</td>
                    <td className="mono">
                      {o.status ? (o.status.sli * 100).toFixed(3) + '%' : '—'}
                    </td>
                    <td className="mono">
                      {o.status ? <BudgetBar value={o.status.budgetRemaining} /> : '—'}
                    </td>
                    <td className="mono">
                      {o.status ? <BurnBadge rate={o.status.burnRate} /> : '—'}
                    </td>
                    <td>
                      {o.status?.healthy
                        ? <span className="badge b-ok">Healthy</span>
                        : <span className="badge b-err">Breached</span>}
                    </td>
                    {isAdmin && (
                      <td>
                        <button className="sec" onClick={() => onDelete(o.id)}>Delete</button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {showNew && isAdmin && (
          <NewSLOModal services={services}
            onClose={() => setShowNew(false)}
            onCreated={() => { setShowNew(false); refresh(); }} />
        )}
      </div>
    </>
  );
}

function BudgetBar({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(1, value)) * 100;
  const color = pct > 50 ? 'var(--ok)' : pct > 20 ? 'var(--warn)' : 'var(--err)';
  return (
    <div title={`${pct.toFixed(1)}% of error budget remaining`} style={{
      display: 'inline-block', width: 100, height: 10, position: 'relative',
      background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 3,
      verticalAlign: 'middle',
    }}>
      <div style={{
        position: 'absolute', left: 0, top: 0, bottom: 0, width: `${pct}%`,
        background: color, borderRadius: 2,
      }} />
    </div>
  );
}

function BurnBadge({ rate }: { rate: number }) {
  if (!isFinite(rate)) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const cls = rate > 2 ? 'b-err' : rate > 1 ? 'b-warn' : 'b-ok';
  return <span className={`badge ${cls}`}>{rate.toFixed(2)}×</span>;
}

function NewSLOModal({ services, onClose, onCreated }: {
  services: string[]; onClose: () => void; onCreated: () => void;
}) {
  const [name, setName] = useState('');
  const [service, setService] = useState('');
  const [sliType, setSliType] = useState<SLIType>('availability');
  const [target, setTarget] = useState('99.0');
  const [windowDays, setWindowDays] = useState('30');
  const [thresholdMs, setThresholdMs] = useState('500');
  const [operation, setOperation] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await api.createSLO({
        name, service, sliType,
        target: parseFloat(target) / 100,
        windowDays: parseInt(windowDays || '30'),
        thresholdMs: sliType === 'latency' ? parseFloat(thresholdMs) : 0,
        operation,
      });
      onCreated();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      try { setError(JSON.parse(msg.replace(/^HTTP \d+:\s*/, ''))?.error ?? msg); }
      catch { setError(msg); }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 420, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>New SLO</div>
        <form onSubmit={submit}>
          <Field label="Name">
            <input required autoFocus value={name}
              onChange={e => setName(e.target.value)} style={{ width: '100%' }} />
          </Field>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <Field label="Service">
              <Combobox value={service} onChange={setService}
                options={services} placeholder="…" />
            </Field>
            <Field label="Operation (optional)">
              <input value={operation}
                onChange={e => setOperation(e.target.value)} />
            </Field>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <Field label="SLI type">
              <select value={sliType}
                onChange={e => setSliType(e.target.value as SLIType)}>
                <option value="availability">Availability</option>
                <option value="latency">Latency</option>
              </select>
            </Field>
            <Field label="Window (days)">
              <input type="number" min={1} max={365} value={windowDays}
                onChange={e => setWindowDays(e.target.value)} />
            </Field>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <Field label="Target % (e.g. 99.9)">
              <input required type="number" min={0} max={100} step="0.001"
                value={target} onChange={e => setTarget(e.target.value)} />
            </Field>
            {sliType === 'latency' && (
              <Field label="Threshold (ms)">
                <input required type="number" min={0} step="0.1"
                  value={thresholdMs} onChange={e => setThresholdMs(e.target.value)} />
              </Field>
            )}
          </div>
          {error && (
            <div style={{ color: 'var(--err)', fontSize: 12, marginBottom: 8 }}>{error}</div>
          )}
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button type="button" className="sec" onClick={onClose}>Cancel</button>
            <button type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create'}</button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 12 }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}
