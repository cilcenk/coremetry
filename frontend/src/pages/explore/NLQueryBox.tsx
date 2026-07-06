import { useState } from 'react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';

// NLQueryBox — v0.5.255 natural-language search input.
// Operator types a plain-English description; the Copilot returns
// a strict-JSON filter set + time range the parent applies.
// Hidden when copilot isn't configured (the endpoint returns 503
// → silent failure mode rather than a misleading "AI failed" toast).
//
// Phase-1 extraction (explore-v2): moved verbatim out of Explore.tsx
// into pages/explore/. Self-contained — owns its own prompt/busy/
// state; parent passes an onApply callback. No behaviour change.
export function NLQueryBox({
  onApply,
}: {
  onApply: (filters: { k: string; op: string; v: string[] }[], preset: string) => void;
}) {
  const [prompt, setPrompt] = useState('');
  const [busy, setBusy] = useState(false);
  const [state, setState] = useState<
    | { kind: 'idle' }
    | { kind: 'ok'; explain: string; preset: string; filterCount: number }
    | { kind: 'warn'; msg: string; raw?: string }
    | { kind: 'err'; msg: string }
    | { kind: 'unavailable' }
  >({ kind: 'idle' });

  const run = async () => {
    const trimmed = prompt.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      const r = await api.copilotNLToQuery(trimmed);
      if (r.warning) {
        setState({ kind: 'warn', msg: r.warning, raw: r.raw });
        return;
      }
      if (!r.filters || r.filters.length === 0) {
        setState({ kind: 'warn', msg: 'Model produced no filters — try rephrasing.' });
        return;
      }
      onApply(r.filters, r.range.preset);
      setState({
        kind: 'ok',
        explain: r.explain,
        preset: r.range.preset,
        filterCount: r.filters.length,
      });
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      // 503 = Copilot not wired; hide the box rather than nag.
      if (msg.toLowerCase().includes('not configured')) {
        setState({ kind: 'unavailable' });
      } else {
        setState({ kind: 'err', msg });
      }
    } finally {
      setBusy(false);
    }
  };

  if (state.kind === 'unavailable') return null;

  return (
    <div style={{
      marginBottom: 10, padding: 8,
      background: 'rgba(139,92,246,0.04)',
      border: '1px solid rgba(139,92,246,0.20)',
      borderRadius: 6,
    }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={{
          fontSize: 12, fontWeight: 600,
          color: 'var(--accent2, #a78bfa)',
          whiteSpace: 'nowrap',
        }}>✦ Natural language</span>
        <input
          value={prompt}
          onChange={e => setPrompt(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && !busy && run()}
          placeholder={`Try: "yesterday's slow checkouts" · "5xx from auth-service last hour" · "kafka producer errors today"`}
          disabled={busy}
          style={{ flex: 1, fontSize: 13 }} />
        <Button size="sm" onClick={run} disabled={busy || !prompt.trim()}
          style={{ whiteSpace: 'nowrap' }}>
          {busy ? 'Thinking…' : 'Apply'}
        </Button>
      </div>
      {state.kind === 'ok' && (
        <div style={{
          marginTop: 6, fontSize: 11, color: 'var(--text2)',
          display: 'flex', gap: 8, alignItems: 'baseline',
        }}>
          <span style={{ color: 'var(--ok)' }}>✓</span>
          <span>
            Applied <b>{state.filterCount}</b> filter{state.filterCount === 1 ? '' : 's'} · range
            {' '}<code style={{ fontFamily: 'ui-monospace, monospace' }}>{state.preset}</code>
            {state.explain && <> · <span style={{ color: 'var(--text3)' }}>{state.explain}</span></>}
          </span>
        </div>
      )}
      {state.kind === 'warn' && (
        <div style={{
          marginTop: 6, fontSize: 11, color: 'var(--warn)',
        }}>
          ⚠ {state.msg}
          {state.raw && (
            <pre style={{
              marginTop: 4, padding: 6, borderRadius: 4,
              background: 'var(--bg0)', border: '1px solid var(--border)',
              fontSize: 10, maxHeight: 80, overflow: 'auto',
              whiteSpace: 'pre-wrap',
            }}>{state.raw}</pre>
          )}
        </div>
      )}
      {state.kind === 'err' && (
        <div style={{ marginTop: 6, fontSize: 11, color: 'var(--err)' }}>
          ✗ {state.msg}
        </div>
      )}
    </div>
  );
}
