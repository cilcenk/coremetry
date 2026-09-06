// @vitest-environment jsdom
//
// v0.9.1037 — AI çekmecesinin model çipi.
//
// Neden gerçek mount: üç iddianın hiçbiri kaynak taramasıyla ölçülemez.
//
//   (1) Çip YALNIZ model doluyken çizilir. Kapalı/modelsiz kurulumda boş
//       bir rozet "bilmiyorum"un gürültülü hâli olurdu.
//   (2) Çip TIKLANAMAZ olmalı — `<button>` DEĞİL. ui/Chip atomu
//       `onRemove`suz hâlde her zaman bir <button> basıyor; oraya
//       düşersek hover'da arka planı değişen ama hiçbir şey yapmayan
//       sahte bir affordance doğar.
//   (3) Metin YALNIZ model adı. "yerel" / "local" gibi bir KONUM iddiası
//       prod'da YANLIŞ olur (LLM uzak uçta) — ve yanlış bir iddiayı
//       ekrana basmak, hiç basmamaktan kötüdür.
//
// Dördüncü iddia mount'suz: /api/copilot/config'e İKİNCİ bir istek
// eklenmedi — çip mevcut modül cache'inden besleniyor.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { MemoryRouter } from 'react-router-dom';

// jsdom'da window.matchMedia YOK ve uPlot onu MODÜL YÜKLENİRKEN çağırıyor
// (setPxRatio, uPlot.cjs.js:80). AIDrawer'ın import zinciri chart gövdesine
// uzandığı için stub IMPORTLARDAN ÖNCE durmalı — `vi.hoisted` dosyanın
// tepesine kaldırılır, normal bir beforeAll çok geç olurdu. Emsal:
// components/chart/CorePanel.smoke.test.tsx.
vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({
    matches: false, media: q, onchange: null,
    addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
});

import { AIDrawer } from './AIDrawer';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthProvider } from '@/components/AuthProvider';
import { ConfirmProvider } from '@/components/ui/ConfirmDialog';
import { __resetCopilotEnabledCache } from './useCopilotEnabled';
import { api } from '@/lib/api';

let host: HTMLDivElement;
let root: Root;

// ?ai=<kind>:<id> — çekmeceyi açan tek şey adres.
const OPEN = '/service?service=payment&ai=service-health%3Apayment%3A1000%3A2000';

async function mountDrawer() {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[OPEN]}>
        {/* v0.10.483 — ✨ Explain aynı çekmecede: AIDrawer → CopilotChat (sağlayıcılar onun) */}
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <AuthProvider><ConfirmProvider><AIDrawer /></ConfirmProvider></AuthProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );
  });
}

function chip(): HTMLElement | null {
  return document.querySelector('.chip');
}

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  __resetCopilotEnabledCache();
  // Çekmece gövdesi kendi ucunu çağırır; bu test yalnız BAŞLIK şeridiyle
  // ilgili, gövde isteğini sessizce düşürüyoruz.
  vi.spyOn(api, 'copilotExplainServiceHealth')
    .mockRejectedValue(new Error('gövde kapsam dışı'));
  // v0.10.483 — kabuk artık CopilotChat: AuthProvider `me` reddedince /login'e
  // yönlenir ve ?ai= silinirdi; sohbet kabuğunun diğer okumaları da mock.
  vi.spyOn(api, 'me').mockResolvedValue({ id: 'u1', email: 'op@x.io', role: 'admin', firstName: 'Cenk' } as never);
  vi.spyOn(api, 'problemsCount').mockResolvedValue({ count: 0 } as never);
  vi.spyOn(api, 'aiConversations').mockResolvedValue([] as never);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe('AI çekmecesi — model çipi', () => {
  it('model doluyken çizilir ve YALNIZ model adını taşır', async () => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({
      enabled: true, model: 'gemma4-26b-a4b-it',
    });
    await mountDrawer();
    const c = chip();
    expect(c).not.toBeNull();
    expect(c?.textContent).toContain('gemma4-26b-a4b-it');
    // Konum iddiası YOK — prod'da LLM uzak uçta.
    expect(c?.textContent?.toLowerCase()).not.toMatch(/yerel|local|on-?prem/);
  });

  it('model YOKKEN çip hiç çizilmez (copilot açık olsa bile)', async () => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true });
    await mountDrawer();
    expect(chip()).toBeNull();
  });

  it('model boş STRING de çip doğurmaz', async () => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: '' });
    await mountDrawer();
    expect(chip()).toBeNull();
  });

  it('çip tıklanabilir bir öğe DEĞİL (sahte affordance yok)', async () => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({
      enabled: true, model: 'gemma4-26b-a4b-it',
    });
    await mountDrawer();
    const c = chip();
    expect(c?.tagName).toBe('SPAN');
    expect(c?.querySelector('button')).toBeNull();
  });

  it('copilot KAPALIYKEN çekmece hiç açılmaz — çip de yok', async () => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({
      // Kapalı kurulumda backend model'i zaten omit eder; istemci yine de
      // "enabled" bayrağına bakmalı — model dolu diye çekmece açılmaz.
      enabled: false, model: 'gemma4-26b-a4b-it',
    });
    await mountDrawer();
    expect(chip()).toBeNull();
  });

  it('config ucuna çekmece başına TEK istek gider', async () => {
    const spy = vi.spyOn(api, 'copilotConfig').mockResolvedValue({
      enabled: true, model: 'gemma4-26b-a4b-it',
    });
    await mountDrawer();
    expect(chip()).not.toBeNull();
    // Modül cache'i: çip için ikinci bir fetch EKLENMEDİ.
    expect(spy.mock.calls.length).toBe(1);
  });
});
