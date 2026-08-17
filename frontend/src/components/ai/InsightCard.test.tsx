// @vitest-environment jsdom
//
// v0.9.1130 (AI Faz 2.2) — InsightCard'ın RENDER sözleşmesi.
//
// NEDEN GERÇEK MOUNT: iddiaların hepsi çalışma zamanı dalı, kaynak
// taramasıyla ölçülemez —
//   (1) SINYALLER prose'tan ÖNCE ekranda. Kartın var olma sebebi bu:
//       deterministik yarı modeli beklemez. Bir refactor sinyalleri
//       cevabın altına taşırsa kart "AI yavaşsa boş" hâline döner ve
//       hiçbir tip hatası vermez;
//   (2) AI KAPALI hâlde kart KÜÇÜLMEZ — sinyaller+pivotlar tam, anlatı
//       yerine tek dürüst satır. Bu, ürün kararının kendisi;
//   (3) 👍/👎 kapısı KİMLİK: aiOff cevabı exchangeId taşımaz, atom kendini
//       gizler (v0.9.592'nin ölü-affordance dersi);
//   (4) hata anlatıyı düşürür ama KANITI düşürmez — ve yarım anlatı
//       silinir (yarım bir kök-neden cümlesi tam sanılırsa operatör
//       yanlış yere koşar);
//   (5) re-render yeniden ÜRETMEZ (LLM + ES maliyeti), unmount akışı KESER.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { InsightCard } from './InsightCard';
import { api, type InsightStreamOpts } from '@/lib/api';
import type { InsightResponse } from '@/lib/types';

let host: HTMLDivElement;
let root: Root;

const text = () => host.textContent ?? '';
const cursor = () => host.querySelector('.cm-ai-cursor');
const badges = () => Array.from(host.querySelectorAll('.badge'));
const feedback = () => Array.from(host.querySelectorAll('button[aria-pressed]'));
const anchors = () => Array.from(host.querySelectorAll('a'));
const button = (label: string) =>
  Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes(label));

/** Sunucunun deterministik yarısı — `signals` çerçevesinin gövdesi. */
const SIGNALS: Partial<InsightResponse> = {
  prose: '',
  signals: [
    { kind: 'exception', label: 'Oluşum', value: '1.240 · son 24s 320', severity: 'err' },
    { kind: 'deploy', label: 'Yakın deploy', value: 'v2.4.1 · 11dk önce', severity: 'warn' },
    { kind: 'generic', label: 'Servis', value: 'payment-service' },
  ],
  links: [
    { label: 'Loglar (error+)', href: '/logs?service=payment&severity=17' },
    { label: 'Servis', href: '/service?name=payment' },
  ],
  exchangeId: 'x-signals',
  model: 'gemma4',
};

const full = (over: Partial<InsightResponse>): InsightResponse =>
  ({ prose: '', signals: [], links: [], ...over });

/**
 * fakeInsight — akışı testin sürdüğü sahte uç. Çağrılar AYRI AYRI
 * tutuluyor: "Yeniden üret" ikinci bir çağrı başlatıyor ve sorunun yarısı
 * tam olarak BİRİNCİSİNE ne olduğu (tek slot tutan bir sahte, iptal
 * edilmemiş bir akışı ölçüp yeşil dönerdi).
 */
function fakeInsight() {
  const seen: Array<{
    signals: (r?: Partial<InsightResponse>) => Promise<void>;
    emit: (t: string) => Promise<void>;
    finish: (r: Partial<InsightResponse>) => Promise<void>;
    fail: (e: unknown) => Promise<void>;
    aborted: () => boolean;
  }> = [];
  vi.spyOn(api, 'insight').mockImplementation(
    (_kind, _id, opts?: InsightStreamOpts) => {
      let resolve!: (v: InsightResponse) => void;
      let reject!: (e: unknown) => void;
      const p = new Promise<InsightResponse>((res, rej) => { resolve = res; reject = rej; });
      seen.push({
        signals: async (r = SIGNALS) => {
          await act(async () => { opts?.onSignals?.(full(r)); });
        },
        emit: async t => { await act(async () => { opts?.onDelta?.(t); }); },
        finish: async r => { await act(async () => { resolve(full({ ...SIGNALS, ...r })); }); },
        fail: async e => { reject(e); await act(async () => { await p.catch(() => {}); }); },
        aborted: () => opts?.signal?.aborted ?? false,
      });
      return p;
    });
  return {
    call: (i = 0) => seen[i],
    count: () => seen.length,
    last: () => seen[seen.length - 1],
  };
}

