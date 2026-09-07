// chatPageContextBody.test.ts — v0.10.539: sayfa bağlamı GERÇEK gövdeye iner
// (fetch stub; v0.10.524 dersi: kaynak pini gövdeyi görmez). Verilmediğinde
// gövdede page/pinnedPage anahtarı HİÇ yoktur (eski sunucu uyumu).
import { describe, it, expect, vi, afterEach } from 'vitest';
import { api } from './api';
import type { PageContext } from './types';

function stubFetch(capture: { body?: unknown }) {
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    capture.body = init?.body ? JSON.parse(String(init.body)) : undefined;
    return new Response('event: done\ndata: {"ok":true}\n\n', { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
  }));
}

describe('copilotChat gövdesi — sayfa bağlamı', () => {
  afterEach(() => vi.unstubAllGlobals());
  it('page + pinnedPage context altına iner', async () => {
    const cap: { body?: { context?: Record<string, unknown> } } = {};
    stubFetch(cap);
    const page: PageContext = { page: 'traces', path: '/traces', cluster: 'c1', service: 'api', activeFilters: [{ k: 'status', op: '=', v: ['error'] }] };
    const pinned: PageContext = { page: 'problems', path: '/problems', problemId: 'p1' };
    await api.copilotChat([{ role: 'user', text: 'neden?' }], () => {}, undefined, 'api', undefined, undefined, undefined, 3600,
      undefined, undefined, undefined, undefined, undefined, page, pinned);
    expect(cap.body?.context?.page).toEqual(page);
    expect(cap.body?.context?.pinnedPage).toEqual(pinned);
    expect(cap.body?.context?.service).toBe('api');
  });
  it('verilmediğinde anahtar yok', async () => {
    const cap: { body?: { context?: Record<string, unknown> } } = {};
    stubFetch(cap);
    await api.copilotChat([{ role: 'user', text: 'x' }], () => {}, undefined, 'api');
    expect(cap.body?.context).not.toHaveProperty('page');
    expect(cap.body?.context).not.toHaveProperty('pinnedPage');
  });
});
