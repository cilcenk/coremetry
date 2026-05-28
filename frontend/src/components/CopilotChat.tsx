import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
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
// `step` events render as tool-call chips, `answer` fills the
// assistant bubble.

type Turn = ChatMessage & { steps?: string[]; pending?: boolean; error?: string };

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
        } else if (e.kind === 'answer') {
          patchLast(t => ({ ...t, text: e.text, pending: false }));
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
      {/* Launcher */}
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
              <button onClick={() => setTurns([])} className="sec"
                style={{ fontSize: 11, padding: '2px 8px' }} title="Clear conversation">Clear</button>
            )}
            <button onClick={() => setOpen(false)} className="sec"
              style={{ fontSize: 14, padding: '2px 8px' }} title="Close">✕</button>
          </div>

          {/* Messages */}
          <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 14, display: 'flex', flexDirection: 'column', gap: 10 }}>
            {turns.length === 0 && (
              <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                <div style={{ marginBottom: 10 }}>Ask about your telemetry — grounded in live data.</div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {SAMPLE_QUESTIONS.map(q => (
                    <button key={q} onClick={() => send(q)} className="sec"
                      style={{ fontSize: 12, textAlign: 'left', padding: '6px 10px' }}>{q}</button>
                  ))}
                </div>
              </div>
            )}
            {turns.map((t, i) => (
              <ChatBubble key={i} turn={t} />
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
            <button type="submit" disabled={busy || !input.trim()}
              style={{ padding: '7px 14px', fontSize: 13 }}>
              {busy ? '…' : 'Send'}
            </button>
          </form>
        </div>
      )}
    </>
  );
}

function ChatBubble({ turn }: { turn: Turn }) {
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
    </div>
  );
}
