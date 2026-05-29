import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAuth } from '@/components/AuthProvider';
import { Spinner } from '@/components/Spinner';
import { tsLong } from '@/lib/utils';
import {
  useRunbooks,
  useRunbookExecutions,
  useExecuteRunbook,
} from '@/lib/queries/runbooks';
import type { Runbook } from '@/lib/types';

// ProblemRunbookPanel (v0.7.10) — the Problem→Runbook bridge in the triage
// drawer. An oncall looking at a fired Problem can run an operational runbook
// against it in one click (the execution is tagged with this problemId so the
// run, the agent's step results, and the audit trail all link back). Below the
// picker we list the runs already attached to this problem.
//
// Execute is a state mutation (creates an execution + the agent runs steps) →
// editor+ only, mirroring the backend RequireAnyRole(editorRoles) gate. Viewers
// still SEE the linked runs (read-only), per invariant #7. The runbook set is
// small + operator-authored, so a client-side filter over the loaded list is
// fine here — this is NOT the 10k-services/ops picker case the hard constraint
// targets (it mirrors how /runbooks itself loads them).
const EXEC_BADGE: Record<string, string> = {
  running: 'b-info', waiting_for_user: 'b-warn',
  completed: 'b-ok', failed: 'b-err', cancelled: 'b-gray',
};

export function ProblemRunbookPanel({ problemId }: { problemId: string }) {
  const { user } = useAuth();
  const canRun = user?.role === 'admin' || user?.role === 'editor';
  const navigate = useNavigate();

  const execsQ = useRunbookExecutions({ problemId });
  const execs = execsQ.data ?? [];

  const [picking, setPicking] = useState(false);

  return (
    <div style={{ marginTop: 4 }}>
      <div style={{
        fontSize: 11, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4,
        marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8,
      }}>
        <span>Runbooks</span>
        <span style={{ flex: 1 }} />
        {canRun && (
          <button className="sec" style={{ fontSize: 11, padding: '2px 10px' }}
            onClick={() => setPicking(v => !v)}>
            {picking ? 'Cancel' : '▶ Run a runbook'}
          </button>
        )}
      </div>

      {picking && canRun && (
        <RunbookPicker problemId={problemId} onDone={() => setPicking(false)} />
      )}

      {/* Linked runs — the durable audit of what was run against this fire. */}
      {execsQ.isLoading ? (
        <Spinner />
      ) : execs.length === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>
          No runbook has been run for this problem yet.
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {execs.map(e => {
            const done = e.stepStates.filter(s =>
              s.status === 'completed' || s.status === 'skipped' || s.status === 'failed').length;
            return (
              <a key={e.id}
                href={`/runbook-exec?id=${encodeURIComponent(e.id)}`}
                onClick={ev => { ev.preventDefault(); navigate(`/runbook-exec?id=${encodeURIComponent(e.id)}`); }}
                style={{
                  display: 'flex', alignItems: 'center', gap: 8,
                  padding: '6px 10px', borderRadius: 6, textDecoration: 'none',
                  border: '1px solid var(--border)', background: 'var(--bg2)',
                  color: 'var(--text)', fontSize: 12,
                }}>
                <span className={`badge ${EXEC_BADGE[e.status] ?? 'b-gray'}`}>
                  {e.status.replace(/_/g, ' ')}
                </span>
                <span style={{ fontWeight: 600, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {e.titleSnapshot || 'Runbook run'}
                </span>
                <span style={{ color: 'var(--text3)', whiteSpace: 'nowrap' }}>
                  {done}/{e.stepStates.length} · {tsLong(e.startedAt)}
                  {e.startedBy ? ` · ${e.startedBy}` : ''}
                </span>
              </a>
            );
          })}
        </div>
      )}
    </div>
  );
}

// RunbookPicker — a filter input + scrollable list of ENABLED runbooks.
// Clicking one starts an execution tagged with the problemId and jumps to the
// live runner. Only enabled runbooks are offered (the backend rejects a
// disabled run with 409).
function RunbookPicker({ problemId, onDone }: { problemId: string; onDone: () => void }) {
  const rbQ = useRunbooks();
  const execute = useExecuteRunbook();
  const navigate = useNavigate();
  const [filter, setFilter] = useState('');
  const [err, setErr] = useState('');

  const matches = useMemo(() => {
    const all = (rbQ.data ?? []).filter(r => r.enabled);
    const q = filter.trim().toLowerCase();
    if (!q) return all;
    return all.filter(r =>
      r.title.toLowerCase().includes(q) ||
      (r.labels ?? []).some(l => l.toLowerCase().includes(q)));
  }, [rbQ.data, filter]);

  const run = async (rb: Runbook) => {
    setErr('');
    try {
      const exec = await execute.mutateAsync({ id: rb.id, problemId });
      onDone();
      navigate(`/runbook-exec?id=${encodeURIComponent(exec.id)}`);
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Failed to start the runbook');
    }
  };

  return (
    <div style={{
      marginBottom: 10, padding: 10, borderRadius: 6,
      border: '1px solid var(--border)', background: 'var(--bg2)',
    }}>
      <input autoFocus value={filter} onChange={e => setFilter(e.target.value)}
        placeholder="Filter runbooks…"
        style={{
          width: '100%', boxSizing: 'border-box', marginBottom: 8,
          fontSize: 12, padding: '5px 8px', borderRadius: 4,
          border: '1px solid var(--border)', background: 'var(--bg)', color: 'var(--text)',
        }} />

      {rbQ.isLoading ? (
        <Spinner />
      ) : matches.length === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>
          {(rbQ.data ?? []).some(r => r.enabled)
            ? 'No runbook matches that filter.'
            : <>No enabled runbooks. <a href="/runbooks" onClick={ev => { ev.preventDefault(); navigate('/runbooks'); }}>Create one ↗</a></>}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 220, overflowY: 'auto' }}>
          {matches.map(rb => (
            <button key={rb.id} disabled={execute.isPending}
              onClick={() => run(rb)}
              style={{
                textAlign: 'left', display: 'flex', alignItems: 'center', gap: 8,
                padding: '6px 8px', borderRadius: 4, fontSize: 12,
                border: '1px solid var(--border)', background: 'var(--bg)',
                color: 'var(--text)', cursor: execute.isPending ? 'wait' : 'pointer',
              }}>
              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {rb.title}
              </span>
              <span style={{ color: 'var(--text3)', whiteSpace: 'nowrap' }}>
                {rb.steps.length} step{rb.steps.length === 1 ? '' : 's'} ▶
              </span>
            </button>
          ))}
        </div>
      )}

      {err && <div style={{ fontSize: 12, color: 'var(--err)', marginTop: 8 }}>{err}</div>}
    </div>
  );
}
