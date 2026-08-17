// @vitest-environment jsdom
//
// v0.9.1148 (AI Faz 4.2) — balonun MARKDOWN RENDER sözleşmesi.
//
// chatMarkdown.test.ts çözücüyü ölçüyor; buradaki iddiaların hiçbiri saf
// testle ölçülemez:
//
//   (1) BAĞLANMA. `turn.pending` gerçekten çözücüye gidiyor mu? Bayrağı
//       bağlamayı unutmak tsc'yi memnun eder, 27 saf test yeşil kalır ve
//       ekranda tablo her delta'da zıplar. Aynı metin pending=true/false
//       ile İKİ farklı DOM vermek zorunda ("saf test ≠ BAĞLANMA" dersi);
//   (2) XSS. Kaçış disiplininin tablo hücrelerine de UZANDIĞI ancak
//       gerçek DOM'da görülür: `<img onerror>` bir element olarak
//       doğmadı mı;
//   (3) KOD ≠ PROSE. Kod gövdesi mdLite'tan geçmemeli (yıldızlar
//       kalmalı), kopyalanan şey balonun tamamı değil O BLOK olmalı ve
//       yarım blokta kopyala butonu HİÇ olmamalı (eksik komut sessizce
//       panoya gitmesin);
//   (4) AKIŞ İMLECİ metne YAPIŞIK kalmalı. Düz metin koşularını blok
//       öğeye çevirmek (div/p) tipte hiçbir şey bozmaz ama imleci alt
//       satıra atar ve "yazıyor" hissi gider;
//   (5) YÜZEY. Aynı render iki yerde de çalışıyor: FAB penceresi
//       (CopilotChat) ve AI çekmecesi (AIDrawer). İkisi de balonu
//       paylaşıyor, ama paylaşımın DURDUĞU ancak mount'la görülür.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// uPlot modül yüklenirken matchMedia çağırıyor; import zincirinden ÖNCE
// durmalı (emsal: chatThreadPersist.test.tsx, CorePanel.smoke.test.tsx).
vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({
    matches: false, media: q, onchange: null,
    addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
});

// CosreChart STUB'LANIYOR: jsdom'da <canvas>.getContext null döner ve
// uPlot ilk çizimde patlar (CorePanel.smoke.test.tsx'in aynı gerekçesi).
// Burada ölçülen şey grafiğin ÇİZİMİ değil, YÖNLENDİRME: kapanmış+geçerli
// bir ```chart``` fence'i kod bloğuna DEĞİL grafiğe gidiyor mu.
vi.mock('@/components/CosreChart', () => ({
  CosreChart: ({ spec }: { spec: { service: string } }) =>
    <div data-cosre={spec.service}>[grafik]</div>,
}));

import { ChatBubble } from './ChatBubble';
import { AIDrawer } from './AIDrawer';
import { api } from '@/lib/api';
import type { AiConversation, ChatTurn } from '@/lib/types';
import { AuthProvider } from '@/components/AuthProvider';
import { ConfirmProvider } from '@/components/ui/ConfirmDialog';
import { CopilotChat } from '@/components/CopilotChat';

let host: HTMLDivElement;
let root: Root;

const q = <T extends Element>(sel: string) => host.querySelector<T>(sel);
const qa = (sel: string) => Array.from(host.querySelectorAll(sel));
const text = () => host.textContent ?? '';
const copyBtn = () =>
  qa('button').find(b => (b as HTMLElement).getAttribute('aria-label') === 'Kod bloğunu kopyala') as
    HTMLButtonElement | undefined;

async function mount(turn: ChatTurn) {
  await act(async () => {
    root.render(
      <MemoryRouter>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <ChatBubble turn={turn} />
        </QueryClientProvider>
      </MemoryRouter>
    );
  });
}

const asst = (t: string, over: Partial<ChatTurn> = {}): ChatTurn =>
  ({ role: 'assistant', text: t, ...over });

const TID = 'a1b2c3d4e5f60718293a4b5c6d7e8f90';

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

