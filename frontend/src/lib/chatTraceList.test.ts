import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { traceListBlocks } from './chatBlocks';
import type { ChatTypedBlock } from './types';

// v0.10.688 — trace_list bloğu: biriktirici şekil doğrular; ChatBubble
// tabloyu çizer (LLM anlatımı yok, D6); satır linki üreticiden (traceHref),
// "Daha fazla" sunucunun deepLink'i.
describe('traceListBlocks (v0.10.688)', () => {
  it('yalnız geçerli trace_list yükleri', () => {
    const good = { query: 'x', window: { fromNs: 1, toNs: 2 }, traces: [], deepLink: '/traces?search=x', truncated: false };
    const blocks: ChatTypedBlock[] = [
      { id: 'b1', type: 'trace_list', seq: 1, final: true, payload: good },
      { id: 'b2', type: 'trace_list', seq: 2, final: true, payload: { traces: 'nope' } },
      { id: 'b3', type: 'chart', seq: 3, final: true, payload: {} },
    ];
    expect(traceListBlocks(blocks)).toEqual([good]);
    expect(traceListBlocks(undefined)).toEqual([]);
  });
});

describe('BAĞLANMA', () => {
  const bubble = readFileSync(resolve(__dirname, '../components/ai/ChatBubble.tsx'), 'utf8');
  const list = readFileSync(resolve(__dirname, '../components/ai/ChatTraceList.tsx'), 'utf8');
  it('ChatBubble trace_list bloğunu ChatTraceList ile çizer', () => {
    expect(bubble).toContain('traceListBlocks(turn.blocks).map(');
    expect(bubble).toContain('<ChatTraceList');
  });
  it('satır linki üreticiden, daha fazla deepLink', () => {
    expect(list).toContain('traceHref(t.traceId)');
    expect(list).toContain('<Link to={tl.deepLink}>Daha fazla → Traces</Link>');
  });
});
