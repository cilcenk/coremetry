import { Suspense, useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { RenderedMarkdown } from '@/components/Markdown';
import { useRunbook, useUpdateRunbook, useDeleteRunbook } from '@/lib/queries';
import { tsLong } from '@/lib/utils';
import type { Runbook, RunbookStep, RunbookStepKind } from '@/lib/types';

// Runbook detail (v0.7.0) — Overview + the Steps editor (the OneUptime
// "Runbook Steps" surface: kind cards to add, drag-to-reorder, per-step
// kind-specific fields). Executions + the runner + Audit Logs tabs land
// in increment 4b. Editor+ authors; viewers see everything read-only.

const KIND_META: { kind: RunbookStepKind; icon: string; label: string; desc: string }[] = [
  { kind: 'manual',     icon: '☑',   label: 'Manual',     desc: 'Pause the run and wait for a responder to tick it off.' },
  { kind: 'query',      icon: '◷',   label: 'Query',      desc: 'Run a Coremetry query inline and capture the result.' },
  { kind: 'http',       icon: '⤴',   label: 'HTTP',       desc: 'Call an external API — PagerDuty, Slack, your own service.' },
  { kind: 'javascript', icon: '⟨⟩',  label: 'JavaScript', desc: 'Run a sandboxed JS snippet on the agent. Capture output.' },
  { kind: 'bash',       icon: '▣',   label: 'Bash',       desc: 'Run a shell command on the agent.' },
];

type Tab = 'overview' | 'steps';

export default function RunbookDetailPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp, setSp] = useSearchParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
  const id = sp.get('id') ?? '';

  const rbQ = useRunbook(id);
  const updateRb = useUpdateRunbook();
  const deleteRb = useDeleteRunbook();

  // Local editable draft, hydrated from the loaded runbook. While dirty
  // we don't re-hydrate so a background refetch can't clobber edits.
  const [draft, setDraft] = useState<Runbook | null>(null);
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (rbQ.data && !dirty) setDraft(structuredClone(rbQ.data));
  }, [rbQ.data, dirty]);

  const tab: Tab = sp.get('tab') === 'steps' ? 'steps' : 'overview';
  const setTab = (t: Tab) => setSp(prev => {
    const p = new URLSearchParams(prev);
    if (t === 'overview') p.delete('tab'); else p.set('tab', t);
    return p;
  }, { replace: true });

  if (!id) return <Empty icon="⚠" title="No runbook selected" />;
  if (rbQ.isError) return <Empty icon="⚠" title="Runbook not found" />;
  if (!draft) return <Spinner />;

  const patch = (p: Partial<Runbook>) => { setDraft({ ...draft, ...p }); setDirty(true); };
  const save = async () => { await updateRb.mutateAsync({ id, patch: draft }); setDirty(false); };
  const remove = async () => {
    if (!confirm(`Delete runbook "${draft.title}"? Historical executions are kept for audit.`)) return;
    await deleteRb.mutateAsync(id);
    navigate('/runbooks');
  };

  const addStep = (kind: RunbookStepKind) => {
    const step: RunbookStep = {
      id: 'st-' + Math.random().toString(36).slice(2, 10),
      order: draft.steps.length, kind, title: '', instructions: '',
      ...(kind === 'http' ? { method: 'GET' } : {}),
    };
    patch({ steps: [...draft.steps, step] });
  };
  const updateStep = (i: number, p: Partial<RunbookStep>) =>
    patch({ steps: draft.steps.map((s, k) => (k === i ? { ...s, ...p } : s)) });
  const removeStep = (i: number) => patch({ steps: draft.steps.filter((_, k) => k !== i) });
  const moveStep = (from: number, to: number) => {
    if (from === to || from < 0 || to < 0) return;
    const steps = [...draft.steps];
    const [m] = steps.splice(from, 1);
    steps.splice(to, 0, m);
    patch({ steps });
  };

  return (
    <>
      <Topbar title={draft.title || 'Runbook'} />
      <div id="content">
        <div className="controls" style={{ marginBottom: 8, alignItems: 'center' }}>
          <button className="sec" onClick={() => navigate('/runbooks')}>← Runbooks</button>
          <span className={`badge ${draft.enabled ? 'b-ok' : 'b-gray'}`} style={{ marginLeft: 4 }}>
            {draft.enabled ? 'ENABLED' : 'DISABLED'}
          </span>
          {draft.createdBy && (
            <span style={{ color: 'var(--text3)', fontSize: 11 }}>by {draft.createdBy}</span>
          )}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
            {canEdit && dirty && (
              <button onClick={save} disabled={updateRb.isPending}>
                {updateRb.isPending ? 'Saving…' : 'Save changes'}
              </button>
            )}
            {canEdit && (
              <button className="sec" style={{ color: 'var(--err)' }} onClick={remove}>Delete</button>
            )}
          </span>
        </div>

        <TabStrip tab={tab} onChange={setTab} stepCount={draft.steps.length} />

        {tab === 'overview' && <Overview draft={draft} canEdit={canEdit} patch={patch} />}
        {tab === 'steps' && (
          <StepsEditor
            steps={draft.steps} canEdit={canEdit}
            onAdd={addStep} onUpdate={updateStep} onRemove={removeStep} onMove={moveStep}
          />
        )}
      </div>
    </>
  );
}

