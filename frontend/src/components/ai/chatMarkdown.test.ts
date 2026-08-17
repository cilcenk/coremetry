// v0.9.1148 (AI Faz 4.2) — sohbet balonu blok çözücüsünün SAF testleri.
//
// Neden bu kadar çok yarım-akış vakası: bu çözücünün girdisi BÜYÜYEN bir
// dize. Tek bir cevap, model yazarken düzinelerce kez ayrıştırılıyor ve
// aradaki her hâl ekranda görünüyor. Yani "tamamlanmış metinle doğru
// çıktı" testin YARISI; diğer yarısı "yarım metinle GÖRÜNTÜ bozulmuyor".
// Faz 4.2'nin ürün riski tam burada: tablo/fence desteği eklerken yarım
// bir tabloyu ham çubuklarla basmak ya da yarım bir çiti kod bloğu sanıp
// cevabın kalanını yutmak, düzeltmenin kendisinden daha kötü olurdu.
import { describe, expect, it } from 'vitest';
import { parseChatBlocks, splitRow, type ChatBlock } from './chatMarkdown';

const kinds = (bs: ChatBlock[]) => bs.map(b => b.kind);
const table = (bs: ChatBlock[]) => bs.find(b => b.kind === 'table') as Extract<ChatBlock, { kind: 'table' }>;
const code = (bs: ChatBlock[]) => bs.find(b => b.kind === 'code') as Extract<ChatBlock, { kind: 'code' }>;
const texts = (bs: ChatBlock[]) =>
  bs.filter((b): b is Extract<ChatBlock, { kind: 'text' }> => b.kind === 'text').map(b => b.text);

const TBL = [
  'Yavaş servisler:',
  '',
  '| Servis | p95 |',
  '|---|---:|',
  '| checkout | 480 |',
  '| payment | 1200 |',
  '',
  'İlk ikisi aynı veritabanını kullanıyor.',
].join('\n');

describe('tablo', () => {
  it('başlık + ayraç + satırlar → tablo bloğu', () => {
    const bs = parseChatBlocks(TBL);
    expect(kinds(bs)).toEqual(['text', 'table', 'text']);
    const t = table(bs);
    expect(t.head).toEqual(['Servis', 'p95']);
    expect(t.rows).toEqual([['checkout', '480'], ['payment', '1200']]);
    // Tablo etrafındaki metin AYRI bloklar — kenar boş satırları atılmış.
    expect(texts(bs)).toEqual(['Yavaş servisler:', 'İlk ikisi aynı veritabanını kullanıyor.']);
  });

  it('ayraç satırından hizalama okunuyor', () => {
    const bs = parseChatBlocks('| a | b | c |\n| :--- | ---: | :-: |\n| 1 | 2 | 3 |\n');
    expect(table(bs).align).toEqual(['left', 'right', 'center']);
  });

  it('AYRAÇ satırı yoksa tablo DEĞİL — düz metin kalır', () => {
    const bs = parseChatBlocks('| Servis | p95 |\n| checkout | 480 |\n');
    expect(kinds(bs)).toEqual(['text']);
  });

  it('BAŞLIKTA çubuk şart: düzyazıdaki çubuk tablo başlatmaz', () => {
    // Gevşek GFM kuralı bu iki satırı tabloya çevirirdi.
    const bs = parseChatBlocks('hızlı | yavaş\n---|---\n');
    expect(kinds(bs)).toEqual(['text']);
  });

  it('ayraçta baştaki çubuk ŞART DEĞİL (model bazen yazmıyor)', () => {
    const bs = parseChatBlocks('| a | b |\n---|---:\n| 1 | 2 |\n');
    expect(table(bs).align).toEqual(['left', 'right']);
    expect(table(bs).rows).toEqual([['1', '2']]);
  });

  it('tire TAŞIMAYAN ayraç hücresi tabloyu açmaz', () => {
    // Satır düzeyinde "en az bir tire" yetmez: `| : | - |` GFM'de ayraç
    // DEĞİL, ve hizalamayı ondan okumak uydurma olurdu.
    expect(kinds(parseChatBlocks('| a | b |\n| : | - |\n| 1 | 2 |\n'))).toEqual(['text']);
  });

  it('ayraç hücre SAYISI başlıkla eşleşmezse tablo değil', () => {
    // `| önemli | not |` + `---` (yatay çizgi): sayı kapısı olmasa
    // düzyazı boş bir tabloya dönerdi.
    expect(kinds(parseChatBlocks('| önemli | not |\n---\n'))).toEqual(['text']);
    expect(kinds(parseChatBlocks('| a | b | c |\n|---|---|\n| 1 | 2 | 3 |\n'))).toEqual(['text']);
  });

  it('kaçırılmış çubuk hücre İÇERİĞİ, kod aralığındaki çubuk bölmez', () => {
    expect(splitRow('| a \\| b | c |')).toEqual(['a | b', 'c']);
    expect(splitRow('| `p95 | p99` | 12 |')).toEqual(['`p95 | p99`', '12']);
  });

  it('kısa satır doldurulur, FAZLA hücre korunur (içerik yutulmaz)', () => {
    const bs = parseChatBlocks('| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |\n');
    expect(table(bs).rows).toEqual([['1', ''], ['1', '2', '3']]);
  });

  it('tablo, çubuksuz ilk satırda BİTER', () => {
    const bs = parseChatBlocks('| a |\n|---|\n| 1 |\nson söz\n');
    expect(kinds(bs)).toEqual(['table', 'text']);
    expect(table(bs).rows).toEqual([['1']]);
  });
});

