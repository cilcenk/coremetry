import { Suspense, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { RenderedMarkdown } from '@/components/Markdown';
import { useRunbookExecution, useRunbookStepAction, useCancelRunbookExecution } from '@/lib/queries';
import { tsLong } from '@/lib/utils';

// Runbook execution runner (v0.7.0) — step through a live run. Manual steps
// are ticked here (Done / Skip / Fail + note); automated steps
// (http/javascript/bash) run on the coremetry-agent (a later increment) and
// show a pending-agent affordance with a Skip escape hatch until then. Once
// the run is terminal this same view is the frozen, read-only audit trail.

const KIND_ICON: Record<string, string> = { manual: '☑', query: '◷', http: '⤴', javascript: '⟨⟩', bash: '▣' };
const STEP_BADGE: Record<string, string> = {
  pending: 'b-gray', running: 'b-info', waiting_for_user: 'b-warn',
  completed: 'b-ok', skipped: 'b-gray', failed: 'b-err',
};
const EXEC_BADGE: Record<string, string> = {
  running: 'b-info', waiting_for_user: 'b-warn', completed: 'b-ok', failed: 'b-err', cancelled: 'b-gray',
};
const TERMINAL = ['completed', 'failed', 'cancelled'];
const STEP_TERMINAL = ['completed', 'skipped', 'failed'];
const AGENT_KINDS = new Set(['http', 'javascript', 'bash']);

export default function RunbookExecutionPage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp] = useSearchParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
  const execId = sp.get('id') ?? '';

  const execQ = useRunbookExecution(execId);
  const exec = execQ.data;
  const stepAction = useRunbookStepAction();
  const cancelExec = useCancelRunbookExecution();
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [err, setErr] = useState<string | null>(null);

  if (!execId || execQ.isError) {
    return (
      <>
        <Topbar title="Execution" />
        <div id="content">
          <div className="controls" style={{ marginBottom: 12 }}>
            <button className="sec" onClick={() => navigate('/runbooks')}>← Runbooks</button>
          </div>
          {!execId
            ? <Empty icon="⚠" title="No execution selected">Open a run from a runbook's Executions tab.</Empty>
            : <Empty icon="⚠" title="Execution not found">This execution may have been removed, or the link is stale. Pick a run from the Runbooks list.</Empty>}
        </div>
      </>
    );
  }
  if (!exec) return <Spinner />;

  const terminal = TERMINAL.includes(exec.status);
  const currentIdx = exec.stepStates.findIndex(s => !STEP_TERMINAL.includes(s.status));
  const done = exec.stepStates.filter(s => STEP_TERMINAL.includes(s.status)).length;

  const act = (stepId: string, action: 'complete' | 'skip' | 'fail') => {
    setErr(null);
    stepAction.mutateAsync({ execId, stepId, action, note: notes[stepId] || undefined })
      // a 409 means the run finished out from under us (concurrent responder /
      // poll race) — surface it and refetch so the now-terminal run renders.
      .catch(e => { setErr(e instanceof Error ? e.message : String(e)); execQ.refetch(); });
  };

  return (
    <>
      <Topbar title={exec.titleSnapshot || 'Execution'} />
      <div id="content">
        {err && (
          <div style={{ background: 'var(--bg1)', border: '1px solid var(--err)', color: 'var(--err)', borderRadius: 6, padding: '8px 12px', marginBottom: 10, fontSize: 13 }}>
            {err} <button className="sec" style={{ marginLeft: 8 }} onClick={() => setErr(null)}>dismiss</button>
          </div>
        )}
        <div className="controls" style={{ marginBottom: 12, alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
          <button className="sec" onClick={() => navigate(`/runbook?id=${encodeURIComponent(exec.runbookId)}`)}>← Runbook</button>
          <span className={`badge ${EXEC_BADGE[exec.status] ?? 'b-gray'}`}>{exec.status.replace(/_/g, ' ')}</span>
          <span style={{ color: 'var(--text3)', fontSize: 11 }}>
            {done}/{exec.stepStates.length} steps
            {exec.startedBy && <> · by {exec.startedBy}</>}
            {' '}· started {tsLong(exec.startedAt)}
            {exec.completedAt ? <> · ended {tsLong(exec.completedAt)}</> : null}
          </span>
          {exec.problemId && (
            <a href={`/problems`} onClick={e => { e.preventDefault(); navigate('/problems'); }}
              style={{ fontSize: 11, color: 'var(--accent2)' }}>problem {exec.problemId}</a>
          )}
          {canEdit && !terminal && (
            <button className="sec" style={{ marginLeft: 'auto', color: 'var(--err)' }}
              onClick={() => { if (confirm('Cancel this execution?')) cancelExec.mutateAsync(execId).catch(e => setErr(e instanceof Error ? e.message : String(e))); }}>
              Cancel run
            </button>
          )}
        </div>

        {exec.stepStates.length === 0 && <Empty icon="▤" title="This runbook had no steps" />}

        {exec.stepStates.map((s, i) => {
          const isCurrent = i === currentIdx && !terminal;
          const isAgent = AGENT_KINDS.has(s.kind);
          return (
            <div key={s.stepId} style={{
              border: isCurrent ? '1px solid var(--accent2)' : '1px solid var(--border)',
              borderRadius: 8, padding: 12, marginBottom: 10, background: 'var(--bg1)',
            }}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <span style={{ color: 'var(--text3)', fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>#{i + 1}</span>
                <span className="badge b-info" style={{ whiteSpace: 'nowrap' }}>{KIND_ICON[s.kind] ?? ''} {s.kind}</span>
                <b style={{ flex: 1 }}>{s.title || '(untitled step)'}</b>
                <span className={`badge ${STEP_BADGE[s.status] ?? 'b-gray'}`}>{s.status.replace(/_/g, ' ')}</span>
              </div>

              {s.instructions && (
                <div style={{ marginTop: 8, fontSize: 13, lineHeight: 1.5 }}>
                  <RenderedMarkdown text={s.instructions} />
                </div>
              )}

              {s.output && (
                <pre style={{
                  marginTop: 8, padding: 8, background: 'var(--bg)', borderRadius: 4,
                  fontSize: 12, overflowX: 'auto', fontFamily: 'ui-monospace, monospace',
                }}>{s.output}</pre>
              )}
              {s.error && (
                <pre style={{
                  marginTop: 8, padding: 8, background: 'var(--bg)', borderRadius: 4,
                  fontSize: 12, overflowX: 'auto', color: 'var(--err)', fontFamily: 'ui-monospace, monospace',
                }}>{s.error}</pre>
              )}

              {(s.by || s.endedAt || s.note) && (
                <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
                  {s.by && <>by {s.by} </>}
                  {s.endedAt ? <>· {tsLong(s.endedAt)} </> : null}
                  {s.note ? <>· “{s.note}”</> : null}
                </div>
              )}

              {isCurrent && canEdit && (
                isAgent ? (
                  <div style={{ marginTop: 10, fontSize: 12, color: 'var(--text2)' }}>
                    <span className="badge b-warn">agent</span>{' '}
                    This {s.kind} step runs on the coremetry-agent — it will pick it up shortly (status updates on the next poll). You can also skip it.
                    <div style={{ marginTop: 6 }}>
                      <button className="sec" onClick={() => act(s.stepId, 'skip')} disabled={stepAction.isPending}>Skip</button>
                    </div>
                  </div>
                ) : (
                  <div style={{ marginTop: 10, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                    <input placeholder="note (optional)" aria-label="Step note (optional)" value={notes[s.stepId] ?? ''}
                      onChange={e => setNotes({ ...notes, [s.stepId]: e.target.value })}
                      style={{ flex: '1 1 220px' }} />
                    <button onClick={() => act(s.stepId, 'complete')} disabled={stepAction.isPending}>✓ Done</button>
                    <button className="sec" onClick={() => act(s.stepId, 'skip')} disabled={stepAction.isPending}>Skip</button>
                    <button className="sec" style={{ color: 'var(--err)' }}
                      onClick={() => act(s.stepId, 'fail')} disabled={stepAction.isPending}>Fail</button>
                  </div>
                )
              )}
            </div>
          );
        })}
      </div>
    </>
  );
}
