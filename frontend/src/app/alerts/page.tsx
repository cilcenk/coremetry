'use client';
import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { AlertRule, TimeRange } from '@/lib/types';

const METRICS = [
  { v: 'error_rate',   label: 'Error rate (%)' },
  { v: 'p99_ms',       label: 'P99 latency (ms)' },
  { v: 'p95_ms',       label: 'P95 latency (ms)' },
  { v: 'p50_ms',       label: 'P50 latency (ms)' },
  { v: 'avg_ms',       label: 'Avg latency (ms)' },
  { v: 'request_rate', label: 'Request rate (/s)' },
  { v: 'error_count',  label: 'Error count' },
];

const COMPARATORS = ['>', '>=', '<', '<='];
const SEVERITIES = ['info', 'warning', 'critical'];

const WINDOWS = [
  { v: 60,    label: '1 min' },
  { v: 300,   label: '5 min' },
  { v: 600,   label: '10 min' },
  { v: 1800,  label: '30 min' },
  { v: 3600,  label: '1 hour' },
];

export default function AlertsPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [rules, setRules] = useState<AlertRule[] | undefined>(undefined);
  const [services, setServices] = useState<string[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [draft, setDraft] = useState<Partial<AlertRule>>({
    name: '', service: '', metric: 'error_rate', comparator: '>',
    threshold: 5, windowSec: 300, severity: 'warning', enabled: true,
  });

  const refresh = () =>
    api.alertRules().then(r => setRules(r ?? [])).catch(() => setRules([]));

  useEffect(() => { refresh(); }, []);
  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => {});
  }, []);

  const save = async () => {
    if (!draft.name || !draft.metric) return;
    await api.createAlertRule(draft);
    setShowForm(false);
    setDraft({ name: '', service: '', metric: 'error_rate', comparator: '>',
      threshold: 5, windowSec: 300, severity: 'warning', enabled: true });
    refresh();
  };
  const remove = async (id: string) => {
    if (!confirm('Disable this rule?')) return;
    await api.deleteAlertRule(id);
    refresh();
  };
  const enable = async (id: string) => {
    await api.enableAlertRule(id);
    refresh();
  };

  return (
    <>
      <Topbar title="Alert rules" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Evaluator runs every minute. Built-in rules can be disabled but not deleted.
          </span>
          <button onClick={() => setShowForm(s => !s)} style={{ marginLeft: 'auto' }}>
            {showForm ? 'Cancel' : '+ New alert rule'}
          </button>
        </div>

        {showForm && (
          <div style={{
            background: 'var(--bg1)', border: '1px solid var(--border)',
            borderRadius: 8, padding: 14, marginBottom: 14,
          }}>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 10 }}>
              <Field label="Name">
                <input value={draft.name ?? ''}
                  onChange={e => setDraft({ ...draft, name: e.target.value })}
                  placeholder="e.g. High error rate on api-gateway" />
              </Field>
              <Field label="Service (empty = all)">
                <Combobox value={draft.service ?? ''} onChange={v => setDraft({ ...draft, service: v })}
                  options={services} placeholder="Service…" width="100%" />
              </Field>
              <Field label="Severity">
                <select value={draft.severity}
                  onChange={e => setDraft({ ...draft, severity: e.target.value })}>
                  {SEVERITIES.map(s => <option key={s} value={s}>{s}</option>)}
                </select>
              </Field>
              <Field label="Metric">
                <select value={draft.metric}
                  onChange={e => setDraft({ ...draft, metric: e.target.value })}>
                  {METRICS.map(m => <option key={m.v} value={m.v}>{m.label}</option>)}
                </select>
              </Field>
              <Field label="Comparator">
                <select value={draft.comparator}
                  onChange={e => setDraft({ ...draft, comparator: e.target.value })}>
                  {COMPARATORS.map(c => <option key={c} value={c}>{c}</option>)}
                </select>
              </Field>
              <Field label="Threshold">
                <input type="number" value={draft.threshold}
                  onChange={e => setDraft({ ...draft, threshold: Number(e.target.value) })} />
              </Field>
              <Field label="Window">
                <select value={draft.windowSec}
                  onChange={e => setDraft({ ...draft, windowSec: Number(e.target.value) })}>
                  {WINDOWS.map(w => <option key={w.v} value={w.v}>{w.label}</option>)}
                </select>
              </Field>
            </div>
            <div style={{ marginTop: 10 }}>
              <button onClick={save}>Save rule</button>
            </div>
          </div>
        )}

        {rules === undefined && <Spinner />}
        {rules && rules.length === 0 && <Empty icon="🔔" title="No alert rules" />}
        {rules && rules.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Service</th>
                  <th>Condition</th>
                  <th>Window</th>
                  <th>Severity</th>
                  <th>Enabled</th>
                  <th>Type</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {rules.map(r => (
                  <tr key={r.id}>
                    <td><b>{r.name}</b></td>
                    <td className="mono">{r.service || '— all —'}</td>
                    <td className="mono">{r.metric} {r.comparator} {r.threshold}</td>
                    <td>{r.windowSec / 60} min</td>
                    <td><SeverityBadge s={r.severity} /></td>
                    <td>{r.enabled
                      ? <span className="badge b-ok">ON</span>
                      : <span className="badge b-gray">OFF</span>}</td>
                    <td>{r.builtIn
                      ? <span className="badge b-info">BUILT-IN</span>
                      : <span className="badge b-gray">custom</span>}</td>
                    <td>
                      {r.enabled
                        ? <button className="sec" onClick={() => remove(r.id)}>Disable</button>
                        : <button className="sec" onClick={() => enable(r.id)}>Enable</button>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11, color: 'var(--text2)' }}>
      {label}
      {children}
    </label>
  );
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}