async function mount(node: React.ReactNode) {
  await act(async () => {
    root.render(<MemoryRouter initialEntries={['/exceptions']}>{node}</MemoryRouter>);
  });
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

describe('InsightCard — deterministik yarı ÖNCE', () => {
  it('signals çerçevesi tek başına kartı DOLDURUR (prose beklemez)', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);

    // İlk çerçeveden önce: yalnız yükleniyor.
    expect(text()).toContain('Sinyaller toplanıyor');

    await f.call(0).signals();

    // Sinyaller VE pivotlar ekranda…
    expect(text()).toContain('1.240 · son 24s 320');
    expect(text()).toContain('v2.4.1 · 11dk önce');
    expect(text()).toContain('payment-service');
    expect(text()).toContain('Loglar (error+)');
    // …anlatı ise HENÜZ YOK: yuvada "düşünüyor" duruyor.
    expect(text()).toContain('CoSRE düşünüyor');
    expect(cursor()).toBeNull();
  });

  it('şiddet paylaşılan badge tonlarına düşer, nötr sinyal rozetsiz kalır', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();

    const classes = badges().map(b => b.className);
    expect(classes).toContain('badge b-err');
    expect(classes).toContain('badge b-warn');
    // Üç sinyalden yalnız ikisi şiddet taşıyor; 'Servis' düz metin.
    expect(badges()).toHaveLength(2);
  });

  it('pivot href\'leri AYNEN çizilir (sunucu penceresi korunur)', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();

    const hrefs = anchors().map(a => a.getAttribute('href'));
    expect(hrefs).toContain('/logs?service=payment&severity=17');
    expect(hrefs).toContain('/service?name=payment');
  });

  it('model çipi cevaptan gelir', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="problem" id="p1" />);
    await f.call(0).signals();
    expect(host.querySelector('.chip')?.textContent).toContain('gemma4');
  });
});

describe('InsightCard — akan anlatı', () => {
  it('delta geldikçe metin BÜYÜR ve imleç görünür', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();

    await f.call(0).emit('Havuz ');
    expect(text()).toContain('Havuz');
    expect(text()).not.toContain('CoSRE düşünüyor');
    expect(cursor()).not.toBeNull();

    await f.call(0).emit('doygunluğu.');
    expect(text()).toContain('Havuz doygunluğu.');
  });

  it('answer biriken metni EZER, imleç kapanır', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await f.call(0).emit('yarım anlatı');
    await f.call(0).finish({ prose: 'tam ve nihai anlatı', exchangeId: 'x-final' });

    expect(text()).toContain('tam ve nihai anlatı');
    expect(text()).not.toContain('yarım anlatı');
    expect(cursor()).toBeNull();
  });

  it('👍/👎 yalnız cevap TAMAMLANINCA çıkar', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await f.call(0).emit('akıyor…');
    // Sinyal çerçevesi kimlik TAŞISA da oy düğmesi yok: akmakta olan bir
    // cevabı oylamak, okunmamış metne not vermek olurdu.
    expect(feedback()).toHaveLength(0);

    await f.call(0).finish({ prose: 'bitti', exchangeId: 'x-final' });
    expect(feedback()).toHaveLength(2);
  });

  it('sıfır delta (buffered geri düşüşü) yine de anlatıyı çizer', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="problem" id="p1" />);
    await f.call(0).finish({ prose: 'tek parça anlatı', exchangeId: 'x1' });
    expect(text()).toContain('tek parça anlatı');
    expect(cursor()).toBeNull();
  });

  it('boş model cevabı dürüstçe söylenir, sinyaller durur', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="problem" id="p1" />);
    await f.call(0).finish({ prose: '' });
    expect(text()).toContain('Model boş yanıt döndürdü');
    expect(text()).toContain('1.240 · son 24s 320');
  });
});

describe('InsightCard — AI kapalı', () => {
  it('dürüst not çizilir; sinyaller ve pivotlar TAM kalır', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).finish({ aiOff: true, prose: '', exchangeId: undefined });

    expect(text()).toContain('AI yapılandırılmamış');
    // Kart KÜÇÜLMEZ: deterministik yarı aynen orada.
    expect(text()).toContain('1.240 · son 24s 320');
    expect(text()).toContain('Loglar (error+)');
    expect(badges()).toHaveLength(2);
    // Anlatı yuvasında "düşünüyor" da imleç de YOK.
    expect(text()).not.toContain('CoSRE düşünüyor');
    expect(cursor()).toBeNull();
  });

  it('kimlik yok → 👍/👎 HİÇ çizilmez (kapı aiOff dalı değil, KİMLİK)', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).finish({ aiOff: true, prose: '', exchangeId: undefined });
    expect(feedback()).toHaveLength(0);
  });

  it('Chat\'te devam et gizlenir (sohbet de yapılandırılmamış)', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).finish({ aiOff: true, prose: '', exchangeId: undefined });
    expect(button('Chat\'te devam et')).toBeUndefined();
    // "Yeniden üret" DURUYOR: sağlayıcı sonradan tanımlanabilir ve
    // deterministik yarı da tazelenebilir.
    expect(button('Yeniden üret')).toBeDefined();
  });
});

