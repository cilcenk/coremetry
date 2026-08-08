// @vitest-environment jsdom
//
// CorePanel RENDER duman testi (v0.9.793) — deponun İLK bileşen testi.
//
// Bugüne dek CorePanel'in tek koruması KAYNAK TARAMASIydı
// (corePanelContracts.test.ts): "şu satır dosyada duruyor mu?". O kapılar
// altı kez işe yaradı ama bir şeyi hiç görmediler — bileşenin gerçekten
// MOUNT olup olmadığını. Bir import hatası, bir hook sırası ihlali, bir
// null-deref: hepsi tarayıcıda beyaz ekran, testte yeşil.
//
// KAPSAM, DÜRÜSTÇE: burada ÇİZİM test EDİLMİYOR. @grafana/ui'nin UPlotChart'ı
// vi.mock ile stub'lanıyor çünkü jsdom'da <canvas>.getContext("2d") null
// döner ve uPlot ilk çizimde patlar (canvas paketi bir devDependency olarak
// eklenmeye değmez — çizimin doğruluğunu zaten piksel testiyle koruyamayız).
// Test edilen: (a) dört PanelData durumunun (loading/error/empty/ready)
// render çıktısı, (b) lejant katmanı — satır sayısı + hücre birimleri,
// (c) config'in gerçekten KURULABİLİYOR olması — stub, builder'ın
// getConfig()'ini çağırır, yani addScale/addAxis/addSeries/addBand/hook
// zinciri patlarsa test kızarır. Yani "çizilen doğru mu" değil, "çizmeye
// giden her şey ayakta mı".

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { spanSeriesToFrames } from '@/lib/chart/dataFrame';

// jsdom'da window.matchMedia YOK ve uPlot onu MODÜL YÜKLENİRKEN çağırıyor
// (setPxRatio, uPlot.cjs.js:80) — yani import zincirinden önce durmalı.
// vi.hoisted, vi.mock'la birlikte dosyanın en tepesine kaldırılır; normal bir
// beforeAll ÇOK GEÇ olurdu (importlar çoktan patlamış olurdu).
// Stub'ın CorePanel'e verdiği SAHTE uPlot örneği testlere buradan sızar.
// vi.hoisted, vi.mock factory'sinin okuyabileceği tek dış bağlam
// (factory hoist edilir, normal bir `const` ondan sonra tanımlanır).
interface FakePlot {
  series: { show?: boolean; alpha?: number; width?: number }[];
  cursor: { idx?: number | null };
  redraws: number;
  redraw: (rebuild?: boolean) => void;
  setSeries: () => void;
}
const holder = vi.hoisted(() => ({ plot: null as null | {
  series: { show?: boolean; alpha?: number; width?: number }[];
  cursor: { idx?: number | null };
  redraws: number;
  redraw: (rebuild?: boolean) => void;
  setSeries: () => void;
} }));

vi.hoisted(() => {
  window.matchMedia = ((q: string) => ({
    matches: false, media: q, onchange: null,
    addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
  // Aynı gerekçe localStorage için: @grafana/ui'nin logger'ı MODÜL
  // SEVİYESİNDE getItem çağırıyor. Node 22'nin deneysel yerleşik
  // localStorage'ı jsdom'unkini gölgeliyor ve metotları çalışmıyor
  // (--localstorage-file uyarısı) → bellek-içi bir uygulama koyuyoruz.
  const mem = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      getItem: (k: string) => (mem.has(k) ? mem.get(k)! : null),
      setItem: (k: string, v: string) => { mem.set(k, String(v)); },
      removeItem: (k: string) => { mem.delete(k); },
      clear: () => mem.clear(),
      key: (i: number) => Array.from(mem.keys())[i] ?? null,
      get length() { return mem.size; },
    },
  });
});

