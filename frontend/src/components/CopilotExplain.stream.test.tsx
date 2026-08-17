// @vitest-environment jsdom
//
// v0.9.1127 (AI Faz 1.5) — tek-atış ✨ Explain'in AKAN render sözleşmesi.
//
// NEDEN GERÇEK MOUNT: dördü de çalışma zamanı dalı, kaynak taramasıyla
// ölçülemez —
//   (1) delta geldikçe metin BÜYÜR (biriktirme; tek parça basmak
//       akışın tüm değerini yok eder),
//   (2) ilk token'a kadar Spinner, token'lardan SONRA imleç — ikisi
//       birlikte "hem bekliyor hem yazıyor" diye okunur,
//   (3) 👍/👎 yalnız answer çerçevesinden sonra çıkar (kimlik oradan
//       gelir; erken çizilen düğme oy kaydedemez — v0.9.592'nin dersi),
//   (4) "Yeniden sor" uçuştaki akışı KESER, yoksa eski delta'lar yeni
//       cevabın üstüne yazar ve panelde iki cevap iç içe geçer.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { CopilotExplain } from './CopilotExplain';
import { __resetCopilotEnabledCache } from './ai/useCopilotEnabled';
import { api } from '@/lib/api';
import type { ExplainStreamOpts } from '@/lib/api';

let host: HTMLDivElement;
let root: Root;

async function mount(node: React.ReactNode) {
  await act(async () => { root.render(node); });
}

const panelText = () => host.textContent ?? '';
const cursor = () => host.querySelector('.cm-ai-cursor');
const feedbackButtons = () => Array.from(host.querySelectorAll('button[aria-pressed]'));
const rerunButton = () =>
  Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('Yeniden sor'));

/**
 * fakeExplain — akışı testin sürdüğü sahte uç.
 *
 * Çağrıları AYRI AYRI tutuyor (tek bir "son çağrı" değil): "Yeniden sor"
 * ikinci bir çağrı başlatıyor ve testin sorusu tam olarak BİRİNCİSİNE ne
 * olduğu. Tek slot tutan bir sahte, iptal edilmemiş yeni signal'i ölçüp
 * yeşil dönerdi.
 */
interface FakeCall {
  emit: (t: string) => Promise<void>;
  finish: (explanation: string, exchangeId?: string) => Promise<void>;
  fail: (e: unknown) => Promise<void>;
  aborted: () => boolean;
}

function fakeExplain() {
  const seen: FakeCall[] = [];
  vi.spyOn(api, 'copilotExplainProblem').mockImplementation(
    (_id: string, opts?: ExplainStreamOpts) => {
      let resolve!: (v: { explanation: string; exchangeId?: string }) => void;
      let reject!: (e: unknown) => void;
      const p = new Promise<{ explanation: string; exchangeId?: string }>((res, rej) => {
        resolve = res; reject = rej;
      });
      seen.push({
        emit: async t => { await act(async () => { opts?.onDelta?.(t); }); },
        finish: async (explanation, exchangeId = 'x1') => {
          await act(async () => { resolve({ explanation, exchangeId }); });
        },
        fail: async e => {
          reject(e);
          await act(async () => { await p.catch(() => {}); });
        },
        aborted: () => opts?.signal?.aborted ?? false,
      });
      return p;
    });
  return {
    call: (i = 0) => seen[i],
    count: () => seen.length,
    emit: (t: string) => seen[seen.length - 1].emit(t),
    finish: (explanation: string, exchangeId = 'x1') =>
      seen[seen.length - 1].finish(explanation, exchangeId),
  };
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  __resetCopilotEnabledCache();
  vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: 'gemma4' });
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe('CopilotExplain — akan render', () => {
  it('delta geldikçe metin BÜYÜR', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);

    // İlk token'dan önce: Spinner, metin yok.
    expect(panelText()).toContain('CoSRE düşünüyor');
    expect(cursor()).toBeNull();

    await f.emit('Kök ');
    expect(panelText()).toContain('Kök');
    expect(panelText()).not.toContain('CoSRE düşünüyor');

    await f.emit('neden: redis.');
    expect(panelText()).toContain('Kök neden: redis.');
  });

  it('akış sürerken imleç var, cevap bitince YOK', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);

    await f.emit('yarım');
    expect(cursor()).not.toBeNull();

    await f.finish('Kök neden: redis.');
    expect(cursor()).toBeNull();
  });

  it('answer çerçevesi biriken metni EZER', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.emit('yarım cevap');
    await f.finish('tam ve nihai cevap');
    expect(panelText()).toContain('tam ve nihai cevap');
    expect(panelText()).not.toContain('yarım cevap');
  });

  it('👍/👎 yalnız cevap TAMAMLANINCA çıkar', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);

    await f.emit('akıyor…');
    expect(feedbackButtons()).toHaveLength(0); // kimlik henüz yok

    await f.finish('bitti', 'xid-42');
    expect(feedbackButtons()).toHaveLength(2);
  });

  it('sıfır delta (buffered geri düşüşü) yine de cevabı çizer', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.finish('tek parça cevap');
    expect(panelText()).toContain('tek parça cevap');
    expect(cursor()).toBeNull();
  });
});

describe('CopilotExplain — Yeniden sor', () => {
  it('uçuştaki akışı İPTAL eder ve ekranı sıfırlar', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.emit('eski cevap');
    await f.finish('eski cevap tamam', 'xid-eski');
    expect(panelText()).toContain('eski cevap tamam');
    expect(feedbackButtons()).toHaveLength(2);

    const btn = rerunButton();
    expect(btn).toBeDefined();
    await act(async () => { btn!.click(); });

    expect(f.call(0).aborted()).toBe(true);    // BİRİNCİ akış kesildi
    expect(f.count()).toBe(2);                 // ve ikincisi başladı
    expect(panelText()).not.toContain('eski cevap tamam');
    expect(feedbackButtons()).toHaveLength(0); // eski oy yeni cevabın üstünde kalamaz
  });

  it('kesilen eski akışın delta\'ları yeni cevaba KARIŞMAZ', async () => {
    const f = fakeExplain();
    await mount(<CopilotExplain kind="problem" id="p1" auto />);
    await f.emit('eski');
    await f.finish('eski tamam');
    await act(async () => { rerunButton()!.click(); });

    // Yeni akış konuşuyor…
    await f.call(1).emit('yeni ');
    // …ve gecikmiş eski çağrı AbortError ile düşüyor. Panel bunu HATA
    // olarak göstermemeli: kullanıcının kendi eylemi kırmızı kutu değil.
    await f.call(0).fail(new DOMException('aborted', 'AbortError'));

    expect(panelText()).toContain('yeni');
    expect(panelText()).not.toContain('aborted');
    expect(panelText()).not.toContain('Explain failed');
    expect(cursor()).not.toBeNull(); // yeni akış hâlâ sürüyor
  });
});