describe('tablo', () => {
  const TBL = 'Yavaşlar:\n\n| Servis | p95 |\n|---|---:|\n| checkout | 480 |\n| payment | 1200 |\n';

  it('markdown tablosu GERÇEK tablo olarak çiziliyor', async () => {
    await mount(asst(TBL));
    const t = q<HTMLTableElement>('table.cm-md-table');
    expect(t).toBeTruthy();
    expect(qa('table.cm-md-table thead th').map(e => e.textContent)).toEqual(['Servis', 'p95']);
    expect(qa('table.cm-md-table tbody tr').length).toBe(2);
    expect(qa('table.cm-md-table tbody tr')[1].textContent).toBe('payment1200');
    // Ham çubuklar ekranda KALMADI — kusurun ta kendisi buydu.
    expect(text()).not.toContain('|---');
    expect(text()).not.toContain('| checkout |');
  });

  it('yatay taşma TABLONUN kendi kabında (sayfa gövdesi yana kaymaz)', async () => {
    await mount(asst(TBL));
    // `.table-wrap` = paylaşılan kaydırma kabı (overflow-x: auto).
    const wrap = q('.table-wrap.cm-md-tw');
    expect(wrap).toBeTruthy();
    expect(wrap!.querySelector('table')).toBeTruthy();
  });

  it('sağa hizalı kolon `.num` alıyor (sayısal kolon = tabular-nums)', async () => {
    await mount(asst(TBL));
    expect(qa('table.cm-md-table thead th')[1].className).toBe('num');
    expect(qa('table.cm-md-table tbody td')[1].className).toBe('num');
    expect(qa('table.cm-md-table tbody td')[0].className).toBe('');
  });

  // Ev kısıtı: 100'den fazla satır → content-visibility. Model tool
  // sonucundan uzun tablo yazabiliyor ve o balon sayfanın en pahalı
  // düğümü olur.
  it('100+ satırlı tabloda satırlar content-visibility alıyor', async () => {
    const rows = (n: number) => Array.from({ length: n }, (_, i) => `| s${i} | ${i} |`).join('\n');
    await mount(asst(`| Servis | p95 |\n|---|---|\n${rows(101)}\n`));
    const trs = qa('table.cm-md-table tbody tr');
    expect(trs.length).toBe(101);
    expect(trs[0].getAttribute('style') ?? '').toContain('content-visibility');
    // Küçük tabloya bedel ödenmiyor.
    await mount(asst(`| Servis | p95 |\n|---|---|\n${rows(3)}\n`));
    expect(qa('table.cm-md-table tbody tr')[0].getAttribute('style') ?? '')
      .not.toContain('content-visibility');
  });

  it('hücrede satır içi işaretleme + trace linki çalışıyor', async () => {
    await mount(asst(`| Servis | Kanıt |\n|---|---|\n| **checkout** | \`db.query\` ${TID} |\n`));
    expect(q('table b')?.textContent).toBe('checkout');
    expect(q('table code')?.textContent).toBe('db.query');
    const a = q<HTMLAnchorElement>('table a[data-nav]');
    expect(a?.getAttribute('href')).toBe(`/trace?id=${TID}`);
  });

  it('hücredeki HTML enjeksiyonu element DOĞURMUYOR', async () => {
    await mount(asst('| a | b |\n|---|---|\n| <img src=x onerror=alert(1)> | <b>x</b> |\n'));
    expect(q('table img')).toBeNull();
    // <b> de metin olarak kaldı: escape mdLite'ın İLK adımı.
    expect(qa('table b').length).toBe(0);
    expect(text()).toContain('<img src=x onerror=alert(1)>');
  });
});