// UPlotChart stub — config'i GERÇEKTEN kurar (patlarsa test kızarır) ve
// sonucu DOM'a nitelik olarak yazar. Builder'ın kendisi (UPlotConfigBuilder,
// enum'lar) ORİJİNAL kalır: mock'lasaydık test kendi kendini onaylardı.
vi.mock('@grafana/ui', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    UPlotChart: ({ config, width, height, plotRef }: {
      config: { getConfig: () => {
        series?: unknown[]; bands?: unknown[];
        axes?: { scale?: string; size?: unknown }[];
        scales?: Record<string, { range?: unknown } | undefined>;
        cursor?: { points?: { size?: unknown; width?: unknown; show?: unknown } };
      } };
      width: number; height: number;
      plotRef?: (u: unknown) => void;
    }) => {
      const cfg = config.getConfig();
      const n = (cfg.series ?? []).length;
      // v0.9.799 — eksen oluğu + imleç noktası KURULAN config'ten okunur:
      // kaynak taraması "satır duruyor mu" der, bu "builder gerçekten
      // üretti mi" der (iki kapı ayrı sınıfları yakalar).
      const yAxis = (cfg.axes ?? []).find(a => a.scale === 'y');
      const pts = cfg.cursor?.points ?? {};
      // v0.9.811 — y ÖLÇEĞİNİN GERÇEK aralık fonksiyonu çağrılır. Kaynak
      // taraması "softMin: 0 satırı duruyor" der; bu, @grafana/ui'nin o
      // prop'u uPlot'un soft-mode'una çevirdiğini ve uPlot'un TABANI
      // gerçekten 0'a çektiğini ölçer. İki ayrı sınıf: birincisi düzeltmenin
      // yazıldığını, ikincisi ÇALIŞTIĞINI korur (@grafana/ui majör göçünde
      // softMin semantiği kayarsa yalnız bu kapı kızarır).
      // Sahte uPlot: rangeFn yalnız u.scales[key].distr okuyor (1 = Linear).
      const yProbe = (dataMin: number, dataMax: number): string => {
        const r = cfg.scales?.y?.range;
        if (typeof r !== 'function') return 'norange';
        const out = (r as (u: unknown, a: number, b: number, k: string) => number[])(
          { scales: { y: { distr: 1 } } }, dataMin, dataMax, 'y');
        return out.join(',');
      };
      // SAHTE uPlot örneği: yalnız CorePanel'in dokunduğu yüzey (series[],
      // cursor, setSeries, redraw). Odak efektinin ALPHA/GENİŞLİK yazdığını
      // gerçekten ölçebilmemizi sağlar — stub'sız bir testte plotRef hiç
      // dolmaz ve efekt sessizce hiçbir şey yapmazdı.
      if (!holder.plot || holder.plot.series.length !== n) {
        holder.plot = {
          series: Array.from({ length: n }, (_, i) =>
            (i === 0 ? {} : { show: true, alpha: 1, width: 1.5 })),
          cursor: { idx: null },
          redraws: 0,
          redraw() { this.redraws++; },
          setSeries() {},
        };
      }
      plotRef?.(holder.plot);
      return (
        <div data-testid="uplot"
          data-series={String(n)}
          data-bands={String((cfg.bands ?? []).length)}
          data-yaxis-size={String(yAxis?.size ?? '')}
          data-cursor-pt={`${String(pts.size ?? '')}/${String(pts.width ?? '')}/${String(pts.show ?? '')}`}
          // Yüksek ve dar bir veri aralığı: taban kaymasının en görünür
          // olduğu şekil (1200-1260 arası gezen bir seri).
          data-yrange-pos={yProbe(1200, 1260)}
          // Negatif taşıyan seri: soft taban UYGULANMAMALI, yoksa veri kırpılır.
          data-yrange-neg={yProbe(-40, -10)}
          data-w={String(width)} data-h={String(height)} />
      );
    },
  };
});

// Import AFTER the mock factory is registered (vi.mock hoists, ama okuyucu
// için sıra anlamlı: CorePanel stub'lı @grafana/ui ile yüklenir).
const { CorePanel } = await import('./CorePanel');

