// @vitest-environment jsdom
//
// v0.9.1121 (Faz 0.3b) — tek-atış ✨ geri bildirim rayının sözleşmesi.
//
// NEDEN GERÇEK MOUNT: dört davranışın hiçbiri kaynak taramasıyla
// ölçülemez, hepsi çalışma zamanı dalı —
//   (1) kimlik YOKSA hiç render edilmez (v0.9.592'nin dersi: cevabını
//       kaydedemeyeceğimiz soruyu sormak ölü affordance'tır),
//   (2) tık GERÇEKTEN POST eder — bu rayın var olma sebebi, tıklayınca
//       hiçbir yere yazmayan bir düğmenin depoda bulunmuş olması,
//   (3) POST patlarsa seçim GERİ ALINIR — seçili kalmak "kaydedildi"
//       yalanının daha sessiz biçimi,
//   (4) aynı oya ikinci tık no-op, ters oya tık yeniden POST.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { AIFeedbackButtons } from './AIFeedbackButtons';
import { api } from '@/lib/api';

let host: HTMLDivElement;
let root: Root;

async function mount(node: React.ReactNode) {
  await act(async () => { root.render(node); });
}

const buttons = () => Array.from(host.querySelectorAll('button'));
const up = () => buttons()[0];
const down = () => buttons()[1];

beforeEach(() => {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe('AIFeedbackButtons — kimlik kapısı', () => {
  it('exchangeId yoksa HİÇ render edilmez', async () => {
    await mount(<AIFeedbackButtons />);
    expect(buttons()).toHaveLength(0);
  });

  it('exchangeId boş dizeyse de render edilmez (omitempty düşmemiş ama boş)', async () => {
    await mount(<AIFeedbackButtons exchangeId="" />);
    expect(buttons()).toHaveLength(0);
  });

  it('kimlik varsa iki düğme çizilir', async () => {
    await mount(<AIFeedbackButtons exchangeId="x1" />);
    expect(buttons()).toHaveLength(2);
  });
});

describe('AIFeedbackButtons — oy gönderimi', () => {
  it('tık POST eder ve seçili durumu DUYURUR (aria-pressed)', async () => {
    const post = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount(<AIFeedbackButtons exchangeId="abc123" />);
    await act(async () => { up().click(); });

    expect(post).toHaveBeenCalledWith({ exchangeId: 'abc123', verdict: 1 });
    expect(up().getAttribute('aria-pressed')).toBe('true');
    expect(down().getAttribute('aria-pressed')).toBe('false');
  });

  it('👎 de aynı raydan gider', async () => {
    const post = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount(<AIFeedbackButtons exchangeId="abc123" />);
    await act(async () => { down().click(); });

    expect(post).toHaveBeenCalledWith({ exchangeId: 'abc123', verdict: -1 });
    expect(down().getAttribute('aria-pressed')).toBe('true');
  });

  it('POST patlarsa seçim GERİ ALINIR', async () => {
    vi.spyOn(api, 'postAIFeedback').mockRejectedValue(new Error('boom'));
    await mount(<AIFeedbackButtons exchangeId="abc123" />);
    await act(async () => { up().click(); });

    expect(up().getAttribute('aria-pressed')).toBe('false');
  });

  it('aynı oya ikinci tık NO-OP (çift POST yok)', async () => {
    const post = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount(<AIFeedbackButtons exchangeId="abc123" />);
    await act(async () => { up().click(); });
    await act(async () => { up().click(); });

    expect(post).toHaveBeenCalledTimes(1);
  });

  it('ters oya tık YENİDEN POST eder (sunucu exchangeId ile değiştirir)', async () => {
    const post = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount(<AIFeedbackButtons exchangeId="abc123" />);
    await act(async () => { up().click(); });
    await act(async () => { down().click(); });

    expect(post).toHaveBeenCalledTimes(2);
    expect(post).toHaveBeenLastCalledWith({ exchangeId: 'abc123', verdict: -1 });
    expect(up().getAttribute('aria-pressed')).toBe('false');
    expect(down().getAttribute('aria-pressed')).toBe('true');
  });

  it('ters oy patlarsa ÖNCEKİ oya döner (null\'a değil)', async () => {
    const post = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount(<AIFeedbackButtons exchangeId="abc123" />);
    await act(async () => { up().click(); });
    post.mockRejectedValueOnce(new Error('boom'));
    await act(async () => { down().click(); });

    expect(up().getAttribute('aria-pressed')).toBe('true');
    expect(down().getAttribute('aria-pressed')).toBe('false');
  });

  it('kimlik değişince oy TAŞINMAZ — "Yeniden sor" taze cevabın oyu boştur', async () => {
    vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount(<AIFeedbackButtons exchangeId="first" />);
    await act(async () => { up().click(); });
    expect(up().getAttribute('aria-pressed')).toBe('true');

    await mount(<AIFeedbackButtons exchangeId="second" />);
    expect(up().getAttribute('aria-pressed')).toBe('false');
    expect(down().getAttribute('aria-pressed')).toBe('false');
  });
});
