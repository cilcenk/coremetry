// @vitest-environment jsdom
//
// StackTrace.test.tsx — v0.10.581 (Aşama 1.5).
//
// Bu bileşenin GERÇEK sözleşmesi metnin KORUNMASI. Bir stack trace,
// operatörün okuduğu tek kanıt: satır sırası, boş satırlar ve
// "Caused by:" bloklarının hizası bilginin kendisi. Süsleme yapan her
// çizim bu bilgiyi yutma riskini taşır ve yutma SESSİZ olur — tsc
// göremez, eslint göremez, ekranda "biraz kısa" bir stack hiçbir
// alarm çalmaz. Kapı bu yüzden metin eşitliğini çiviliyor, görünümü
// değil (globals.css jsdom'a yüklenmiyor; sınıfların CSS karşılığını
// `styles/undefinedCssRefs` ayrı olarak yokluyor).
import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import type { StackFrameLink } from '@/lib/types';
import { StackTrace } from './StackTrace';

let host: HTMLDivElement | null = null;
let root: Root | null = null;

function render(node: ReactNode): HTMLElement {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(node); });
  return host;
}

afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
});

const pre = (el: HTMLElement) => el.querySelector('pre.ex-stack') as HTMLPreElement;

// Gerçek bir Java stack'inin şekli: mesaj başlığı, boş satır, iki
// "Caused by:" bloğu, katlanmış "... 12 more". Sentetik iki satırlık
// bir fixture bu kapının tam da korumak istediği şeyleri (boş satır,
// tekrar eden frame, blok başlıkları) hiç sınamazdı.
const STACK = [
  /*  0 */ 'java.lang.NullPointerException: hesap bulunamadi',
  /*  1 */ '\tat com.banka.hesap.HesapService.getir(HesapService.java:142)',
  /*  2 */ '\tat org.springframework.aop.Invoker.invoke(Invoker.java:88)',
  /*  3 */ '',
  /*  4 */ 'Caused by: java.sql.SQLException: ORA-00942',
  /*  5 */ '\tat com.banka.hesap.HesapRepo.bul(HesapRepo.java:57)',
  /*  6 */ '\tat com.banka.hesap.HesapService.getir(HesapService.java:142)',
  /*  7 */ '\t... 12 more',
].join('\n');

const frame = (p: Partial<StackFrameLink> & { lineIndex: number }): StackFrameLink => ({
  class: 'com.banka.hesap.HesapService', method: 'getir',
  file: 'HesapService.java', line: 142, isApp: true, tier: 0, ...p,
});

const APP_URL = 'https://devops.local/_git/Hesap?path=/src/HesapService.java&line=142';

describe('StackTrace — frames YOKKEN bugünkü düz metin', () => {
  // Aşama 1.5'in geri çekilme yolu. Uç `configured:false` dönerse,
  // istek düşerse ya da hiçbir frame tanınmazsa yüzey ESKİSİ gibi
  // davranmalı: operatöre ne hata ne uyarı, sadece stack.
  it('frames verilmediğinde çıktı düz <pre> metninin BİREBİR aynısı', () => {
    const el = render(<StackTrace stack={STACK} />);
    expect(pre(el).textContent).toBe(STACK);
    expect(el.querySelectorAll('a').length).toBe(0);
    expect(el.querySelectorAll('span').length).toBe(0);
  });

  it('boş frame listesi de düz metin (configured ama hiçbir frame tanınmadı)', () => {
    const el = render(<StackTrace stack={STACK} frames={[]} warning="uyari" />);
    expect(pre(el).textContent).toBe(STACK);
    expect(el.textContent).toBe(STACK); // uyarı rozeti de çizilmez
  });

  it('frames yokken uyarı metni GÖSTERİLMEZ', () => {
    const el = render(<StackTrace stack={STACK} warning="branş sürümü farklı olabilir" />);
    expect(el.querySelector('.badge')).toBeNull();
  });
});

