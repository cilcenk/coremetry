import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { IconSparkles } from './icons';

// CopilotExplain — drop-in Explain button that calls the
// Coremetry AI copilot endpoint for the given subject and renders the
// reply inline beneath the button. Self-hides when the copilot is
// not configured (no API key on the server).
//
// Five subject types share this component to avoid a button per call site:
//   - kind="trace"          → POST /api/copilot/explain-trace/{id}
//   - kind="problem"        → POST /api/copilot/explain-problem/{id}
//   - kind="incident"       → POST /api/copilot/explain-incident/{id}
//   - kind="anomaly"        → POST /api/copilot/explain-anomaly/{id}
//   - kind="service-health" → POST /api/copilot/explain-service?service=…&from=…&to=…
//                             ↑ takes (id=service, fromNs, toNs) instead of an
//                               ID lookup, because the prompt needs the live
//                               RED series for the current window.
// Each endpoint uses a kind-specific system prompt so the model's
// answers match the operator's question.
export function CopilotExplain({ kind, id, label, fromNs, toNs }: {
  kind: 'trace' | 'problem' | 'incident' | 'anomaly' | 'service-health';
  id: string;
  label?: React.ReactNode;
  // Only used when kind === 'service-health'. Ignored otherwise.
  fromNs?: number;
  toNs?: number;
}) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);

  if (enabled !== true) return null;

  const run = async () => {
    setBusy(true); setError(null); setText(null);
    try {
      const r = kind === 'trace'          ? await api.copilotExplainTrace(id)
              : kind === 'problem'        ? await api.copilotExplainProblem(id)
              : kind === 'incident'       ? await api.copilotExplainIncident(id)
              : kind === 'anomaly'        ? await api.copilotExplainAnomaly(id)
              :                             await api.copilotExplainServiceHealth(id, fromNs ?? 0, toNs ?? 0);
      setText(r.explanation);
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
            <IconSparkles size={11} /> COPILOT
          </div>
          {text}
        </div>
      )}
    </div>
  );
}