function TabStrip({ tab, onChange, stepCount }: {
  tab: Tab; onChange: (t: Tab) => void; stepCount: number;
}) {
  const items: { key: Tab; label: string; hint?: string }[] = [
    { key: 'overview', label: 'Overview' },
    { key: 'steps', label: 'Steps', hint: stepCount > 0 ? String(stepCount) : undefined },
  ];
  return (
    <div style={{ display: 'flex', gap: 0, marginTop: 8, marginBottom: 14, borderBottom: '1px solid var(--border)' }}>
      {items.map(it => {
        const active = tab === it.key;
        return (
          <button key={it.key} type="button" onClick={() => onChange(it.key)}
            style={{
              all: 'unset', cursor: 'pointer', padding: '8px 18px',
              fontSize: 13, fontWeight: active ? 700 : 500,
              color: active ? 'var(--text)' : 'var(--text2)',
              borderBottom: active ? '2px solid var(--accent2)' : '2px solid transparent',
              marginBottom: -1,
            }}>
            {it.label}
            {it.hint && (
              <span style={{ marginLeft: 6, fontSize: 11, color: 'var(--text3)', fontFamily: 'ui-monospace, monospace' }}>{it.hint}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

function Overview({ draft, canEdit, patch }: {
  draft: Runbook; canEdit: boolean; patch: (p: Partial<Runbook>) => void;
}) {
  const labelsText = (draft.labels ?? []).join(', ');
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, maxWidth: 1100 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <Field label="Title">
          <input value={draft.title} disabled={!canEdit}
            onChange={e => patch({ title: e.target.value })} placeholder="e.g. API gateway 5xx spike" />
        </Field>
        <Field label="Description (markdown)">
          <textarea value={draft.description ?? ''} disabled={!canEdit}
            onChange={e => patch({ description: e.target.value })}
            placeholder="# When to run&#10;…what this procedure is for, prerequisites, escalation."
            spellCheck={false}
            style={{ minHeight: 180, fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 13, resize: 'vertical' }} />
        </Field>
        <Field label="Labels (comma-separated)">
          <input value={labelsText} disabled={!canEdit}
            onChange={e => patch({ labels: e.target.value.split(',').map(s => s.trim()).filter(Boolean) })}
            placeholder="payments, sev1, db" />
        </Field>
        {canEdit && (
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
            <input type="checkbox" checked={draft.enabled}
              onChange={e => patch({ enabled: e.target.checked })} />
            Enabled (runbook can be executed)
          </label>
        )}
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Updated {tsLong(draft.updatedAt)} · Created {tsLong(draft.createdAt)}
        </div>
      </div>
      <div>
        <div style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 600, letterSpacing: '0.5px', textTransform: 'uppercase', marginBottom: 6 }}>
          Preview
        </div>
        <div style={{
          minHeight: 180, padding: 12, fontSize: 13, lineHeight: 1.55,
          background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 8, overflowWrap: 'break-word',
        }}>
          {(draft.description ?? '').trim() === ''
            ? <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}>Description preview</span>
            : <RenderedMarkdown text={draft.description ?? ''} />}
        </div>
      </div>
    </div>
  );
}

function StepsEditor({ steps, canEdit, onAdd, onUpdate, onRemove, onMove }: {
  steps: RunbookStep[]; canEdit: boolean;
  onAdd: (k: RunbookStepKind) => void;
  onUpdate: (i: number, p: Partial<RunbookStep>) => void;
  onRemove: (i: number) => void;
  onMove: (from: number, to: number) => void;
}) {
  const [dragIdx, setDragIdx] = useState<number | null>(null);
  return (
    <div>
      <p style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12, maxWidth: 760 }}>
        Ordered list of steps to run. Manual steps pause the runbook until a responder ticks them off;
        automated steps (HTTP / JavaScript / Bash) run inline on the coremetry-agent. Drag to reorder.
      </p>

      {steps.length === 0 ? (
        <KindCards canEdit={canEdit} onAdd={onAdd} hero />
      ) : (
        <>
          {steps.map((s, i) => (
            <StepRow key={s.id} step={s} index={i} canEdit={canEdit}
              isDragging={dragIdx === i}
              onDragStart={() => setDragIdx(i)}
              onDragEnd={() => setDragIdx(null)}
              onDropOn={() => { if (dragIdx !== null) onMove(dragIdx, i); setDragIdx(null); }}
              onChange={p => onUpdate(i, p)} onRemove={() => onRemove(i)} />
          ))}
          {canEdit && (
            <div style={{ marginTop: 16 }}>
              <div style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 600, letterSpacing: '0.5px', textTransform: 'uppercase', marginBottom: 8 }}>
                Add a step
              </div>
              <KindCards canEdit={canEdit} onAdd={onAdd} />
            </div>
          )}
        </>
      )}
    </div>
  );
}

