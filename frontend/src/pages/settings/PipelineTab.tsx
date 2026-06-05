import { useEffect, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Modal, Button, Stack } from '@/components/ui';
import { api, type PipelineRule } from '@/lib/api';

// ── Pipeline tab (v0.5.263) ─────────────────────────────────────────────────
//
// Ingest-time drop rules — span-only MVP. Operator picks "service.name =
// frontend" and any span matching that predicate gets dropped before the
// sampler / consumer sees it. Drop counter is exposed on /admin/stats so
// the effect is observable without log-grepping.
export function PipelineTab() {
  const [rules, setRules] = useState<PipelineRule[] | null | undefined>(undefined);
  const [editing, setEditing] = useState<PipelineRule | null>(null);
  const [creating, setCreating] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    setRules(undefined);
    api.listPipelineRules().then(r => setRules(r.rules ?? [])).catch(() => setRules(null));
  };
  useEffect(load, []);

  const toggle = async (r: PipelineRule) => {
    try {
      await api.upsertPipelineRule({ ...r, enabled: !r.enabled });
      setMsg({ kind: 'ok', text: `${r.name}: ${!r.enabled ? 'enabled' : 'disabled'}` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
  };

  const remove = async (r: PipelineRule) => {
    if (!confirm(`Delete pipeline rule "${r.name}"? Spans matching this rule will no longer be dropped.`)) return;
    try {
      await api.deletePipelineRule(r.id);
      setMsg({ kind: 'ok', text: `Deleted "${r.name}"` });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : String(e) });
    }
  };

  if (rules === undefined) return <Spinner />;
  if (rules === null) return <Empty icon="!" title="Failed to load pipeline rules" />;

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 12 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', flex: 1 }}>
          Ingest-time rules evaluated <b>before</b> the sampler. A "drop"
          rule that matches removes the span entirely — no CH write, no
          tail-sampler bookkeeping. Use for noisy health-check spans,
          internal-only kinds you never want to inspect, or services
          you've decided to drop wholesale for cost.
        </span>
        <Button onClick={() => setCreating(true)}>+ New rule</Button>
      </div>

      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4, fontSize: 13,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'rgba(34,197,94,0.08)' : 'rgba(220,38,38,0.08)',
          border: `1px solid ${msg.kind === 'ok' ? 'rgba(34,197,94,0.3)' : 'rgba(220,38,38,0.3)'}`,
        }}>{msg.text}</div>
      )}

      {rules.length === 0 ? (
        <Empty icon="⇉" title="No pipeline rules yet">
          Create one to drop noisy span traffic at ingest.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Signal</th>
                <th>Predicate</th>
                <th>Enabled</th>
                <th style={{ textAlign: 'right' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {rules.map(r => (
                <tr key={r.id}>
                  <td style={{ fontWeight: 600 }}>{r.name}</td>
                  <td>
                    <span className={r.kind === 'drop' ? 'badge b-err' : 'badge b-info'}>
                      {r.kind.toUpperCase()}
                    </span>
                  </td>
                  <td>
                    <code style={{ fontSize: 11 }}>{r.signal}</code>
                  </td>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                    {r.when.key} <b>{r.when.op}</b>{' '}
                    <span style={{ color: 'var(--text2)' }}>"{r.when.value}"</span>
                    {r.kind === 'enrich' && r.setAttributes && Object.entries(r.setAttributes).map(([k, v]) => (
                      <span key={k} style={{ marginLeft: 8, color: 'var(--accent2)' }}>
                        → {k}=<b>"{v}"</b>
                      </span>
                    ))}
                    {r.kind === 'sample' && r.rate != null && (
                      <span style={{ marginLeft: 8, color: 'var(--accent2)' }}>
                        keep <b>{(r.rate * 100).toFixed(1)}%</b>
                      </span>
                    )}
                  </td>
                  <td>
                    <input type="checkbox" checked={r.enabled} onChange={() => toggle(r)} />
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="secondary" size="sm" onClick={() => setEditing(r)} style={{ marginRight: 6 }}>
                      Edit
                    </Button>
                    <Button variant="secondary" size="sm" onClick={() => remove(r)}>
                      Delete
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(creating || editing) && (
        <PipelineRuleModal
          existing={editing}
          onClose={() => { setCreating(false); setEditing(null); }}
          onSaved={() => { setCreating(false); setEditing(null); load(); }}
        />
      )}
    </div>
  );
}

function PipelineRuleModal({ existing, onClose, onSaved }: {
  existing: PipelineRule | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name,    setName]    = useState(existing?.name ?? '');
  const [kind,    setKind]    = useState<PipelineRule['kind']>(existing?.kind ?? 'drop');
  const [signal,  setSignal]  = useState<PipelineRule['signal']>(existing?.signal ?? 'spans');
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [whenKey, setWhenKey] = useState(existing?.when.key ?? 'service.name');
  const [whenOp,  setWhenOp]  = useState<PipelineRule['when']['op']>(existing?.when.op ?? '=');
  const [whenVal, setWhenVal] = useState(existing?.when.value ?? '');
  // v0.5.270 — enrich + sample fields. Enrich uses a single
  // key/value pair for the MVP (multi-attr could come later
  // via a chip list — start narrow).
  const [enrichKey, setEnrichKey] = useState<string>(() => {
    const m = existing?.setAttributes ?? {};
    return Object.keys(m)[0] ?? '';
  });
  const [enrichVal, setEnrichVal] = useState<string>(() => {
    const m = existing?.setAttributes ?? {};
    const k = Object.keys(m)[0];
    return k ? m[k] : '';
  });
  const [rate, setRate] = useState<number>(existing?.rate ?? 0.1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const body: PipelineRule = {
        id: existing?.id ?? '',
        name: name.trim(),
        kind, signal, enabled,
        when: { key: whenKey.trim(), op: whenOp, value: whenVal.trim() },
      };
      if (kind === 'enrich') {
        body.setAttributes = enrichKey.trim()
          ? { [enrichKey.trim()]: enrichVal.trim() }
          : {};
      }
      if (kind === 'sample') {
        body.rate = rate;
      }
      await api.upsertPipelineRule(body);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={existing ? `Edit rule — ${existing.name}` : 'New pipeline rule'}
      size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="pipeline-form" loading={busy}>Save</Button>
        </>
      }>
      <form id="pipeline-form" onSubmit={submit}>
        <Stack gap={3}>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Rule name</label>
            <input value={name} onChange={e => setName(e.target.value)} required
              placeholder="e.g. drop frontend health-checks"
              style={{ width: '100%' }} />
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Action</label>
              <select value={kind} onChange={e => setKind(e.target.value as PipelineRule['kind'])}
                style={{ width: '100%' }}>
                <option value="drop">Drop — discard the matching signal</option>
                <option value="enrich">Enrich — set a resource attribute</option>
                <option value="sample">Sample — keep at probability</option>
              </select>
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>Signal</label>
              <select value={signal} onChange={e => setSignal(e.target.value as PipelineRule['signal'])}
                style={{ width: '100%' }}>
                <option value="spans">spans</option>
                <option value="logs" disabled>logs (coming soon)</option>
                <option value="metrics" disabled>metrics (coming soon)</option>
              </select>
            </div>
          </div>
          <div>
            <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
              When (predicate)
            </label>
            <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr 2fr', gap: 8 }}>
              <input value={whenKey} onChange={e => setWhenKey(e.target.value)} required
                placeholder="service.name | name | kind | attr.X | resource.X"
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              <select value={whenOp} onChange={e => setWhenOp(e.target.value as PipelineRule['when']['op'])}
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                <option value="=">=</option>
                <option value="!=">!=</option>
                <option value="contains">contains</option>
                <option value="startsWith">startsWith</option>
                <option value="endsWith">endsWith</option>
              </select>
              <input value={whenVal} onChange={e => setWhenVal(e.target.value)} required
                placeholder="value"
                style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
            </div>
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
              Well-known span fields branch directly: <code>service.name</code>,
              {' '}<code>name</code>, <code>kind</code>, <code>status_code</code>.
              Custom attributes via <code>attr.foo</code> / <code>resource.foo</code> prefix.
            </div>
          </div>

          {/* v0.5.270 — enrich-only fields */}
          {kind === 'enrich' && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Set resource attribute
              </label>
              <div style={{ display: 'grid', gridTemplateColumns: '2fr 3fr', gap: 8 }}>
                <input value={enrichKey} onChange={e => setEnrichKey(e.target.value)}
                  placeholder="e.g. team, region, cluster"
                  style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
                <input value={enrichVal} onChange={e => setEnrichVal(e.target.value)}
                  placeholder="value"
                  style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }} />
              </div>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Sets a resource attribute on every matching span. Existing keys are
                overridden. Multi-attribute support coming later — start with one.
              </div>
            </div>
          )}

          {/* v0.5.270 — sample-only fields */}
          {kind === 'sample' && (
            <div>
              <label style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
                Keep rate ({(rate * 100).toFixed(1)}%)
              </label>
              <input type="range" min={0} max={1} step={0.01}
                value={rate} onChange={e => setRate(Number(e.target.value))}
                style={{ width: '100%' }} />
              <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                Probability of keeping each matching span. 1.0 = no-op; 0.0 = use a
                drop rule instead. Runs BEFORE the global head sampler — that may
                still further sample the kept spans.
              </div>
            </div>
          )}

          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
            <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
            Enabled
          </label>
          {error && (
            <div style={{
              color: 'var(--err)', fontSize: 12,
              padding: '4px 8px', background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