describe('fence', () => {
  it('kapanmış fence: dil + gövde, open=false', () => {
    const bs = parseChatBlocks('şu sorgu:\n```sql\nSELECT 1\nFROM t\n```\nbitti\n');
    expect(kinds(bs)).toEqual(['text', 'code', 'text']);
    expect(code(bs)).toMatchObject({ lang: 'sql', code: 'SELECT 1\nFROM t', open: false });
  });

  it('kapanmamış fence (akış BİTTİ) içeriği KORUR, open=true', () => {
    const bs = parseChatBlocks('```sql\nSELECT 1\n');
    expect(code(bs)).toMatchObject({ lang: 'sql', code: 'SELECT 1', open: true });
  });

  it('fence gövdesindeki markdown ham kalır', () => {
    const bs = parseChatBlocks('```\n**bold** ve `kod`\n```\n');
    expect(code(bs).code).toBe('**bold** ve `kod`');
  });

  it('chart fence dili taşıyor (balon onu grafiğe çeviriyor)', () => {
    const bs = parseChatBlocks('\n```chart\n{"service":"a","agg":"p95"}\n```\n');
    expect(code(bs)).toMatchObject({ lang: 'chart', open: false });
    expect(JSON.parse(code(bs).code)).toEqual({ service: 'a', agg: 'p95' });
  });

  it('fence içindeki tablo satırları TABLO OLMAZ', () => {
    const bs = parseChatBlocks('```\n| a | b |\n|---|---|\n```\n');
    expect(kinds(bs)).toEqual(['code']);
  });
});

describe('başlık ve liste', () => {
  it('# / ## / ### kademeleri, #### üçe kırpılıyor', () => {
    const bs = parseChatBlocks('# bir\n## iki\n### üç\n#### dört\n');
    expect(bs).toEqual([
      { kind: 'heading', level: 1, text: 'bir' },
      { kind: 'heading', level: 2, text: 'iki' },
      { kind: 'heading', level: 3, text: 'üç' },
      { kind: 'heading', level: 3, text: 'dört' },
    ]);
  });

  it('boşluksuz #foo başlık değil', () => {
    expect(kinds(parseChatBlocks('#foo\n'))).toEqual(['text']);
  });

  it('madde ve sıralı liste; tür değişimi listeyi böler', () => {
    const bs = parseChatBlocks('- bir\n- iki\n1. üç\n2) dört\n');
    expect(bs).toEqual([
      { kind: 'list', ordered: false, items: ['bir', 'iki'] },
      { kind: 'list', ordered: true, items: ['üç', 'dört'] },
    ]);
  });

  it('--- yatay çizgisi liste değil', () => {
    expect(kinds(parseChatBlocks('---\n'))).toEqual(['text']);
  });
});

