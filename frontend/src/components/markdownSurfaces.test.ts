import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { stripMarkdown } from './Markdown';

// v0.9.641 — operatör-bildirimli: "neden kök neden ipucu veya öncelikli
// inceleme başlıkları bold yazmıyor".
//
// Model markdown üretiyor (**Kök Neden İpucu:**) ama CopilotExplain
// {text}'i HAM basıyordu — yıldızlar ekranda görünüyordu. Oysa
// RenderedMarkdown v0.7.0'dan beri VAR ve tam bu işi yapıyor; yalnız
// Runbook sayfaları kullanıyordu. Bileşen var, AI yüzeyi ondan
// habersizdi.

function read(rel: string) {
  return readFileSync(resolve(__dirname, rel), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')            // blok yorumlar
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n'); // satır yorumları
}

describe('yorum sıyırma', () => {
  it('açıklamadaki alıntıyı koda saymaz', () => {
    const src = read('./CopilotExplain.tsx');
    // Bu dosyanın YORUMU "{text}" dizgisini alıntılıyor; sıyrılmazsa
    // aşağıdaki test kendi açıklamasıyla eşleşip hep kırmızı kalırdı.
    expect(src).not.toContain('HAM');
  });
});

describe('AI açıklama yüzeyi markdown basıyor', () => {
  const src = read('./CopilotExplain.tsx');

  it('RenderedMarkdown kullanıyor', () => {
    expect(src).toContain('<RenderedMarkdown text={text} />');
  });

  it('ham {text} bırakılmadı', () => {
    // Tek başına bir satırda çıplak {text} — ham basımın imzası.
    expect(src).not.toMatch(/^\s*\{text\}\s*$/m);
  });

  // pre-wrap + Markdown birlikte satır aralarını ikiye katlıyor:
  // Markdown zaten <p>/<ul>/<h*> üretiyor.
  it('metin kutusunda pre-wrap kalmadı', () => {
    const i = src.indexOf('maxWidth:');
    expect(src.slice(Math.max(0, i - 300), i)).not.toContain('pre-wrap');
  });
});

describe('RenderedMarkdown sözleşmesi', () => {
  const md = read('./Markdown.tsx');
  it('bold desteği duruyor', () => {
    expect(md).toMatch(/renderInline/);
    // Gerçek yayıcı <b> — "strong" aramak sözleşmeyi değil kelimeyi
    // çivilerdi.
    expect(md).toContain('<b key=');
  });
});

// v0.9.696 — KAPI GENELLEŞTİRİLDİ.
//
// Operatör aynı kusuru İKİNCİ KEZ bildirdi ("burada neden açıklama da
// hata tipi falan bold yazmıyor"), bu sefer exception detayında.
//
// v0.9.641 kusuru CopilotExplain'de düzeltmişti ve testi de YALNIZ o
// dosyayı çiviliyordu. Oysa AI metni BEŞ yerde basılıyor:
//   ProblemDetail ×2 (exception + problem), AnomaliesPage, Inbox, Service.
// Dördü kapının DIŞINDAYDI. Tek dosyayı çivileyen bir test, sınıfı değil
// ÖRNEĞİ koruyor — ve sınıf geri geldi.
//
// Bu kapı artık her aiSummary basımını sayıyor: ya RenderedMarkdown
// (kırpılmamış prose) ya stripMarkdown (kırpılmış kart + title). Çıplak
// {x.aiSummary} = hata.
describe('AI özeti basan HER yüzey markdown\'ı ele alıyor', () => {
  const SURFACES = [
    '../features/anomalies/ProblemDetail.tsx',
    '../features/anomalies/AnomaliesPage.tsx',
    '../pages/Inbox.tsx',
    '../pages/Service.tsx',
  ];

  // İLK YAZIMIM ŞEKLE BAKIYORDU ve YANLIŞ POZİTİF verdi: `{p.aiSummary}`
  // arayan regex, DOĞRU kullanım olan `<RenderedMarkdown text={p.aiSummary} />`
  // ifadesini de yakaladı. Şekil değil NİYET aranmalı — bir satır, metni
  // geçirdiği YOLLA aklanır.
  //
  // `aiSummaryAt` (zaman damgası) ayrı bir alan; `(?!At)` onu dışarıda
  // tutuyor, yoksa her tarih biçimlendirmesi kaçak sayılırdı.
  const USE = /\.aiSummary(?!At)/;

  for (const rel of SURFACES) {
    const name = rel.split('/').pop();
    it(`${name} — her aiSummary basımı bir yoldan geçiyor`, () => {
      const offenders = read(rel).split('\n')
        .map((text, i) => ({ text: text.trim(), n: i + 1 }))
        .filter(x => USE.test(x.text))
        // AKLAYAN YOLLAR — ikisi de bilinçli:
        //   RenderedMarkdown : kırpılmamış prose (blok üretir)
        //   stripMarkdown    : kırpılmış kart + title özniteliği
        .filter(x => !/RenderedMarkdown|stripMarkdown/.test(x.text))
        // Varlık kontrolü metni BASMIYOR: `x.aiSummary && (`, `!x.aiSummary`,
        // `x.aiSummary ? a : b`.
        .filter(x => !/\.aiSummary(?!At)\s*(&&|\?)/.test(x.text))
        .filter(x => !/!\s*[\w$.]*\.aiSummary(?!At)/.test(x.text));

      expect(offenders.map(o => `${o.n}: ${o.text}`), 'ham AI metni').toEqual([]);
    });
  }
});

describe('stripMarkdown', () => {
  // Ekran görüntüsündeki GERÇEK model çıktısı — düzeltmenin hedefi bu.
  const real = '* **Hata Tipi:** `CannotCreateTransactionException`, Spring\'in veritabanı üzerinde';

  it('operatörün gördüğü satırı düzleştiriyor', () => {
    expect(stripMarkdown(real)).toBe(
      'Hata Tipi: CannotCreateTransactionException, Spring\'in veritabanı üzerinde');
  });

  it('başlık ve madde imlerini atıyor', () => {
    expect(stripMarkdown('## Kök Neden\n- ilk\n- ikinci')).toBe('Kök Neden ilk ikinci');
  });

  it('link etiketini bırakıp URL\'yi atıyor', () => {
    expect(stripMarkdown('bkz [runbook](https://x.example/y) detay')).toBe('bkz runbook detay');
  });

  // Kapanışsız işaret satırın kalanını YUTMAMALI — RenderedMarkdown'ın
  // "bilinmeyen markdown olduğu gibi geçer" ilkesiyle aynı.
  it('kapanışsız yıldızı olduğu gibi bırakıyor', () => {
    expect(stripMarkdown('yarım **kalın ve devamı')).toBe('yarım **kalın ve devamı');
  });

  it('satır sonlarını boşluğa indiriyor (tek satır kırpması için)', () => {
    expect(stripMarkdown('bir\n\niki')).toBe('bir iki');
  });
});
