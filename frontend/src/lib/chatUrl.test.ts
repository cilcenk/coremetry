import { describe, it, expect } from 'vitest';
import { syncChatParam } from './chatUrl';

// v0.9.1258 — sohbet deep-link'inin saf yarısı.
describe('syncChatParam', () => {
  const P = (s: string) => new URLSearchParams(s);
  it('writes the id when the drawer is open with a persisted conversation', () => {
    expect(syncChatParam(P('range=1h'), 'c1', true)?.toString()).toBe('range=1h&chat=c1');
  });
  it('removes the param on close/clear, preserving foreign params', () => {
    expect(syncChatParam(P('range=1h&chat=c1'), null, true)?.toString()).toBe('range=1h');
    expect(syncChatParam(P('chat=c1&range=1h'), 'c1', false)?.toString()).toBe('range=1h');
  });
  it('returns null when nothing changes (no churn writes)', () => {
    expect(syncChatParam(P('chat=c1'), 'c1', true)).toBeNull();
    expect(syncChatParam(P('range=1h'), null, false)).toBeNull();
    expect(syncChatParam(P(''), null, true)).toBeNull(); // açık ama kimliksiz — yazma
  });
});
