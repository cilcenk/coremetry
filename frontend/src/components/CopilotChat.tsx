import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import type { ChatMessage } from '@/lib/types';

// CopilotChat (v0.6.53) — global in-app AI assistant. A fixed
// bottom-right launcher opens a drawer where the operator chats
// with an agent that queries their telemetry (the 7 MCP tools) to
// answer. Mounted once in AppShell like CommandPalette, so it's
// reachable on every authenticated page.
//
// Conversation is ephemeral component state — closing the drawer
// keeps it for the session; a reload clears it. Each send posts the
// full history to /api/copilot/chat and consumes the SSE stream:
// `step` events render as tool-call chips, `delta` events (v0.8.404,
// guided path) live-append tokens into the pending bubble, `answer`
// replaces it with the final text.

// exchangeId (v0.8.399) — server-minted per answer, carried on the SSE
// answer event; enables the thumbs up/down row. verdict is the local
// optimistic copy of what we last POSTed to /api/ai/feedback.
type Turn = ChatMessage & {
  steps?: string[]; pending?: boolean; error?: string;
  exchangeId?: string; verdict?: 1 | -1;
};

const SAMPLE_QUESTIONS = [
  'Which services are unhealthy right now?',
  'Show me errors in the last hour',
  'Why is the slowest endpoint slow?',
  'What problems are open?',
];

export function CopilotChat() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);

  // One-shot config probe — hide the launcher entirely when no AI
  // key is set so we don't dangle a dead button.
  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);

  // Autoscroll to the newest message on every update.
  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });
  }, [turns, open]);

  // Abort any in-flight stream on unmount.
  useEffect(() => () => abortRef.current?.abort(), []);

  if (!enabled) return null;

  // Thumbs up/down on an answer (v0.8.399). Optimistic: flip the local
  // verdict immediately, POST in the background, revert on failure.
  // Toggleable = clicking the other thumb replaces the verdict
  // (ReplacingMergeTree server-side); re-clicking the same is a no-op.
  const rate = (idx: number, verdict: 1 | -1) => {
    const turn = turns[idx];
    if (!turn?.exchangeId || turn.verdict === verdict) return;
    const prior = turn.verdict;
    const exchangeId = turn.exchangeId;
    setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict } : t)));
    api.postAIFeedback({ exchangeId, verdict }).catch(() => {
      setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict: prior } : t)));
    });
  };

  const send = async (text: string) => {
    const q = text.trim();
    if (!q || busy) return;
    setInput('');
    // History sent to the backend = prior turns + this question.
    // We only send role+text; tool plumbing is server-internal.
    const history: ChatMessage[] = [
      ...turns.filter(t => !t.error).map(t => ({ role: t.role, text: t.text })),
      { role: 'user', text: q },
    ];
    setTurns(prev => [
      ...prev,
      { role: 'user', text: q },
      { role: 'assistant', text: '', steps: [], pending: true },
    ]);
    setBusy(true);
    const ac = new AbortController();
    abortRef.current = ac;

    // Mutate the last (assistant) turn as events arrive.
    const patchLast = (fn: (t: Turn) => Turn) =>
      setTurns(prev => prev.map((t, i) => (i === prev.length - 1 ? fn(t) : t)));

    try {
      await api.copilotChat(history, (e) => {
        if (e.kind === 'step') {
          patchLast(t => ({ ...t, steps: [...(t.steps ?? []), e.tool] }));
        } else if (e.kind === 'delta') {
          // v0.8.404 — token streaming: live-append into the pending
          // bubble. The final `answer` event REPLACES the text below
          // (never appends), so deltas can't double-render.
          patchLast(t => ({ ...t, text: (t.text ?? '') + e.text }));
        } else if (e.kind === 'answer') {
          patchLast(t => ({ ...t, text: e.text, exchangeId: e.exchangeId, pending: false }));
        } else if (e.kind === 'error') {
          patchLast(t => ({ ...t, error: e.error, pending: false }));
        } else if (e.kind === 'done') {
          patchLast(t => ({ ...t, pending: false }));
        }
      }, ac.signal);
    } catch (err) {
      patchLast(t => ({ ...t, error: err instanceof Error ? err.message : String(err), pending: false }));
    } finally {
      setBusy(false);
      abortRef.current = null;
    }
  };

  return (
    <>
      {/* Launcher — deliberately NOT the shared <Button> atom: a
          circular fixed-position FAB is its own anatomy (48px round
          accent disc), not a text button; the atom's variants have
          nothing to offer it (U1 batch 2 judgement call). */}
      {!open && (
        <button
          onClick={() => setOpen(true)}
          title="Ask Coremetry AI"
          style={{
            position: 'fixed', right: 18, bottom: 18, zIndex: 60,
            width: 48, height: 48, borderRadius: 24,
            background: 'var(--accent2)', color: '#fff', border: 'none',
            fontSize: 20, cursor: 'pointer', boxShadow: '0 2px 12px rgba(0,0,0,0.35)',
          }}>
          ✨
        </button>
      )}

      {/* Drawer */}
      {open && (
        <div style={{
          position: 'fixed', right: 18, bottom: 18, zIndex: 60,
          width: 'min(420px, calc(100vw - 36px))', height: 'min(620px, calc(100vh - 100px))',
          display: 'flex', flexDirection: 'column',
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 10, boxShadow: '0 4px 24px rgba(0,0,0,0.4)',
        }}>
          {/* Header */}
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            padding: '10px 14px', borderBottom: '1px solid var(--border)',
          }}>
            <span style={{ fontWeight: 600, fontSize: 13 }}>✨ Coremetry AI</span>
            <span style={{ flex: 1 }} />
            {turns.length > 0 && (
              <Button variant="secondary" size="sm" onClick={() => setTurns([])}
                title="Clear conversation">Clear</Button>
            )}
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)}
              title="Close">✕</Button>
          </div>

          {/* Messages */}
          <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 14, display: 'flex', flexDirection: 'column', gap: 10 }}>
            {turns.length === 0 && (
              <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                <div style={{ marginBottom: 10 }}>Ask about your telemetry — grounded in live data.</div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {SAMPLE_QUESTIONS.map(q => (
                    <Button key={q} variant="secondary" size="sm" onClick={() => send(q)}
                      style={{ textAlign: 'left' }}>{q}</Button>
                  ))}
                </div>
              </div>
            )}
            {turns.map((t, i) => (
              <ChatBubble key={i} turn={t} onRate={v => rate(i, v)} />
            ))}
          </div>

          {/* Composer */}
          <form
            onSubmit={e => { e.preventDefault(); send(input); }}
            style={{ display: 'flex', gap: 8, padding: 10, borderTop: '1px solid var(--border)' }}>
            <input
              value={input}
              onChange={e => setInput(e.target.value)}
              placeholder="Ask about your services…"
              disabled={busy}
              autoFocus
              style={{
                flex: 1, padding: '7px 10px', fontSize: 13,
                background: 'var(--bg)', color: 'var(--text)',
                border: '1px solid var(--border)', borderRadius: 6,
              }} />
            <Button type="submit" disabled={busy || !input.trim()}>
              {busy ? '…' : 'Send'}
            </Button>
          </form>
        </div>
      )}
    </>
  );
}

