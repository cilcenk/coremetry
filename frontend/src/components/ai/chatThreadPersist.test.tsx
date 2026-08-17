// @vitest-environment jsdom
//
// v0.9.1139 (AI Faz 4.1) — kalıcılığın BAĞLANMA testi.
//
// chatPersist.test.ts saf dönüşümleri ölçüyor; buradaki iddialar
// çalışma-zamanı dalları ve kaynak taramasıyla ölçülemez:
//
//   (1) KAYDETME GÖNDERMEYİ BLOKLAMAZ. Sohbetin en pahalı özelliği
//       akıcılığı: `await api.saveAiConversation(...)` yazmak tsc'yi
//       memnun eder, testleri geçer ve sohbeti arşiv ucunun gecikmesine
//       bağlar. Askıda kalan (hiç resolve olmayan) bir kaydetmeyle
//       ikinci sorunun HÂLÂ gidebildiğini ölçüyoruz;
//   (2) KİMLİĞİ SUNUCU BASAR. İlk yazım `id` taşımaz, ikincisi yanıttan
//       gelen id'yi taşır. Bu kırılırsa her tur YENİ bir thread açar ve
//       geçmiş listesi aynı konuşmanın kopyalarıyla dolar;
//   (3) HATA SESSİZ. Kaydetme reddedilince sohbet çalışmaya devam eder;
//   (4) "Temizle" kalıcı kimliği de düşürür — yoksa temizlenmiş ekranın
//       ilk cevabı arşivdeki DOLU thread'in üstüne yazılır;
//   (5) Geçmiş listesi çekmece açılışında çekilir, satıra tıklamak
//       konuşmayı EKRANA getirir (turlar gerçekten çizilir).
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

// jsdom'da window.matchMedia YOK ve uPlot onu MODÜL YÜKLENİRKEN çağırıyor
// (setPxRatio, uPlot.cjs.js:80). CopilotChat'in import zinciri chart
// gövdesine uzanıyor, bu yüzden stub IMPORTLARDAN ÖNCE durmalı —
// `vi.hoisted` dosyanın tepesine kaldırılır. Emsal: ai/modelChip.test.tsx.
vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({
    matches: false, media: q, onchange: null,
    addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
});

import { api } from '@/lib/api';
import type { AiConversation, AiConversationSummary, ChatStreamEvent } from '@/lib/types';
import { useChatThread } from './useChatThread';
import { AuthProvider } from '@/components/AuthProvider';
import { ConfirmProvider } from '@/components/ui/ConfirmDialog';
import { CopilotChat } from '@/components/CopilotChat';

let host: HTMLDivElement;
let root: Root;

const text = () => host.textContent ?? '';

/** Tek turda cevap veren sahte SSE. */
function stubChat(answer = 'p95 480ms') {
  return vi.spyOn(api, 'copilotChat').mockImplementation(
    async (_msgs, onEvent: (e: ChatStreamEvent) => void) => {
      onEvent({ kind: 'answer', text: answer, exchangeId: 'x1' });
      onEvent({ kind: 'done', ok: true });
    });
}

const conv = (over: Partial<AiConversation> = {}): AiConversation => ({
  id: 'srv-1', title: 'checkout yavaş mı?', updatedAt: 5, messages: [], ...over,
});

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
  vi.useRealTimers();
});

// ── (1)-(4): hook seviyesi. useChatThread hiçbir context kullanmıyor,
// bu yüzden sonda bir prob bileşeni yeterli.
type Thread = ReturnType<typeof useChatThread>;
let thread: Thread;

function Probe({ persist = true }: { persist?: boolean }) {
  thread = useChatThread({ persist });
  return (
    <div>
      {thread.turns.map((t, i) => <p key={i}>{`${t.role}:${t.text}`}</p>)}
    </div>
  );
}

async function mountProbe(persist = true) {
  await act(async () => { root.render(<Probe persist={persist} />); });
}

/** Debounce'u geçir + bekleyen promise'leri boşalt. */
async function flushPersist() {
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
}

