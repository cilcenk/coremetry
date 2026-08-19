// @vitest-environment jsdom
//
// v0.9.1033 — AIExplainButton × `?ai=` URL sözleşmesi (ServiceCharts AI
// çekmecesi, giriş noktaları Ⓐ toolbar / Ⓑ kart başlığı).
//
// Neden gerçek mount: üç davranışın hiçbiri kaynak taramasıyla
// ölçülemez, hepsi çalışma zamanı dalı —
//   (1) copilot KAPALIYKEN düğme HİÇ render edilmez (mockup'ın
//       "Copilot kapalıysa düğmeler hiç render edilmez" maddesi;
//       kapalı bir kurulumda ölü affordance göstermek, tıklayınca
//       503 alan bir düğme demektir),
//   (2) tık ADRESE yazar (çekmece içeriğini adres belirler) ve
//       YABANCI paramları korur — ?tab=details&op=… kaybolursa
//       çekmeceyi açmak sayfanın kapsamını sıfırlardı,
//   (3) kapsam URL'e girer, yani Ⓐ ve Ⓑ FARKLI adres üretir; aksi
//       halde kart-kapsamlı ✨ ile toolbar düğmesi aynı cevabı açardı.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { AIExplainButton } from './AIExplainButton';
import { __resetCopilotEnabledCache } from './useCopilotEnabled';
import { api } from '@/lib/api';

let host: HTMLDivElement;
let root: Root;
let lastSearch = '';

function Probe() {
  lastSearch = useLocation().search;
  return null;
}

async function mount(initial: string, node: React.ReactNode) {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[initial]}>
        <Probe />
        {node}
      </MemoryRouter>,
    );
  });
}

function button(): HTMLButtonElement | null {
  return host.querySelector('button');
}

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  lastSearch = '';
  __resetCopilotEnabledCache();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

const CHARTS_SUBJECT = {
  kind: 'charts' as const, id: 'payment-service',
  fromNs: 1000, toNs: 2000, scope: 'dur' as const,
};

describe('AIExplainButton — copilot kapalıyken', () => {
  it('hiç render edilmez', async () => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: false });
    await mount('/service/x?tab=details', <AIExplainButton subject={CHARTS_SUBJECT} />);
    expect(button()).toBeNull();
  });

  it('config isteği patlarsa da render edilmez (fail-closed)', async () => {
    // Kapalı bir kurulumda ölü düğme göstermektense hiç göstermemek
    // doğru davranış: tık 503 döndürürdü.
    vi.spyOn(api, 'copilotConfig').mockRejectedValue(new Error('boom'));
    await mount('/service/x', <AIExplainButton subject={CHARTS_SUBJECT} />);
    expect(button()).toBeNull();
  });
});

describe('AIExplainButton — `?ai=` gidiş-dönüşü', () => {
  beforeEach(() => {
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true });
  });

  it('copilot açıkken render edilir', async () => {
    await mount('/service/x', <AIExplainButton subject={CHARTS_SUBJECT} />);
    expect(button()).not.toBeNull();
  });

  it('tık ?ai= yazar ve YABANCI paramları korur', async () => {
    await mount('/service/x?tab=details&op=POST+%2Fpay', <AIExplainButton subject={CHARTS_SUBJECT} />);
    await act(async () => { button()!.click(); });

    const p = new URLSearchParams(lastSearch);
    expect(p.get('ai')).toBe('charts:payment-service:1000:2000:dur');
    // Sayfanın kendi kapsamı çekmece açılınca KAYBOLMAMALI.
    expect(p.get('tab')).toBe('details');
    expect(p.get('op')).toBe('POST /pay');
  });

  it('aynı özneye ikinci tık çekmeceyi kapatır (?ai= silinir)', async () => {
    await mount('/service/x?tab=details', <AIExplainButton subject={CHARTS_SUBJECT} />);
    await act(async () => { button()!.click(); });
    expect(new URLSearchParams(lastSearch).get('ai')).not.toBeNull();

    await act(async () => { button()!.click(); });
    const p = new URLSearchParams(lastSearch);
    expect(p.get('ai')).toBeNull();
    expect(p.get('tab')).toBe('details'); // kapanış da yabancı paramı korur
  });

  // v0.9.1166 — ağırlık basamakları. Sınıf adları globals.css'in
  // sözleşmesi: `accent`+`sm` = tint chip, sınıfsız (=primary) + md ölçek
  // sınıfı YOK = dolu aksan. Kaynak taramasıyla ölçülemez, çünkü ikisi de
  // aynı JSX'ten çıkıyor; regresyon "strong sessizce accent'e döndü"
  // şeklinde gelir ve gözle fark edilmesi zordur.
  it('varsayılan ağırlık accent + sm (mevcut çağrılar değişmedi)', async () => {
    await mount('/service/x', <AIExplainButton subject={CHARTS_SUBJECT} />);
    const cls = button()!.className.split(/\s+/);
    expect(cls).toContain('accent');
    expect(cls).toContain('sm');
  });

  it('emphasis="strong" dolu birincil + md üretir', async () => {
    await mount('/trace/abc', <AIExplainButton subject={CHARTS_SUBJECT} emphasis="strong" />);
    const cls = button()!.className.split(/\s+/);
    expect(cls).not.toContain('accent');
    expect(cls).not.toContain('sm');
    expect(cls).not.toContain('xs');
    expect(cls).not.toContain('lg');
  });

  it('açık `size` emphasis\'i ezer (kart başlığındaki mini ✨ korunur)', async () => {
    await mount('/service/x', <AIExplainButton subject={CHARTS_SUBJECT} size="xs" />);
    expect(button()!.className.split(/\s+/)).toContain('xs');
  });

  it('Ⓐ (kapsam all) ile Ⓑ (kart kapsamı) FARKLI adres üretir', async () => {
    await mount('/service/x',
      <AIExplainButton subject={{ ...CHARTS_SUBJECT, scope: 'all' }} />);
    await act(async () => { button()!.click(); });
    const all = new URLSearchParams(lastSearch).get('ai');

    await act(async () => { root.unmount(); });
    host.remove();
    host = document.createElement('div');
    document.body.appendChild(host);
    root = createRoot(host);
    await mount('/service/x', <AIExplainButton subject={CHARTS_SUBJECT} />);
    await act(async () => { button()!.click(); });
    const dur = new URLSearchParams(lastSearch).get('ai');

    expect(all).toBe('charts:payment-service:1000:2000:all');
    expect(dur).toBe('charts:payment-service:1000:2000:dur');
    expect(all).not.toBe(dur);
  });
});