describe('metin koşusu', () => {
  it('iç boş satır korunur, kenar boş satırlar atılır', () => {
    expect(texts(parseChatBlocks('\n\nbir\n\niki\n\n'))).toEqual(['bir\n\niki']);
  });

  it('boş metin blok üretmez', () => {
    expect(parseChatBlocks('')).toEqual([]);
    expect(parseChatBlocks('   \n\n')).toEqual([]);
  });
});

// ── AKIŞ (streaming=true) ────────────────────────────────────────────
//
// Sözleşme tek cümle: bitmemiş SON satır yalnız "yapı kuran" bir işaret
// ise tutulur (çubuk / çit / diyez), bilgi taşıyorsa basılır.
describe('akış — yarım satır', () => {
  it('yarım DÜZYAZI satırı BASILIR (yoksa balon akış boyunca boş durur)', () => {
    // En önemli iddia: model `\n` basmadan uzun uzun yazabiliyor.
    expect(texts(parseChatBlocks('checkout p95 480ms ve yükseli', true))).toEqual(['checkout p95 480ms ve yükseli']);
  });

  it('yarım tablo BAŞLIĞI ham çubuklarla basılmaz', () => {
    const bs = parseChatBlocks('Yavaşlar:\n| Servis | p9', true);
    expect(kinds(bs)).toEqual(['text']);
    expect(texts(bs)).toEqual(['Yavaşlar:']);
  });

  it('yarım tablo SATIRI satır olmaz; \\n gelince satır olur', () => {
    const half = '| Servis | p95 |\n|---|---|\n| checkout | 480 |\n| payme';
    expect(table(parseChatBlocks(half, true)).rows).toEqual([['checkout', '480']]);
    expect(table(parseChatBlocks(half + 'nt | 1200 |\n', true)).rows)
      .toEqual([['checkout', '480'], ['payment', '1200']]);
  });

  it('yarım fence AÇILIŞI tutulur (çıplak backtick yanıp sönmez)', () => {
    expect(parseChatBlocks('sorgu:\n```sq', true)).toEqual([{ kind: 'text', text: 'sorgu:' }]);
    // Backtick'le BAŞLAYAN düzyazı tutulmaz — kalıp dar: yalnız çit+dil.
    expect(kinds(parseChatBlocks('`GET /pay` yav', true))).toEqual(['text']);
  });

  it('fence İÇİNDE yarım kod satırı basılır (kodda da yazma hissi)', () => {
    expect(code(parseChatBlocks('```sql\nSELECT 1\nFROM t WHER', true)))
      .toMatchObject({ code: 'SELECT 1\nFROM t WHER', open: true });
  });

  it('yarım KAPANIŞ çiti kod bloğunu açık bırakır', () => {
    const bs = parseChatBlocks('```sql\nSELECT 1\n``', true);
    expect(code(bs)).toMatchObject({ code: 'SELECT 1', open: true });
  });

  it('yarım başlık diyezleri tutulur', () => {
    expect(parseChatBlocks('bir şey\n##', true)).toEqual([{ kind: 'text', text: 'bir şey' }]);
  });

  it('akış BİTTİĞİNDE hiçbir şey tutulmaz — aynı metin, iki farklı çıktı', () => {
    const half = 'Yavaşlar:\n| Servis | p9';
    expect(kinds(parseChatBlocks(half, true))).toEqual(['text']);
    // Kesilmiş cevapta içeriği yutmak yerine ham göster (Markdown.tsx
    // "bilinmeyen işaret olduğu gibi geçer" ilkesi).
    expect(texts(parseChatBlocks(half, false))).toEqual([half]);
  });

  it('\\n ile biten akan metinde yarım satır YOKTUR', () => {
    // `text.endsWith('\n')` dalı: son satır boş dizedir, tutulacak bir
    // şey yok — tablo tam çizilir.
    const bs = parseChatBlocks('| a |\n|---|\n| 1 |\n', true);
    expect(table(bs).rows).toEqual([['1']]);
  });
});