describe('InsightCard — hata', () => {
  it('sinyaller geldikten SONRAKİ hata kanıtı düşürmez, yarım anlatıyı siler', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await f.call(0).emit('yarım kök neden');
    await f.call(0).fail(new Error('model kotası doldu'));

    expect(text()).toContain('model kotası doldu');
    expect(text()).not.toContain('yarım kök neden');
    expect(text()).toContain('1.240 · son 24s 320');   // kanıt yerinde
    expect(text()).toContain('Loglar (error+)');
    expect(cursor()).toBeNull();
  });

  it('ilk çerçeveden ÖNCE patlayan istek Empty + yeniden dene gösterir', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="yok" />);
    await f.call(0).fail(new Error('HTTP 404: exception group not found'));

    expect(text()).toContain('Insight üretilemedi');
    expect(text()).toContain('404');
    const retry = button('Yeniden dene');
    expect(retry).toBeDefined();
    await act(async () => { retry!.click(); });
    expect(f.count()).toBe(2);
  });

  it('kanıtsız cevap boş panel değil Empty çizer', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="problem" id="p1" />);
    await f.call(0).finish({ prose: '', signals: [], links: [], exchangeId: undefined });
    expect(text()).toContain('kanıt bulunamadı');
  });
});

describe('InsightCard — maliyet disiplini', () => {
  it('re-render YENİDEN üretmez', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    expect(f.count()).toBe(1);
  });

  it('özne DEĞİŞİRSE yeni üretim çalışır', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await mount(<InsightCard kind="exception" id="baska" />);
    expect(f.count()).toBe(2);
  });

  it('unmount uçuştaki akışı KESER', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await f.call(0).emit('akıyor');
    expect(f.call(0).aborted()).toBe(false);

    await act(async () => { root.render(<MemoryRouter />); });
    expect(f.call(0).aborted()).toBe(true);
  });

  it('Yeniden üret öncekini İPTAL eder, oyu sıfırlar, KANITI korur', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="exception" id="a3f19c" />);
    await f.call(0).signals();
    await f.call(0).finish({ prose: 'eski anlatı', exchangeId: 'x-eski' });
    expect(feedback()).toHaveLength(2);

    await act(async () => { button('Yeniden üret')!.click(); });

    expect(f.call(0).aborted()).toBe(true);   // birinci akış kesildi
    expect(f.count()).toBe(2);                // ve ikincisi başladı
    expect(text()).not.toContain('eski anlatı');
    expect(feedback()).toHaveLength(0);       // eski oy yeni cevabın üstünde kalamaz
    // Deterministik yarı SİLİNMEZ: aynı veri yeniden gelecek, kartı bir
    // kare boyunca çökertmek "kanıt kayboldu" der.
    expect(text()).toContain('1.240 · son 24s 320');
    expect(text()).toContain('CoSRE düşünüyor');

    await f.call(1).finish({ prose: 'yeni anlatı', exchangeId: 'x-yeni' });
    expect(text()).toContain('yeni anlatı');
  });
});

describe('InsightCard — köprüler', () => {
  it('Chat\'te devam et konuya uygun soruyla `coremetry:ai-ask` atar', async () => {
    const f = fakeInsight();
    const seen: string[] = [];
    const h = (e: Event) => {
      const q = (e as CustomEvent<{ question?: string }>).detail?.question;
      if (q) seen.push(q);
    };
    window.addEventListener('coremetry:ai-ask', h);
    try {
      await mount(<InsightCard kind="problem" id="p1" />);
      await f.call(0).finish({ prose: 'anlatı', exchangeId: 'x1' });
      await act(async () => { button('Chat\'te devam et')!.click(); });
      expect(seen).toEqual(['Bu problemin kök nedeni ne? (problem p1)']);
    } finally {
      window.removeEventListener('coremetry:ai-ask', h);
    }
  });

  it('onClose verilirse kapat düğmesi çizilir', async () => {
    const f = fakeInsight();
    const closed: number[] = [];
    await mount(<InsightCard kind="problem" id="p1" onClose={() => closed.push(1)} />);
    await f.call(0).signals();
    await act(async () => { button('kapat')!.click(); });
    expect(closed).toHaveLength(1);
  });

  it('onClose YOKSA kapat düğmesi de yok (host satırı kapatıyor olabilir)', async () => {
    const f = fakeInsight();
    await mount(<InsightCard kind="problem" id="p1" />);
    await f.call(0).signals();
    expect(button('kapat')).toBeUndefined();
  });
});