describe('StackTrace — lineIndex eşleşmesi', () => {
  it('yalnız işaret edilen satır süslenir, komşuları düz kalır', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({ lineIndex: 1, url: APP_URL })]} />,
    );
    const a = el.querySelectorAll('a');
    expect(a.length).toBe(1);
    expect(a[0].textContent).toBe('\tat com.banka.hesap.HesapService.getir(HesapService.java:142)');
    expect(a[0].getAttribute('href')).toBe(APP_URL);
  });

  // Bu, metin-eşleştirmeli bir uygulamanın DÜŞECEĞİ yer: 1 ve 6.
  // satırlar KARAKTERİ KARAKTERİNE aynı. Sunucu yalnız 6'yı
  // işaretlediyse 1 düz kalmalı.
  it('aynı metne sahip iki satırdan YALNIZ işaretleneni süslenir', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({ lineIndex: 6, url: APP_URL })]} />,
    );
    const a = el.querySelectorAll('a');
    expect(a.length).toBe(1);
    // KONUMDAN doğrula, metin aramasından DEĞİL: aranan dizgi 1.
    // satırda da geçtiği için `indexOf` her zaman 1'i bulur ve testin
    // kendisi tam da korumak istediği tuzağa düşerdi.
    let before = '';
    for (const n of Array.from(pre(el).childNodes)) {
      if (n === a[0]) break;
      before += n.textContent ?? '';
    }
    expect(before.split('\n').length - 1).toBe(6);
  });

  it('boş satır ve "Caused by:" başlığı hiç dokunulmadan geçer', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({ lineIndex: 5, url: APP_URL })]} />,
    );
    const lines = pre(el).textContent!.split('\n');
    expect(lines[3]).toBe('');
    expect(lines[4]).toBe('Caused by: java.sql.SQLException: ORA-00942');
  });

  it('menzil dışı lineIndex çizimi bozmaz', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({ lineIndex: 99, url: APP_URL })]} />,
    );
    expect(pre(el).textContent).toBe(STACK);
  });
});

describe('StackTrace — link ÜRETME kuralları', () => {
  it('kütüphane frame\'i URL taşısa bile link ÜRETMEZ, soluk çizilir', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({
        lineIndex: 2, isApp: false, tier: 3, url: APP_URL,
        class: 'org.springframework.aop.Invoker', method: 'invoke',
        file: 'Invoker.java', line: 88,
      })]} />,
    );
    expect(el.querySelectorAll('a').length).toBe(0);
    const s = el.querySelector('span.ex-frame-lib') as HTMLElement;
    expect(s).not.toBeNull();
    expect(s.textContent).toBe('\tat org.springframework.aop.Invoker.invoke(Invoker.java:88)');
  });

  it('url\'süz uygulama frame\'i link üretmez ama reason\'ı title\'da taşır', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({
        lineIndex: 1, url: undefined, reason: 'depo çözülemedi: HesapService',
      })]} />,
    );
    expect(el.querySelectorAll('a').length).toBe(0);
    const s = el.querySelector('span.ex-frame-app') as HTMLElement;
    expect(s.getAttribute('title')).toBe('depo çözülemedi: HesapService');
  });

  it('reason boşsa title hiç yazılmaz (boş tooltip kutusu çıkmasın)', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({ lineIndex: 1, url: undefined, reason: '' })]} />,
    );
    expect(el.querySelector('span.ex-frame-app')!.hasAttribute('title')).toBe(false);
  });

  it('dış bağlantı yeni sekmede ve referrer sızdırmadan açılır', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[frame({ lineIndex: 1, url: APP_URL })]} />,
    );
    const a = el.querySelector('a')!;
    expect(a.getAttribute('target')).toBe('_blank');
    expect(a.getAttribute('rel')).toContain('noreferrer');
  });
});

