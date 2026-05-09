import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import {
  useAlertRules,
  useCreateAlertRule, useUpdateAlertRule,
  useDeleteAlertRule, useEnableAlertRule,
} from '@/lib/queries';
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

// Empty draft used for both "+ New" and as the reset value after save.
// Kept at module scope so the create/edit reset paths share one source.
const emptyDraft: Partial<AlertRule> = {
  name: '', service: '', metric: 'error_rate', comparator: '>',
  threshold: 5, windowSec: 300, severity: 'warning', enabled: true,
};

export default function AlertsPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [services, setServices] = useState<string[]>([]);
  const [showForm, setShowForm] = useState(false);
  const [draft, setDraft] = useState<Partial<AlertRule>>(emptyDraft);
  // Non-null while editing — `id` of the row we're editing. Drives the
  // form's "Update" vs "Save" copy and decides between PUT and POST on
  // submit.
  const [editingId, setEditingId] = useState<string | null>(null);

  // Rules query + 4 mutations. Each mutation auto-invalidates
  // the rules cache on success — no manual refresh() coordinator.
  const rulesQ = useAlertRules();
  const rules = rulesQ.isLoading ? undefined : rulesQ.data ?? [];
  const createRule = useCreateAlertRule();
  const updateRule = useUpdateAlertRule();
  const deleteRule = useDeleteAlertRule();
  const enableRule = useEnableAlertRule();

  // Service list for the picker — kept on raw fetch since
  // it's a one-shot lookup and the cache value doesn't really
  // need sharing across pages.
  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name)))
      .catch(() => {});
  }, []);

  const startEdit = (r: AlertRule) => {
    setDraft({ ...r });
    setEditingId(r.id);
    setShowForm(true);
  };
  const cancelForm = () => {
    setShowForm(false);
    setEditingId(null);
    setDraft(emptyDraft);
  };

  const save = async () => {
    if (!draft.name || !draft.metric) return;
    if (editingId) {
      await updateRule.mutateAsync({ id: editingId, patch: draft });
    } else {
      await createRule.mutateAsync(draft);
    }
    cancelForm();
  };
  const remove = async (id: string) => {
    if (!confirm('Disable this rule?')) return;
    await deleteRule.mutateAsync(id);
  };
  const enable = async (id: string) => {
    await enableRule.mutateAsync(id);
  };

  return (
    <>
      <Topbar title="Alert rules" range={range} onRangeChange={setRange} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Evaluator runs every minute. Built-in rules ship pre-configured but
            can be edited or disabled to taste.
          </span>
          <button onClick={() => showForm ? cancelForm() : setShowForm(true)}
                  style={{ marginLeft: 'auto' }}>
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
                <ServicePicker value={draft.service ?? ''} onChange={v => setDraft({ ...draft, service: v })}
                  placeholder="Service…" width="100%" />
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
            <div style={{ marginTop: 10, display: 'flex', gap: 8, alignItems: 'center' }}>
              <button onClick={save}>{editingId ? 'Update rule' : 'Save rule'}</button>
              {editingId && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  Editing <code>{editingId}</code>
                  {draft.builtIn && (
                    <span style={{ marginLeft: 6, color: 'var(--text2)' }}>
                      (built-in — edits persist; preset values aren't restored on next boot)
                    </span>
                  )}
                </span>
              )}
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
                    <td style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                      <button className="sec" onClick={() => startEdit(r)}>Edit</button>
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