describe('kod bloğu', () => {
  const SQL = 'şu sorgu:\n```sql\nSELECT count() FROM spans\nWHERE service = \'a\'\n```\n';

  it('fence → <pre> + dil etiketi + kopyala', async () => {
    await mount(asst(SQL));
    expect(q('.cm-md-code pre')?.textContent).toBe("SELECT count() FROM spans\nWHERE service = 'a'");
    expect(q('.cm-md-code-lang')?.textContent).toBe('sql');
    expect(copyBtn()).toBeTruthy();
    // Çıplak ``` çiti ekranda kalmadı.
    expect(text()).not.toContain('```');
  });

  it('kod gövdesi mdLite\'tan GEÇMİYOR (yıldız/backtick literal)', async () => {
    await mount(asst('```\n**not bold** ve `not code`\n```\n'));
    expect(q('.cm-md-code pre')?.textContent).toBe('**not bold** ve `not code`');
    expect(q('.cm-md-code pre b')).toBeNull();
    expect(q('.cm-md-code pre code')).toBeNull();
  });

  it('kopyala BLOĞU kopyalıyor (balonun tamamını değil)', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    await mount(asst(SQL));
    await act(async () => { copyBtn()!.click(); });
    expect(writeText).toHaveBeenCalledWith("SELECT count() FROM spans\nWHERE service = 'a'");
    expect(copyBtn()!.textContent).toBe('✓');
  });

  // ÜRÜN KARARI: kopyala butonu KAPANMIŞ çite bağlı, akış durumuna değil.
  // Kesilmiş bir cevabın kod bloğu da yarımdır — panoya gitmesi
  // operatörün eksik bir komutu çalıştırması demek. Butonun yokluğu
  // sessiz kalmasın diye şerit durumu YAZIYOR.
  it('BAĞLANMA: yarım blokta kopyala YOK; şerit akarken "yazılıyor", kesildiyse "kesildi"', async () => {
    const half = 'şu sorgu:\n```sql\nSELECT count() FROM spans';
    await mount(asst(half, { pending: true }));
    expect(q('.cm-md-code pre')?.textContent).toBe('SELECT count() FROM spans');
    expect(copyBtn()).toBeUndefined();
    expect(q('.cm-md-code-st')?.textContent).toBe('yazılıyor…');

    // Akış bitti, çit hiç gelmedi: içerik YUTULMAZ ama "yazılıyor" demek
    // yalan olur. Aynı metin, farklı bayrak → farklı şerit (bağlanma).
    await mount(asst(half));
    expect(q('.cm-md-code pre')?.textContent).toBe('SELECT count() FROM spans');
    expect(copyBtn()).toBeUndefined();
    expect(q('.cm-md-code-st')?.textContent).toBe('kesildi');

    // Çit kapanınca kopyala AÇILIR (akış sürerken bile).
    await mount(asst(half + '\n```\n', { pending: true }));
    expect(copyBtn()).toBeTruthy();
    expect(q('.cm-md-code-st')).toBeNull();
  });
});

describe('başlık ve liste', () => {
  it('# → h4/h5/h6, - → <ul>, 1. → <ol>; ham işaretler kalmıyor', async () => {
    await mount(asst('## Kök neden\n- db havuzu doldu\n- retry fırtınası\n\n1. havuzu büyüt\n'));
    expect(q('h5.cm-md-h')?.textContent).toBe('Kök neden');
    expect(qa('ul.cm-md-list li').map(e => e.textContent)).toEqual(['db havuzu doldu', 'retry fırtınası']);
    expect(qa('ol.cm-md-list li').map(e => e.textContent)).toEqual(['havuzu büyüt']);
    expect(text()).not.toContain('## ');
    expect(text()).not.toContain('- db');
  });
});

describe('akış', () => {
  it('BAĞLANMA: yarım tablo satırı akarken çizilmez, tamamlanınca çizilir', async () => {
    const half = '| Servis | p95 |\n|---|---|\n| checkout | 480 |\n| payme';
    await mount(asst(half, { pending: true }));
    expect(qa('table tbody tr').length).toBe(1);
    expect(text()).not.toContain('payme');
    await mount(asst(half + 'nt | 1200 |\n', { pending: true }));
    expect(qa('table tbody tr').length).toBe(2);
    expect(text()).toContain('payment');
  });

  it('imleç düz metne YAPIŞIK (metin koşusu satır içi span kalıyor)', async () => {
    await mount(asst('checkout p95 480ms ve yükseli', { pending: true }));
    const cur = q('.cm-ai-cursor')!;
    expect(cur).toBeTruthy();
    // İmlecin hemen SOLUNDA metni taşıyan span durmalı; blok bir öğe
    // (div/p/table) imleci alt satıra atardı.
    expect(cur.previousElementSibling?.tagName).toBe('SPAN');
    expect(cur.previousElementSibling?.textContent).toBe('checkout p95 480ms ve yükseli');
  });
});