describe('StackTrace — sürüm uyarısı', () => {
  const WARN = 'main branşı gösteriliyor; exception başka bir sürümde koşmuş olabilir';

  it('link varsa bölümün üstünde BİR KEZ çizilir, satır başına değil', () => {
    const el = render(
      <StackTrace stack={STACK} warning={WARN} frames={[
        frame({ lineIndex: 1, url: APP_URL }),
        frame({ lineIndex: 5, url: APP_URL, file: 'HesapRepo.java', line: 57 }),
        frame({ lineIndex: 6, url: APP_URL }),
      ]} />,
    );
    expect(el.querySelectorAll('a').length).toBe(3);
    const badges = el.querySelectorAll('.badge.b-warn');
    expect(badges.length).toBe(1);
    expect(badges[0].textContent).toBe(WARN);
    // Ve uyarı <pre>'nin DIŞINDA: stack metnini kirletmiyor.
    expect(pre(el).textContent).toBe(STACK);
  });

  it('hiç link üretilmediyse uyarı çizilmez (gürültü olurdu)', () => {
    const el = render(
      <StackTrace stack={STACK} warning={WARN} frames={[
        frame({ lineIndex: 1, url: undefined, reason: 'ağaçta yok' }),
        frame({ lineIndex: 2, isApp: false, tier: 3 }),
      ]} />,
    );
    expect(el.querySelectorAll('a').length).toBe(0);
    expect(el.querySelector('.badge')).toBeNull();
  });
});

describe('StackTrace — hiçbir satır yutulmuyor', () => {
  // Kapının ÇEKİRDEĞİ. Her frame kombinasyonunda <pre>'nin metni
  // girdinin AYNISI olmalı: satır sayısı, sıra, boş satırlar,
  // sekmeler. Bir sarmalayıcı fazladan boşluk basar ya da bir satırı
  // atlarsa burası kırmızıya döner.
  const CASES: Array<[string, StackFrameLink[]]> = [
    ['tek app link', [frame({ lineIndex: 1, url: APP_URL })]],
    ['app + kütüphane karışık', [
      frame({ lineIndex: 1, url: APP_URL }),
      frame({ lineIndex: 2, isApp: false, tier: 3 }),
      frame({ lineIndex: 5, url: APP_URL }),
      frame({ lineIndex: 6, url: APP_URL }),
    ]],
    ['linksiz app frame', [frame({ lineIndex: 5, url: undefined, reason: 'yok' })]],
    ['her satır işaretli', Array.from({ length: 8 }, (_, i) =>
      frame({ lineIndex: i, url: i % 2 ? APP_URL : undefined, isApp: i % 3 !== 0 }))],
  ];
  for (const [name, frames] of CASES) {
    it(`${name}: metin girdiyle birebir aynı`, () => {
      const el = render(<StackTrace stack={STACK} frames={frames} />);
      expect(pre(el).textContent).toBe(STACK);
      expect(pre(el).textContent!.split('\n').length).toBe(STACK.split('\n').length);
    });
  }

  it('tek satırlık stack (satırsonu yok) sonuna \\n eklemez', () => {
    const one = 'at com.banka.X.y(X.java:1)';
    const el = render(<StackTrace stack={one} frames={[frame({ lineIndex: 0, url: APP_URL })]} />);
    expect(pre(el).textContent).toBe(one);
  });

  it('sondaki boş satır korunur', () => {
    const s = 'a\nb\n';
    const el = render(<StackTrace stack={s} frames={[frame({ lineIndex: 0, url: APP_URL })]} />);
    expect(pre(el).textContent).toBe(s);
  });

  it('aynı satır için iki frame gelirse satır TEK süs alır', () => {
    const el = render(
      <StackTrace stack={STACK} frames={[
        frame({ lineIndex: 1, url: APP_URL }),
        frame({ lineIndex: 1, url: APP_URL + '&dup=1' }),
      ]} />,
    );
    expect(el.querySelectorAll('a').length).toBe(1);
    expect(pre(el).textContent).toBe(STACK);
  });
});