function KindCards({ canEdit, onAdd, hero }: {
  canEdit: boolean; onAdd: (k: RunbookStepKind) => void; hero?: boolean;
}) {
  if (!canEdit) {
    return hero ? <Empty icon="▤" title="No steps yet">This runbook has no steps. An editor can add them.</Empty> : null;
  }
  return (
    <div style={hero ? {
      border: '1px dashed var(--border)', borderRadius: 10, padding: 24,
    } : undefined}>
      {hero && (
        <div style={{ textAlign: 'center', marginBottom: 18 }}>
          <div style={{ fontSize: 28 }}>▤</div>
          <h3 style={{ margin: '8px 0 2px' }}>Start your runbook</h3>
          <p style={{ color: 'var(--text2)', fontSize: 13, margin: 0 }}>
            Add the first step. You can reorder and edit at any time.
          </p>
        </div>
      )}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))', gap: 10 }}>
        {KIND_META.map(k => (
          <button key={k.kind} type="button" onClick={() => onAdd(k.kind)}
            style={{
              all: 'unset', cursor: 'pointer', border: '1px solid var(--border)',
              borderRadius: 8, padding: 12, background: 'var(--bg1)',
            }}>
            <div style={{ fontSize: 18 }}>{k.icon}</div>
            <div style={{ fontWeight: 700, marginTop: 6 }}>{k.label}</div>
            <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 4, lineHeight: 1.4 }}>{k.desc}</div>
          </button>
        ))}
      </div>
    </div>
  );
}

