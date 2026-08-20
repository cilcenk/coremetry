// @vitest-environment jsdom
//
// v0.9.1193 (AI Faz 5.1) — 👎 yorum kutusunun BAĞLANMA sözleşmesi.
// Saf test yok çünkü buradaki iddiaların hepsi çalışma-zamanı dalı:
//
//   (1) kutu YALNIZ 👎 seçiliyken çıkar (👍'ye neden sormak anket olurdu);
//   (2) düz oy tıkı comment ALANINI HİÇ göndermez — preserve sözleşmesinin
//       FE yarısı. Alanı `undefined` diye göndermekle hiç göndermemek
//       JSON'da aynı kapıya çıkar ama bunu ancak gerçek çağrı gövdesi
//       kanıtlar; bağ koparsa herhangi bir flip operatörün yazdığı
//       yorumu sessizce silerdi;
//   (3) Gönder, yorumu verdict:-1 İLE BİRLİKTE gönderir (tam gövde) ve
//       başarıda "Kaydedildi"ye döner.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { AIFeedbackButtons } from './AIFeedbackButtons';
import { api } from '@/lib/api';

let host: HTMLDivElement;
let root: Root;

const buttons = () => Array.from(host.querySelectorAll('button'));
const thumbDown = () => buttons().find(b => b.getAttribute('aria-label') === 'Cevabı faydasız işaretle')!;
const box = () => host.querySelector<HTMLTextAreaElement>('textarea');
const send = () => buttons().find(b => (b.textContent ?? '').match(/Gönder|Kaydedildi/));

async function mount() {
  await act(async () => { root.render(<AIFeedbackButtons exchangeId="x1" />); });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe('AIFeedbackButtons — 👎 yorum kutusu', () => {
  it('kutu yalnız 👎 seçiliyken çıkar', async () => {
    vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount();
    expect(box()).toBeNull();

    await act(async () => { thumbDown().click(); });
    expect(box()).not.toBeNull();
  });

  it('düz oy tıkı comment alanını HİÇ göndermez (preserve sözleşmesi)', async () => {
    const spy = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount();
    await act(async () => { thumbDown().click(); });

    expect(spy).toHaveBeenCalledTimes(1);
    const body = spy.mock.calls[0][0];
    expect('comment' in body).toBe(false);
    expect(body.verdict).toBe(-1);
  });

  it('Gönder yorumu verdict:-1 ile birlikte gönderir ve Kaydedildi olur', async () => {
    const spy = vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount();
    await act(async () => { thumbDown().click(); });

    await act(async () => {
      const ta = box()!;
      // React controlled input: native setter + input event.
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!;
      set.call(ta, 'kanıt span listesi eksikti');
      ta.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => { send()!.click(); });

    expect(spy).toHaveBeenLastCalledWith({
      exchangeId: 'x1', verdict: -1, comment: 'kanıt span listesi eksikti',
    });
    expect(send()!.textContent).toContain('Kaydedildi');
  });

  it('boş yorumla Gönder pasif — boş POST atılamaz', async () => {
    vi.spyOn(api, 'postAIFeedback').mockResolvedValue({ ok: true });
    await mount();
    await act(async () => { thumbDown().click(); });
    expect((send() as HTMLButtonElement).disabled).toBe(true);
  });
});