describe('useChatThread — kalıcılık', () => {
  it('cevap tamamlanınca kaydeder; kimliği SUNUCUDAN devralır', async () => {
    vi.useFakeTimers();
    stubChat();
    const save = vi.spyOn(api, 'saveAiConversation')
      .mockResolvedValue(conv({ id: 'srv-1' }));

    await mountProbe();
    await act(async () => { await thread.send('checkout yavaş mı?'); });
    expect(save).not.toHaveBeenCalled(); // debounce
    await flushPersist();

    expect(save).toHaveBeenCalledTimes(1);
    expect(save.mock.calls[0][0]).toEqual({
      id: undefined, subject: undefined,
      messages: [
        { role: 'user', text: 'checkout yavaş mı?' },
        { role: 'assistant', text: 'p95 480ms' },
      ],
    });
    expect(thread.conversationId).toBe('srv-1');

    // İkinci tur AYNI thread'e yazar.
    await act(async () => { await thread.send('peki loglar?'); });
    await flushPersist();
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1][0].id).toBe('srv-1');
    expect(save.mock.calls[1][0].messages).toHaveLength(4);
  });

  it('persist kapalıyken (çekmece sohbeti) HİÇ yazmaz', async () => {
    vi.useFakeTimers();
    stubChat();
    const save = vi.spyOn(api, 'saveAiConversation').mockResolvedValue(conv());

    await mountProbe(false);
    await act(async () => { await thread.send('bu trace neden yavaş?'); });
    await flushPersist();
    expect(save).not.toHaveBeenCalled();
  });

  it('ASKIDA kalan kaydetme sıradaki soruyu bloklamaz', async () => {
    vi.useFakeTimers();
    const chat = stubChat();
    // Hiç resolve olmayan kaydetme: `await` edilmiş olsaydı sohbet burada
    // ölürdü.
    vi.spyOn(api, 'saveAiConversation')
      .mockImplementation(() => new Promise<AiConversation>(() => {}));

    await mountProbe();
    await act(async () => { await thread.send('ilk soru'); });
    await flushPersist();
    expect(thread.busy).toBe(false);

    await act(async () => { await thread.send('ikinci soru'); });
    expect(chat).toHaveBeenCalledTimes(2);
    expect(text()).toContain('ikinci soru');
  });

  it('kaydetme HATASI sohbeti bozmaz (sonraki tur yeniden dener)', async () => {
    vi.useFakeTimers();
    stubChat();
    const save = vi.spyOn(api, 'saveAiConversation')
      .mockRejectedValueOnce(new Error('503'))
      .mockResolvedValue(conv({ id: 'srv-9' }));

    await mountProbe();
    await act(async () => { await thread.send('ilk soru'); });
    await flushPersist();
    expect(thread.conversationId).toBeNull();

    await act(async () => { await thread.send('ikinci soru'); });
    await flushPersist();
    expect(save).toHaveBeenCalledTimes(2);
    expect(thread.conversationId).toBe('srv-9');
    // Kayıp kendini onarır: ikinci yazım TÜM geçmişi taşır.
    expect(save.mock.calls[1][0].messages).toHaveLength(4);
  });

  it('load turları geri getirir, clear hem turları hem KİMLİĞİ düşürür', async () => {
    vi.useFakeTimers();
    stubChat();
    const save = vi.spyOn(api, 'saveAiConversation').mockResolvedValue(conv({ id: 'srv-1' }));
    vi.spyOn(api, 'aiConversation').mockResolvedValue(conv({
      id: 'arsiv-7',
      messages: [{ role: 'user', text: 'dünkü soru' }, { role: 'assistant', text: 'dünkü cevap' }],
    }));

    await mountProbe();
    await act(async () => { await thread.load('arsiv-7'); });
    expect(text()).toContain('user:dünkü soru');
    expect(text()).toContain('assistant:dünkü cevap');
    expect(thread.conversationId).toBe('arsiv-7');

    // Yüklenen thread'e devam yazımı AYNI kimliğe gider.
    await act(async () => { await thread.send('devam sorusu'); });
    await flushPersist();
    expect(save.mock.calls[0][0].id).toBe('arsiv-7');

    // "Temizle" = yeni konuşma: kimlik düşer, sonraki yazım id'siz.
    await act(async () => { thread.clear(); });
    expect(thread.conversationId).toBeNull();
    expect(text()).not.toContain('dünkü soru');
    await act(async () => { await thread.send('yepyeni soru'); });
    await flushPersist();
    expect(save.mock.calls[1][0].id).toBeUndefined();
    expect(save.mock.calls[1][0].messages).toHaveLength(2);
  });
});