function StepRow({ step, index, canEdit, isDragging, onDragStart, onDragEnd, onDropOn, onChange, onRemove }: {
  step: RunbookStep; index: number; canEdit: boolean; isDragging: boolean;
  onDragStart: () => void; onDragEnd: () => void; onDropOn: () => void;
  onChange: (p: Partial<RunbookStep>) => void; onRemove: () => void;
}) {
  const meta = KIND_META.find(k => k.kind === step.kind);
  return (
    <div
      draggable={canEdit}
      onDragStart={canEdit ? onDragStart : undefined}
      onDragEnd={canEdit ? onDragEnd : undefined}
      onDragOver={canEdit ? e => e.preventDefault() : undefined}
      onDrop={canEdit ? e => { e.preventDefault(); onDropOn(); } : undefined}
      style={{
        border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10,
        background: 'var(--bg1)', opacity: isDragging ? 0.5 : 1, cursor: canEdit ? 'grab' : 'default',
      }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        {canEdit && <span style={{ color: 'var(--text3)', cursor: 'grab' }} title="Drag to reorder">⠿</span>}
        <span className="badge b-info" style={{ whiteSpace: 'nowrap' }}>{meta?.icon} {meta?.label ?? step.kind}</span>
        <input value={step.title} disabled={!canEdit}
          onChange={e => onChange({ title: e.target.value })}
          placeholder="Step title" style={{ flex: 1 }} />
        <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'ui-monospace, monospace' }}>#{index + 1}</span>
        {canEdit && (
          <button className="sec" style={{ color: 'var(--err)' }} onClick={onRemove} title="Remove step">✕</button>
        )}
      </div>

      <textarea value={step.instructions ?? ''} disabled={!canEdit}
        onChange={e => onChange({ instructions: e.target.value })}
        placeholder="Instructions (markdown) — what the responder should do / check."
        spellCheck={false}
        style={{ width: '100%', marginTop: 8, minHeight: 64, fontSize: 13, resize: 'vertical' }} />

      {step.kind === 'manual' && (
        <Field label="Expected outcome (optional)">
          <input value={step.expected ?? ''} disabled={!canEdit}
            onChange={e => onChange({ expected: e.target.value })} placeholder="What 'done' looks like" />
        </Field>
      )}
      {step.kind === 'query' && (
        <Field label="Query (ClickHouse SQL / Explore DSL)">
          <textarea value={step.query ?? ''} disabled={!canEdit}
            onChange={e => onChange({ query: e.target.value })}
            placeholder="error_rate by service where service = 'api-gateway'"
            spellCheck={false}
            style={{ minHeight: 56, fontFamily: 'ui-monospace, monospace', fontSize: 12, resize: 'vertical' }} />
        </Field>
      )}
      {step.kind === 'http' && (
        <div style={{ display: 'grid', gridTemplateColumns: '110px 1fr', gap: 8, marginTop: 6 }}>
          <Field label="Method">
            <select value={step.method ?? 'GET'} disabled={!canEdit}
              onChange={e => onChange({ method: e.target.value })}>
              {['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].map(m => <option key={m} value={m}>{m}</option>)}
            </select>
          </Field>
          <Field label="URL">
            <input value={step.url ?? ''} disabled={!canEdit}
              onChange={e => onChange({ url: e.target.value })} placeholder="https://events.pagerduty.com/v2/enqueue" />
          </Field>
          <Field label="Headers (one per line: Key: Value)">
            <textarea value={headersToText(step.headers)} disabled={!canEdit}
              onChange={e => onChange({ headers: textToHeaders(e.target.value) })}
              placeholder={'Authorization: Token abc\nContent-Type: application/json'}
              spellCheck={false}
              style={{ minHeight: 48, fontFamily: 'ui-monospace, monospace', fontSize: 12, resize: 'vertical' }} />
          </Field>
          <Field label="Body">
            <textarea value={step.body ?? ''} disabled={!canEdit}
              onChange={e => onChange({ body: e.target.value })} spellCheck={false}
              style={{ minHeight: 48, fontFamily: 'ui-monospace, monospace', fontSize: 12, resize: 'vertical' }} />
          </Field>
          <Field label="Timeout (ms)">
            <input type="number" value={step.timeoutMs ?? ''} disabled={!canEdit}
              onChange={e => onChange({ timeoutMs: e.target.value ? Number(e.target.value) : undefined })} placeholder="10000" />
          </Field>
        </div>
      )}
      {step.kind === 'javascript' && (
        <Field label="Script (JavaScript — runs sandboxed on the agent)">
          <textarea value={step.script ?? ''} disabled={!canEdit}
            onChange={e => onChange({ script: e.target.value })}
            placeholder="// return a value or call out via fetch-like API\nreturn 1 + 1;"
            spellCheck={false}
            style={{ minHeight: 72, fontFamily: 'ui-monospace, monospace', fontSize: 12, resize: 'vertical' }} />
        </Field>
      )}
      {step.kind === 'bash' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 110px', gap: 8, marginTop: 6 }}>
          <Field label="Command (runs on the agent)">
            <input value={step.command ?? ''} disabled={!canEdit}
              onChange={e => onChange({ command: e.target.value })}
              placeholder="kubectl rollout restart deploy/api -n prod" style={{ fontFamily: 'ui-monospace, monospace' }} />
          </Field>
          <Field label="Timeout (ms)">
            <input type="number" value={step.timeoutMs ?? ''} disabled={!canEdit}
              onChange={e => onChange({ timeoutMs: e.target.value ? Number(e.target.value) : undefined })} placeholder="30000" />
          </Field>
        </div>
      )}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 11, color: 'var(--text2)', marginTop: 6 }}>
      {label}
      {children}
    </label>
  );
}

function headersToText(h?: Record<string, string>): string {
  if (!h) return '';
  return Object.entries(h).map(([k, v]) => `${k}: ${v}`).join('\n');
}
function textToHeaders(t: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of t.split('\n')) {
    const idx = line.indexOf(':');
    if (idx > 0) {
      const k = line.slice(0, idx).trim();
      if (k) out[k] = line.slice(idx + 1).trim();
    }
  }
  return out;
}