beforeAll(() => {
  // jsdom'da yok — CorePanel genişliği ResizeObserver'dan öğrenir.
  class RO {
    constructor(private cb: () => void) {}
    observe() { this.cb(); }
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver = RO as unknown as typeof ResizeObserver;
  // jsdom layout yapmaz → clientWidth hep 0 olurdu ve `width > 0` kapısı
  // grafiği HİÇ mount etmezdi (test sessizce hiçbir şey ölçmezdi).
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', {
    configurable: true, get: () => 600,
  });
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', {
    configurable: true, get: () => 200,
  });
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  if (root) act(() => root!.unmount());
  host?.remove();
  root = null; host = null;
  holder.plot = null;
  localStorage.clear();
});

// Testler sahte örneği tipli okusun (holder'ın tipi hoist yüzünden inline).
const plot = () => holder.plot as unknown as FakePlot;

function render(el: React.ReactElement): HTMLDivElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  const r = createRoot(host);
  root = r;
  act(() => r.render(el));
  return host;
}

// İki serilik sahte veri — biri delikli (null taşıma yolu da yürüsün).
const TWO_SERIES = spanSeriesToFrames([
  { groupKey: ['checkout'], points: [
    { time: 1_000_000_000_000_000, value: 10 },
    { time: 1_000_060_000_000_000, value: 20 },
    { time: 1_000_120_000_000_000, value: 30 },
  ] },
  { groupKey: ['payments'], points: [
    { time: 1_000_000_000_000_000, value: 5 },
    { time: 1_000_060_000_000_000, value: null as unknown as number },
    { time: 1_000_120_000_000_000, value: 7 },
  ] },
], { unit: 'ms' });

