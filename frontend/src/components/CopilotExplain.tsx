import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { IconSparkles } from './icons';

// CopilotExplain — drop-in Explain button that calls the
// Coremetry AI copilot endpoint for the given subject and renders the
// reply inline beneath the button. Self-hides when the copilot is
// not configured (no API key on the server).
//
// Six subject types share this component to avoid a button per call site:
//   - kind="trace"          → POST /api/copilot/explain-trace/{id}
//   - kind="problem"        → POST /api/copilot/explain-problem/{id}
//   - kind="incident"       → POST /api/copilot/explain-incident/{id}
//   - kind="anomaly"        → POST /api/copilot/explain-anomaly/{id}
//   - kind="service-health" → POST /api/copilot/explain-service?service=…&from=…&to=…
//                             ↑ takes (id=service, fromNs, toNs) instead of an
//                               ID lookup, because the prompt needs the live
//                               RED series for the current window.
//   - kind="runbook"        → POST /api/copilot/runbook/{id}
//                             ↑ same id shape as problem, but the model output
//                               is a numbered, actionable checklist anchored
//                               in past resolved instances of the same rule.
// Each endpoint uses a kind-specific system prompt so the model's
// answers match the operator's question.
export function CopilotExplain({ kind, id, label, fromNs, toNs, spanId }: {
  kind: 'trace' | 'span' | 'problem' | 'incident' | 'anomaly' | 'service-health' | 'runbook';
  id: string;
  label?: React.ReactNode;
  // Only used when kind === 'service-health'. Ignored otherwise.
  fromNs?: number;
  toNs?: number;
  // Only used when kind === 'span'. The target span inside the
  // trace identified by `id`. v0.5.144 per-span explain.
  spanId?: string;
}) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState<string | null>(null);
  const [meta, setMeta] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);

  if (enabled !== true) return null;

  const run = async () => {
    setBusy(true); setError(null); setText(null); setMeta(null);
    try {
      if (kind === 'runbook') {
        const r = await api.copilotRunbook(id);
        setText(r.explanation);
        // Surface the "based on N past resolutions" hint so the
        // operator knows whether the steps are grounded in
        // real history or first-principles.
        setMeta(r.similarCount > 0
          ? `Based on ${r.similarCount} past resolved instance${r.similarCount === 1 ? '' : 's'} of this rule on this service.`
          : `No past resolutions found — first-principles only.`);
      } else {
        const r = kind === 'trace'          ? await api.copilotExplainTrace(id)
                : kind === 'span'           ? await api.copilotExplainSpan(id, spanId ?? '')
                : kind === 'problem'        ? await api.copilotExplainProblem(id)
                : kind === 'incident'       ? await api.copilotExplainIncident(id)
                : kind === 'anomaly'        ? await api.copilotExplainAnomaly(id)
                :                             await api.copilotExplainServiceHealth(id, fromNs ?? 0, toNs ?? 0);
        setText(r.explanation);
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Explain failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ display: 'inline-flex', flexDirection: 'column', gap: 8, alignItems: 'flex-start' }}>
      <button onClick={run} disabled={busy} className="sec"
        style={{ padding: '5px 12px', fontSize: 12, color: 'var(--accent2)',
                 display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        {busy
          ? <><IconSparkles /> <span>Thinking…</span></>
          : (label ?? <><IconSparkles /> <span>AI explain</span></>)}
      </button>
      {error && (
        <div style={{
          padding: 10, borderRadius: 6, fontSize: 12,
          background: 'rgba(255,82,82,.10)', color: 'var(--err)',
          border: '1px solid rgba(255,82,82,.25)', maxWidth: 720,
        }}>{error}</div>
      )}
      {text && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'rgba(56,139,253,.08)',
          border: '1px solid rgba(56,139,253,.25)',
          color: 'var(--text)', whiteSpace: 'pre-wrap', maxWidth: 720,
        }}>
          <div style={{ fontSize: 10, color: 'var(--accent2)', marginBottom: 6, fontWeight: 700, letterSpacing: '.5px',
                        display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <IconSparkles size={11} /> {kind === 'runbook' ? 'RUNBOOK' : 'COPILOT'}
          </div>
          {meta && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, fontStyle: 'italic' }}>
              {meta}
            </div>
          )}
          {text}
        </div>
      )}
    </div>
  );
}