function ChatBubble({ turn, onRate }: { turn: Turn; onRate?: (v: 1 | -1) => void }) {
  const isUser = turn.role === 'user';
  return (
    <div style={{ alignSelf: isUser ? 'flex-end' : 'flex-start', maxWidth: '85%' }}>
      <div style={{
        padding: '8px 11px', borderRadius: 10, fontSize: 13, lineHeight: 1.5,
        whiteSpace: 'pre-wrap', wordBreak: 'break-word',
        background: isUser ? 'var(--accent2)' : 'var(--bg2)',
        color: isUser ? '#fff' : 'var(--text)',
        border: isUser ? 'none' : '1px solid var(--border)',
      }}>
        {/* Tool-call progress chips (assistant only, while/after running) */}
        {!isUser && turn.steps && turn.steps.length > 0 && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginBottom: turn.text ? 6 : 0 }}>
            {turn.steps.map((s, i) => (
              <span key={i} style={{
                fontSize: 10, fontFamily: 'ui-monospace, monospace',
                padding: '1px 6px', borderRadius: 8,
                background: 'var(--bg3)', color: 'var(--text3)',
              }}>⚙ {s}</span>
            ))}
          </div>
        )}
        {turn.error
          ? <span style={{ color: isUser ? '#fff' : 'var(--err)' }}>⚠ {turn.error}</span>
          : turn.text || (turn.pending ? <span style={{ color: 'var(--text3)' }}>thinking…</span> : '')}
      </div>
      {/* Thumbs row (v0.8.399) — only for completed assistant answers
          that carry the server-minted exchangeId (old backends don't
          send one → row hides, rolling-deploy safe). */}
      {!isUser && !!turn.exchangeId && !turn.pending && !turn.error && onRate && (
        <div style={{ display: 'flex', gap: 2, marginTop: 2 }}>
          <Button variant="ghost" size="sm" onClick={() => onRate(1)}
            title="Helpful" aria-label="Rate answer helpful"
            style={{ padding: '0 6px', fontSize: 12, opacity: turn.verdict === 1 ? 1 : 0.4 }}>
            👍
          </Button>
          <Button variant="ghost" size="sm" onClick={() => onRate(-1)}
            title="Not helpful" aria-label="Rate answer not helpful"
            style={{ padding: '0 6px', fontSize: 12, opacity: turn.verdict === -1 ? 1 : 0.4 }}>
            👎
          </Button>
        </div>
      )}
    </div>
  );
}