describe('CorePanel render duman testi (UPlotChart vi.mock ile stub)', () => {
  it('ready: crash yok, başlık + grafik katmanı + lejant birlikte', () => {
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-ready"
        data={{ state: 'ready', frames: TWO_SERIES }} />);

    expect(el.textContent).toContain('Latency');
    const plot = el.querySelector('[data-testid="uplot"]');
    expect(plot, 'grafik katmanı mount olmadı').not.toBeNull();
    // uPlot serisi = x + iki veri serisi.
    expect(plot!.getAttribute('data-series')).toBe('3');
    expect(plot!.getAttribute('data-w')).toBe('600');
    // Lejant VARSAYILAN KAPALI (v0.9.743) ama seri sayısını duyurur.
    expect(el.textContent).toContain('Series (2)');
  });

  it('ready: lejant açılınca seri BAŞINA bir satır', () => {
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-legend"
        data={{ state: 'ready', frames: TWO_SERIES }} />);

    const toggle = Array.from(el.querySelectorAll('button'))
      .find(b => b.textContent?.includes('Series'));
    expect(toggle, 'lejant aç/kapa düğmesi yok').toBeDefined();
    act(() => { toggle!.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

    const rows = el.querySelectorAll('tbody tr');
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toContain('checkout');
    expect(rows[1].textContent).toContain('payments');
    // Hücreler birim taşır (v0.9.774: eksen/tooltip/lejant tek kaynak).
    expect(rows[0].textContent).toMatch(/ms/);
  });

  it('loading: spinner çizilir, lejant ve grafik YOK', () => {
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-loading" data={{ state: 'loading' }} />);
    expect(el.querySelector('[data-testid="uplot"]')).toBeNull();
    expect(el.textContent).not.toContain('Series (');
    // Spinner bir metin taşımaz; DOM'da bir düğüm bırakmalı.
    expect(el.querySelector('div')).not.toBeNull();
  });

  it('error: MESAJ yazılır — "veri yok" teşhisine düşmez', () => {
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-error"
        data={{ state: 'error', message: 'ClickHouse timeout' }} />);
    expect(el.textContent).toContain('Grafik yüklenemedi');
    expect(el.textContent).toContain('ClickHouse timeout');
    expect(el.textContent).not.toContain('Aralığı genişletmeyi deneyin');
    expect(el.querySelector('[data-testid="uplot"]')).toBeNull();
  });

  it('empty: SEBEP + sonraki adım birlikte (spec: boş durum açıklamadır)', () => {
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-empty"
        data={{ state: 'empty', reason: 'Bu serviste span yok', hint: 'Filtreleri azalt' }} />);
    expect(el.textContent).toContain('Bu serviste span yok');
    expect(el.textContent).toContain('Filtreleri azalt');
    expect(el.querySelector('[data-testid="uplot"]')).toBeNull();
  });

  it('tek noktalı ready: çizim yerine "çizilecek nokta yok" (kenar durum)', () => {
    const one = spanSeriesToFrames([
      { groupKey: ['solo'], points: [{ time: 1_000_000_000_000_000, value: 1 }] },
    ], {});
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-one" data={{ state: 'ready', frames: one }} />);
    expect(el.textContent).toContain('Bu aralıkta çizilecek nokta yok');
    expect(el.querySelector('[data-testid="uplot"]')).toBeNull();
  });

  it('v0.9.788 stacked: bantlar config\'e girer, kümülatif matris aligned\'ı bozmaz', () => {
    const el = render(
      <CorePanel title="Throughput" storageKey="smoke-stacked" viz="stacked"
        data={{ state: 'ready', frames: TWO_SERIES }} />);
    const plot = el.querySelector('[data-testid="uplot"]');
    expect(plot).not.toBeNull();
    // İki görünür katman = ardışık çift = 1 bant.
    expect(plot!.getAttribute('data-bands')).toBe('1');
  });

  it('v0.9.792/793: pin ipucu ve kesikli işaret mount\'u kırmaz', () => {
    const el = render(
      <CorePanel title="Latency" storageKey="smoke-mixed"
        dashed={[false, true]} onBucketClick={() => {}}
        data={{ state: 'ready', frames: TWO_SERIES }} />);
    expect(el.querySelector('[data-testid="uplot"]')).not.toBeNull();
    // bucket-tık afordansı (v0.9.789) ready durumda görünür.
    expect(el.textContent).toContain('tık → örnek trace');
  });

  // ── v0.9.793 focusedLabel: efektin GERÇEKTEN yazdığını ölç ───────────────
  it('focusedLabel yokken seriler nötr (alpha 1, taban genişlik)', () => {
    render(<CorePanel title="L" storageKey="smoke-nofocus"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    expect(plot().series[1]).toMatchObject({ alpha: 1, width: 1.5 });
    expect(plot().series[2]).toMatchObject({ alpha: 1, width: 1.5 });
  });

  it('focusedLabel: odaklanan kalın+tam, öteki SOLUK — rebuild değil redraw', () => {
    render(<CorePanel title="L" storageKey="smoke-focus" focusedLabel="payments"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    expect(plot().series[1]).toMatchObject({ alpha: 0.25, width: 1.5 }); // checkout
    expect(plot().series[2]).toMatchObject({ alpha: 1, width: 2 });      // payments
    // Odak bir YENİDEN ÇİZİMDİR; uPlot yaşamaya devam eder (v0.9.704).
    expect(plot().redraws).toBeGreaterThan(0);
  });

  it('focusedLabel bu panelde yoksa kimse solmaz (Explore çapraz-panel hover)', () => {
    render(<CorePanel title="L" storageKey="smoke-otherpanel" focusedLabel="billing"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    expect(plot().series[1]!.alpha).toBe(1);
    expect(plot().series[2]!.alpha).toBe(1);
  });

  // ── v0.9.799 — eksen oluğu + imleç noktaları (operatör-raporlu) ────────
  it('y ekseni oluk genişliği SAYISAL gelir — Grafana otomatiğine bırakılmaz', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-gutter"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    const size = el.querySelector('[data-testid="uplot"]')!.getAttribute('data-yaxis-size');
    // jsdom'da canvas ölçmez → kaba tahmine düşer; kapının derdi ZATEN
    // mutlak piksel değil, oluğun HESAPLANMIŞ olması.
    expect(Number(size), 'eksen size verilmedi').toBeGreaterThan(0);
  });

  it('imleç noktası boyutu SAYI, show hiç yazılmaz (uPlot tuzağı)', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-cursor"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    const pt = el.querySelector('[data-testid="uplot"]')!.getAttribute('data-cursor-pt');
    // size/width/show — show BOŞ olmalı: `show: true` yazmak uPlot'ta
    // noktaları tamamen kapatır (fnOrSelf(true) HTMLElement döndürmez).
    expect(pt).toBe('10/2/');
  });

  it('iç lejant satırı hover\'ı da odak kanalını sürer', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-hover"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    const toggle = Array.from(el.querySelectorAll('button'))
      .find(b => b.textContent?.includes('Series'))!;
    act(() => { toggle.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    const row = el.querySelectorAll('tbody tr')[0];
    act(() => { row.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })); });
    expect(plot().series[1]).toMatchObject({ alpha: 1, width: 2 });
    expect(plot().series[2]!.alpha).toBe(0.25);
    act(() => { row.dispatchEvent(new MouseEvent('mouseout', { bubbles: true })); });
    expect(plot().series[2]!.alpha).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// v0.9.811 — ÇUBUK AİLESİNDE Y TABANI SIFIR.
//
// Çubuğun UZUNLUĞU değeri kodlar ve okur onu tabandan ölçer. uPlot'un
// otomatik aralığı ise veriye göre kayıyor: 1200-1260 arasında gezen bir
// seride taban ~1190 olur ve 1200'lük çubuk 1260'lık çubuğun beşte biri
// boyunda çizilir — grafiğin okunan tek bilgisi yanlış. Çizgide aynı
// kayma DOĞRUDUR (çizgide kodlanan konum, uzunluk değil), o yüzden bu bir
// MARK kuralıdır ve line/area'ya dokunmaz.
//
// Kapılar aralık fonksiyonunu GERÇEKTEN çağırır: @grafana/ui softMin'i
// uPlot soft-mode'una çeviriyor mu, uPlot tabanı çekiyor mu, ve negatif
// veride soft limiti yok sayıyor mu (yoksa `min: 0` gibi veriyi kırpardı).
// ---------------------------------------------------------------------------
describe('CorePanel çubuk tabanı sıfır (v0.9.811)', () => {
  const yRange = (el: HTMLElement, attr: 'pos' | 'neg') =>
    el.querySelector('[data-testid="uplot"]')!.getAttribute(`data-yrange-${attr}`)!;

  it('🔴 bars: yüksek+dar veride taban 0\'a iner', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-bars-base" viz="bars"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    expect(yRange(el, 'pos').split(',')[0]).toBe('0');
  });

  it('🔴 stacked-bars da yığın ailesinin çubuk markı — aynı taban', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-sbars-base" viz="stacked-bars"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    expect(yRange(el, 'pos').split(',')[0]).toBe('0');
  });

  it('🔴 line ETKİLENMEZ — otomatik aralık veriye yakın kalır', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-line-base" viz="line"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    const lo = Number(yRange(el, 'pos').split(',')[0]);
    expect(lo).toBeGreaterThan(1000); // 0'a inmedi
    expect(lo).toBeLessThanOrEqual(1200);
  });

  it('area ve stacked de dokunulmadan kalır (dolgu markı ≠ çubuk markı)', () => {
    for (const viz of ['area', 'stacked'] as const) {
      const el = render(<CorePanel title="L" storageKey={`smoke-${viz}-base`} viz={viz}
        data={{ state: 'ready', frames: TWO_SERIES }} />);
      expect(Number(yRange(el, 'pos').split(',')[0]), viz).toBeGreaterThan(1000);
      if (root) act(() => root!.unmount());
      host?.remove();
      root = null; host = null;
    }
  });

  it('🔴 NEGATİF veride soft taban YOK SAYILIR — `min: 0` olsaydı kırpardı', () => {
    const el = render(<CorePanel title="L" storageKey="smoke-bars-neg" viz="bars"
      data={{ state: 'ready', frames: TWO_SERIES }} />);
    const lo = Number(yRange(el, 'neg').split(',')[0]);
    expect(lo).toBeLessThan(-40); // veri minimumunun ALTINDA, 0'da değil
  });
});