// ── (5): FAB çekmecesindeki "Geçmiş" bölümü.
const THREADS: AiConversationSummary[] = [
  { id: 'c1', title: 'checkout yavaş mı?', updatedAt: Date.now() * 1e6, messages: 4 },
  { id: 'c2', title: 'dünkü deploy etkisi', updatedAt: Date.now() * 1e6 - 3.6e12, messages: 2 },
];

function shell() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <AuthProvider>
          <ConfirmProvider>
            <CopilotChat />
          </ConfirmProvider>
        </AuthProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

// Çekmece `document.body`ye portallanıyor (Drawer.tsx:86), bu yüzden
// sorgular host'a DEĞİL body'ye bakıyor.
const bodyText = () => document.body.textContent ?? '';
const button = (label: string) =>
  Array.from(document.body.querySelectorAll('button'))
    .find(b => b.textContent?.includes(label));

async function openDrawer() {
  await act(async () => { root.render(shell()); });
  // FAB metin taşımıyor (markalı SVG) — sınıfıyla bulunuyor.
  const fab = document.querySelector<HTMLButtonElement>('.cm-ai-fab');
  if (!fab) throw new Error('FAB çizilmedi — copilotConfig stub\'ı çözülmemiş olabilir');
  await act(async () => { fab.click(); });
}

describe('CopilotChat — Geçmiş bölümü', () => {
  beforeEach(() => {
    // jsdom'da Element.scrollTo YOK (mesaj listesinin auto-scroll'u onu
    // çağırıyor). Ürün hatası değil, ortam eksiği.
    (Element.prototype as unknown as { scrollTo: () => void }).scrollTo = () => {};
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: 'gemma4' });
    vi.spyOn(api, 'me').mockResolvedValue({
      id: 'u1', email: 'op@example.com', role: 'admin', firstName: 'Cenk',
    });
    vi.spyOn(api, 'problemsCount').mockResolvedValue({ count: 0 });
    vi.spyOn(api, 'problems').mockResolvedValue({ items: [], total: 0, truncated: false });
  });

  it('çekmece açılışında liste çekilir; başlıklar ve zaman çizilir', async () => {
    const list = vi.spyOn(api, 'aiConversations').mockResolvedValue(THREADS);
    await openDrawer();
    expect(list).toHaveBeenCalledTimes(1);

    await act(async () => { button('Geçmiş')?.click(); });
    expect(bodyText()).toContain('checkout yavaş mı?');
    expect(bodyText()).toContain('dünkü deploy etkisi');
  });

  it('satıra tıklamak konuşmayı EKRANA getirir', async () => {
    vi.spyOn(api, 'aiConversations').mockResolvedValue(THREADS);
    const get = vi.spyOn(api, 'aiConversation').mockResolvedValue(conv({
      id: 'c1', title: 'checkout yavaş mı?',
      messages: [
        { role: 'user', text: 'checkout yavaş mı?' },
        { role: 'assistant', text: 'p95 480ms — db çağrısı' },
      ],
    }));
    await openDrawer();
    await act(async () => { button('Geçmiş')?.click(); });
    await act(async () => { button('checkout yavaş mı?')?.click(); });

    expect(get).toHaveBeenCalledWith('c1');
    expect(bodyText()).toContain('p95 480ms — db çağrısı');
  });

  it('kayıtlı konuşma yokken BOŞ DURUM çizilir (boş panel değil)', async () => {
    vi.spyOn(api, 'aiConversations').mockResolvedValue([]);
    await openDrawer();
    await act(async () => { button('Geçmiş')?.click(); });
    expect(document.body.querySelector('.empty')).toBeTruthy();
    expect(bodyText()).toContain('Kayıtlı konuşma yok');
  });

  it('liste okunamazsa hata çizilir', async () => {
    vi.spyOn(api, 'aiConversations').mockRejectedValue(new Error('boom'));
    await openDrawer();
    await act(async () => { button('Geçmiş')?.click(); });
    expect(bodyText()).toContain('Geçmiş okunamadı');
  });
});
