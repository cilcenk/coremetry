import { useEffect, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { ServiceContract, ContractViolation } from '@/lib/types';

// /admin/contracts — operator-curated service dependency
// contracts (v0.5.191). Two layers on one page:
//   1. Active violations — what's currently broken (top of
//      page, scannable). Read-only.
//   2. Contract definitions — full CRUD for the rule set.
// Refresh button + 30s server cache keeps the violations list
// close to live without hammering the topology aggregator.
export default function AdminContractsPage() {
  const { user } = useAuth();
  const [contracts, setContracts] = useState<ServiceContract[] | null | undefined>(undefined);
  const [violations, setViolations] = useState<ContractViolation[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<ServiceContract | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [busy, setBusy] = useState(false);

  const reload = () => {
    api.listContracts().then(setContracts).catch(() => setContracts(null));
    api.contractViolations(30).then(setViolations).catch(() => setViolations(null));
  };
  useEffect(() => { reload(); }, []);

  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Service contracts" />
        <div id="content">
          <Empty icon="◇" title="Admin access required">
            Service contracts are an admin-only surface.
          </Empty>
        </div>
      </>
    );
  }

  const onDelete = async (id: string) => {
    if (!confirm('Delete this contract?')) return;
    await api.deleteContract(id);
    reload();
  };
  const onToggle = async (c: ServiceContract) => {
    setBusy(true);
    try {
      await api.upsertContract({ ...c, enabled: !c.enabled });
      reload();
    } finally { setBusy(false); }
  };

  return (
    <>
      <Topbar title="Service contracts" />
      <div id="content">
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 14 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>
            Architectural assertions — "A must call B" or "A must NOT call B" — checked
            against the live topology every 30s.
          </span>
          <span style={{ flex: 1 }} />
          <button className="sec" onClick={reload}>↻ Refresh</button>
          <button onClick={() => { setEditing(null); setShowForm(true); }}>
            + New contract
          </button>
        </div>

        {/* Active violations — what's broken right now */}
        <div style={{
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 14, marginBottom: 18,
        }}>
          <div style={{
            display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 10,
          }}>
            <span style={{ fontSize: 13, fontWeight: 700 }}>⚠ Active violations</span>
            {violations && (
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                {violations.length} in last 30 min
              </span>
            )}
          </div>
          {violations === undefined && <Spinner />}
          {violations === null && (
            <div style={{ fontSize: 12, color: 'var(--err)' }}>
              Failed to evaluate contracts — check logs.
            </div>
          )}
          {violations && violations.length === 0 && (
            <div style={{ fontSize: 12, color: 'var(--ok)' }}>
              ✓ All enabled contracts hold over the last 30 minutes.
            </div>
          )}
          {violations && violations.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {violations.map(v => <ViolationRow key={v.contract.id} v={v} />)}
            </div>
          )}
        </div>

        {/* Contract definitions */}
        {showForm && (
          <ContractForm initial={editing ?? undefined}
            onSaved={() => { setShowForm(false); setEditing(null); reload(); }}
            onCancel={() => { setShowForm(false); setEditing(null); }} />
        )}
        {contracts === undefined && <Spinner />}
        {contracts === null && <Empty icon="✗" title="Failed to load contracts" />}
        {contracts && contracts.length === 0 && (
          <Empty icon="◇" title="No contracts defined yet">
            Click "+ New contract" to assert a dependency rule.
          </Empty>
        )}
        {contracts && contracts.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead><tr>
                <th>Name</th>
                <th>Rule</th>
                <th>Severity</th>
                <th>Status</th>
                <th></th>
              </tr></thead>
              <tbody>
                {contracts.map(c => (
                  <tr key={c.id}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{c.name || '—'}</div>
                      {c.description && (
                        <div style={{ fontSize: 11, color: 'var(--text3)' }}>{c.description}</div>
                      )}
                    </td>
                    <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                      <code>{c.service}</code>
                      {' '}
                      <span style={{ color: c.ruleType === 'forbidden' ? 'var(--err)' : 'var(--accent2)' }}>
                        {c.ruleType === 'forbidden' ? 'must NOT call' : 'must call'}
                      </span>
                      {' '}
                      <code>{c.targetService}</code>
                    </td>
                    <td>
                      <span className={`badge ${c.severity === 'critical' ? 'b-err' : c.severity === 'warning' ? 'b-warn' : 'b-info'}`}>
                        {c.severity}
                      </span>
                    </td>
                    <td>
                      {c.enabled
                        ? <span className="badge b-ok">enabled</span>
                        : <span className="badge b-gray">disabled</span>}
                    </td>
                    <td style={{ display: 'flex', gap: 6, justifyContent: 'flex-end' }}>
                      <button className="sec"
                        onClick={() => { setEditing(c); setShowForm(true); }}>Edit</button>
                      <button className="sec" disabled={busy}
                        onClick={() => onToggle(c)}>
                        {c.enabled ? 'Disable' : 'Enable'}
                      </button>
                      <button className="sec" onClick={() => onDelete(c.id)}
                        style={{ color: 'var(--err)' }}>Delete</button>
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

function ViolationRow({ v }: { v: ContractViolation }) {
  const c = v.contract;
  const sevCls = c.severity === 'critical' ? 'b-err' : c.severity === 'warning' ? 'b-warn' : 'b-info';
  const summary = c.ruleType === 'must-call'
    ? `${c.service} did NOT call ${c.targetService}`
    : `${c.service} called ${c.targetService} (forbidden, ${v.edgeCalls} times)`;
  return (
    <div style={{
      padding: 10, borderRadius: 4,
      background: 'var(--bg)', border: '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <span className={`badge ${sevCls}`}>{c.severity}</span>
        <span style={{ fontWeight: 600, fontSize: 13 }}>{c.name || '(unnamed contract)'}</span>
        <span style={{ flex: 1 }} />
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          detected {tsLong(v.detected)}
        </span>
      </div>
      <div style={{ marginTop: 4, fontSize: 12,
        fontFamily: 'ui-monospace, monospace', color: 'var(--text2)' }}>
        {summary}
      </div>
      {c.description && (
        <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text3)' }}>
          {c.description}
        </div>
      )}
    </div>
  );
}

function ContractForm({ initial, onSaved, onCancel }: {
  initial?: ServiceContract;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? '');
  const [service, setService] = useState(initial?.service ?? '');
  const [target, setTarget] = useState(initial?.targetService ?? '');
  const [ruleType, setRuleType] = useState<'must-call' | 'forbidden'>(initial?.ruleType ?? 'must-call');
  const [severity, setSeverity] = useState<'info' | 'warning' | 'critical'>(initial?.severity ?? 'warning');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      await api.upsertContract({
        id: initial?.id,
        name: name.trim(),
        service: service.trim(),
        targetService: target.trim(),
        ruleType, severity, description: description.trim(), enabled,
      });
      onSaved();
    } catch (e2) {
      setErr(e2 instanceof Error ? e2.message : String(e2));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 14, marginBottom: 18,
      display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10,
    }}>
      <div style={{ gridColumn: '1 / -1', fontWeight: 700, fontSize: 13 }}>
        {initial ? `Edit "${initial.name || initial.id}"` : 'New contract'}
      </div>
      <label>
        <div style={{ fontSize: 11, color: 'var(--text2)' }}>Name</div>
        <input value={name} onChange={e => setName(e.target.value)}
          placeholder="e.g. auth → audit-log compliance trail"
          style={{ width: '100%' }} />
      </label>
      <label>
        <div style={{ fontSize: 11, color: 'var(--text2)' }}>Rule type</div>
        <select value={ruleType} onChange={e => setRuleType(e.target.value as 'must-call' | 'forbidden')}>
          <option value="must-call">must call</option>
          <option value="forbidden">must NOT call (forbidden)</option>
        </select>
      </label>
      <label>
        <div style={{ fontSize: 11, color: 'var(--text2)' }}>Source service</div>
        <ServicePicker value={service} onChange={setService} placeholder="auth-service" />
      </label>
      <label>
        <div style={{ fontSize: 11, color: 'var(--text2)' }}>Target service</div>
        <ServicePicker value={target} onChange={setTarget} placeholder="audit-log" />
      </label>
      <label>
        <div style={{ fontSize: 11, color: 'var(--text2)' }}>Severity</div>
        <select value={severity} onChange={e => setSeverity(e.target.value as 'info' | 'warning' | 'critical')}>
          <option value="info">info</option>
          <option value="warning">warning</option>
          <option value="critical">critical</option>
        </select>
      </label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
        <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
        Enabled
      </label>
      <label style={{ gridColumn: '1 / -1' }}>
        <div style={{ fontSize: 11, color: 'var(--text2)' }}>Description (optional)</div>
        <textarea value={description} onChange={e => setDescription(e.target.value)}
          rows={2}
          placeholder="Why this contract exists — operators reading the violation row see this."
          style={{ width: '100%' }} />
      </label>
      {err && (
        <div style={{ gridColumn: '1 / -1', fontSize: 12, color: 'var(--err)' }}>{err}</div>
      )}
      <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 6 }}>
        <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
        <button type="button" className="sec" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}