describe('chart fence', () => {
  const spec = '{"service":"checkout","agg":"p95"}';

  it('kapanmış + geçerli spec → grafik (kod bloğu DEĞİL)', async () => {
    await mount(asst(`p95 şöyle:\n\`\`\`chart\n${spec}\n\`\`\`\n`));
    expect(q('[data-cosre="checkout"]')).toBeTruthy();
    expect(q('.cm-md-code')).toBeNull();
    expect(text()).not.toContain('"service"');
  });

  it('akarken ham JSON DÖKMÜYOR — bekleme satırı', async () => {
    await mount(asst(`p95 şöyle:\n\`\`\`chart\n{"service":"chec`, { pending: true }));
    expect(text()).not.toContain('"service"');
    expect(q('.cm-md-wait')).toBeTruthy();
  });

  it('akış kesildiyse (kapanmamış) JSON kod bloğunda görünür — yutulmaz', async () => {
    await mount(asst(`\`\`\`chart\n${spec}`));
    expect(q('.cm-md-code pre')?.textContent).toBe(spec);
  });

  it('kapanmış ama BOZUK spec atlanıyor (v0.9.183 kararı korundu)', async () => {
    await mount(asst('```chart\n{bozuk\n```\n'));
    expect(q('[data-cosre]')).toBeNull();
    expect(q('.cm-md-code')).toBeNull();
    expect(text()).not.toContain('bozuk');
  });
});

// ── YÜZEY (5): FAB penceresi gerçek mount ile ────────────────────────
describe('CopilotChat (FAB) yüzeyi', () => {
  const bodyEl = (sel: string) => document.body.querySelector(sel);
  const bodyButton = (label: string) =>
    Array.from(document.body.querySelectorAll('button')).find(b => b.textContent?.includes(label));

  beforeEach(() => {
    (Element.prototype as unknown as { scrollTo: () => void }).scrollTo = () => {};
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: 'gemma4' });
    vi.spyOn(api, 'me').mockResolvedValue({ id: 'u1', email: 'op@x.io', role: 'admin', firstName: 'Cenk' });
    vi.spyOn(api, 'problemsCount').mockResolvedValue({ count: 0 });
    vi.spyOn(api, 'problems').mockResolvedValue({ items: [], total: 0, truncated: false });
    vi.spyOn(api, 'aiConversations').mockResolvedValue([
      { id: 'c1', title: 'yavaş servisler', updatedAt: Date.now() * 1e6, messages: 2 },
    ]);
    vi.spyOn(api, 'aiConversation').mockResolvedValue({
      id: 'c1', title: 'yavaş servisler', updatedAt: Date.now() * 1e6,
      messages: [
        { role: 'user', text: 'hangi servisler yavaş?' },
        { role: 'assistant', text: '| Servis | p95 |\n|---|---:|\n| checkout | 480 |\n' },
      ],
    } as AiConversation);
  });

  it('arşivden gelen tablo cevabı FAB penceresinde tablo olarak çiziliyor', async () => {
    await act(async () => {
      root.render(
        <MemoryRouter>
          <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
            <AuthProvider><ConfirmProvider><CopilotChat /></ConfirmProvider></AuthProvider>
          </QueryClientProvider>
        </MemoryRouter>
      );
    });
    const fab = document.querySelector<HTMLButtonElement>('.cm-ai-fab');
    expect(fab, 'FAB çizilmedi').toBeTruthy();
    await act(async () => { fab!.click(); });
    await act(async () => { bodyButton('Geçmiş')?.click(); });
    await act(async () => { bodyButton('yavaş servisler')?.click(); });

    expect(bodyEl('table.cm-md-table')).toBeTruthy();
    expect(document.body.textContent).not.toContain('|---');
  });
});

