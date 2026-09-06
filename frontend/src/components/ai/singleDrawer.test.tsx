// @vitest-environment jsdom
//
// v0.10.492 (Astra #12a) — TEK ÇEKMECE, davranışla: ?ai= ve ?chat= birlikte
// gelse de sayfada bir tane role="dialog" açılır (461 kabuğu eşitlemişti ama
// iki bileşen iki çekmece açıyordu; 483 birleştirdi — bu test onu render'la
// pinler, kaynak dizesiyle değil). Kapatma ?ai='i adresten siler.
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CopilotChat } from '@/components/CopilotChat';
import { AIDrawer } from './AIDrawer';
import { __resetCopilotEnabledCache } from './useCopilotEnabled';
import { api } from '@/lib/api';
import { AuthProvider } from '@/components/AuthProvider';
import { ConfirmProvider } from '@/components/ui/ConfirmDialog';

let host: HTMLDivElement;
let root: Root;
let search = '';
function Probe() { search = useLocation().search; return null; }

beforeEach(() => {
  window.matchMedia = ((q: string) => ({ matches: false, media: q, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false })) as unknown as typeof window.matchMedia;
  (Element.prototype as unknown as { scrollTo: () => void }).scrollTo = () => {};
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  __resetCopilotEnabledCache();
  vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: 'gemma4' } as never);
  vi.spyOn(api, 'copilotExplainServiceHealth').mockRejectedValue(new Error('gövde kapsam dışı'));
  vi.spyOn(api, 'me').mockResolvedValue({ id: 'u1', email: 'op@x.io', role: 'admin', firstName: 'Cenk' } as never);
  vi.spyOn(api, 'problemsCount').mockResolvedValue({ count: 0 } as never);
  vi.spyOn(api, 'aiConversations').mockResolvedValue([] as never);
  vi.spyOn(api, 'aiConversation').mockResolvedValue({ id: 'c1', title: 't', updatedAt: 0, messages: [] } as never);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe('tek AI çekmecesi', () => {
  it('?ai= + ?chat= birlikte: AppShell gibi iki bileşen mount edilse de tek dialog; ✕ ?ai=\'i siler', async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={['/service?service=payment&chat=c1&ai=service-health%3Apayment%3A1000%3A2000']}>
          <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
            <AuthProvider><ConfirmProvider><CopilotChat /><Probe /></ConfirmProvider></AuthProvider>
          </QueryClientProvider>
        </MemoryRouter>,
      );
    });
    await act(async () => { await new Promise(r => setTimeout(r, 30)); });
    const dialogs = document.body.querySelectorAll('[role="dialog"]');
    expect(dialogs.length, 'tek çekmece').toBe(1);
    expect(document.body.textContent).toContain('CoSRE');
    // Açıklama kipi: başlık meta öznesi ✨ ile.
    expect(document.body.textContent).toContain('✨');
    const close = Array.from(document.body.querySelectorAll('button[aria-label="Close"]'))[0] as HTMLButtonElement | undefined;
    expect(close, 'kapatma düğmesi').toBeTruthy();
    await act(async () => { close!.click(); });
    await act(async () => { await new Promise(r => setTimeout(r, 10)); });
    expect(search).not.toContain('ai=');
    expect(document.body.querySelectorAll('[role="dialog"]').length).toBe(0);
  });

  it('AIDrawer sarmalayıcısı CopilotChat ile aynı tek çekmeceyi açar', async () => {
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={['/service?service=payment&ai=service-health%3Apayment%3A1000%3A2000']}>
          <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
            <AuthProvider><ConfirmProvider><AIDrawer /></ConfirmProvider></AuthProvider>
          </QueryClientProvider>
        </MemoryRouter>,
      );
    });
    await act(async () => { await new Promise(r => setTimeout(r, 30)); });
    expect(document.body.querySelectorAll('[role="dialog"]').length).toBe(1);
  });
});