// ── YÜZEY (5): AI çekmecesi gerçek mount ile ─────────────────────────
//
// İki yüzeyin AYRI ölçülmesinin sebebi tarih: v0.9.479'a dek sohbetin
// İKİ implementasyonu vardı ve çekmece geride kalıyordu. Balon
// paylaşıldığı sürece ikisi eşit — "paylaşıldığı sürece" kısmı ancak
// mount'la doğrulanır.
describe('AIDrawer (çekmece) yüzeyi', () => {
  it('çekmece sohbetinde tablo cevabı tablo olarak çiziliyor', async () => {
    (Element.prototype as unknown as { scrollTo: () => void }).scrollTo = () => {};
    (Element.prototype as unknown as { scrollIntoView: () => void }).scrollIntoView = () => {};
    vi.spyOn(api, 'copilotConfig').mockResolvedValue({ enabled: true, model: 'gemma4' });
    vi.spyOn(api, 'copilotExplainProblem').mockResolvedValue({
      explanation: 'kök neden: db havuzu doldu', exchangeId: 'e1',
    });
    vi.spyOn(api, 'copilotChat').mockImplementation(async (_msgs, onEvent) => {
      onEvent({ kind: 'answer', text: '| Servis | p95 |\n|---|---:|\n| checkout | 480 |\n', exchangeId: 'x2' });
      onEvent({ kind: 'done', ok: true });
    });

    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={['/problems?ai=problem:P-1']}>
          <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
            <AIDrawer />
          </QueryClientProvider>
        </MemoryRouter>
      );
    });
    // Çekmece body'ye portallanıyor (Drawer.tsx).
    const btn = (label: string) =>
      Array.from(document.body.querySelectorAll('button')).find(b => b.textContent?.includes(label));
    expect(document.body.textContent, 'explain çizilmedi').toContain('kök neden');

    await act(async () => { btn("Chat'te devam et")?.click(); });
    // İlk takip sorusu çipi (↳ …) — sohbeti başlatır.
    const chip = Array.from(document.body.querySelectorAll('button'))
      .find(b => b.textContent?.startsWith('↳'));
    expect(chip, 'takip çipi yok').toBeTruthy();
    await act(async () => { chip!.click(); });

    expect(document.body.querySelector('table.cm-md-table')).toBeTruthy();
    expect(document.body.textContent).not.toContain('|---');
  });
});

// ── YÜZEY KAPISI: paylaşım DURUYOR mu + XSS yüzeyi TEK mi ────────────
describe('markdown yolu TEK', () => {
  const read = (rel: string) =>
    readFileSync(resolve(__dirname, rel), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

  for (const rel of ['./AIDrawer.tsx', '../CopilotChat.tsx']) {
    const name = rel.split('/').pop();
    it(`${name} — turları ChatBubble ile çiziyor, kendi HTML'ini basmıyor`, () => {
      const src = read(rel);
      expect(src).toContain('<ChatBubble');
      expect(src).not.toContain('dangerouslySetInnerHTML');
      // Kendi mdLite/renderMessage kopyası da olmasın (ikinci yazılış =
      // kapının kör kaldığı yer, v0.9.696 dersi).
      expect(src).not.toMatch(/function\s+mdLite|function\s+renderMessage/);
    });
  }

  it('ChatBubble: innerHTML TEK yerde ve mdLite besliyor', () => {
    const src = read('./ChatBubble.tsx');
    const hits = src.match(/dangerouslySetInnerHTML/g) ?? [];
    expect(hits.length, 'yeni bir HTML basım yüzeyi açılmış').toBe(1);
    expect(src).toContain('dangerouslySetInnerHTML={{ __html: mdLite(text) }}');
    // Kod gövdesi ASLA innerHTML'e gitmez: React çocuğu olarak basılır.
    expect(src).toContain('<pre>{code}</pre>');
  });
});
