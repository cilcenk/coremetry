// insightHosts — v0.9.1133 (AI Faz 2.3) YUVA BAĞLANMA kapısı.
//
// insightRow.test.tsx davranışı ölçüyor ama kendi harness'ıyla: yuva
// birimleri doğru çalışsa da GERÇEK host onları yanlış bağlayabilir ve
// hiçbir şey kızarmaz. Bu deponun en pahalı dersi tam bu — "saf test ≠
// BAĞLANMA" (v0.9.1012) ve "kapı kapsamı göçte erir": bir atom ikinci bir
// yazılış doğurduğunda eski kapı yeni dosyaları ölçmeyi bırakır, sayı
// yeşil kaldığı için kimse fark etmez.
//
// Somut, tip-doğru ve SESSİZ üç kırılma bu kapının varlık sebebi:
//   1. kartın koşullu çizimi düşer (`<InsightRowSlot …/>` her satırda) →
//      her satır bir ES projeksiyonu + bir LLM çağrısı; ekran neredeyse
//      aynı görünür, fatura görünmez;
//   2. çipin `toggle`ı yanlış kimliği taşır (fingerprint yerine servis
//      adı gibi) → kart açılır ama BAŞKA bir özneyi anlatır;
//   3. problem satırında şerit `suppressed` almaz → aynı satırda iki
//      kanıt yüzeyi üst üste açılır (v0.9.306 sınıfı) ve mockup'ın
//      "tek şerit" kararı sessizce geri alınır.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, relative } from 'node:path';

const SRC = resolve(__dirname, '../..');

// Yorumları BOŞALT (satır numaraları korunur). ŞART: bu dosyaların
// şerhleri kendi sözleşmelerini düz metin olarak ANLATIYOR ("kart YALNIZ
// açık satırda mount olur"), ham tarama onları kod sanardı — ve tersi
// daha kötü: bir gün kod silinip yorum kalsa kapı YEŞİL kalırdı.
const read = (rel: string) => readFileSync(resolve(SRC, rel), 'utf8')
  .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
  .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

/** Bir JSX öğesinin kendi penceresi — prop'lar KOMŞU öğeden okunmasın. */
function element(src: string, tag: string): string {
  const i = src.indexOf(`<${tag}`);
  expect(i, `${tag} öğesi bulunamadı — kapı BAYAT, hedefi yeniden bul`).toBeGreaterThan(-1);
  const j = src.indexOf('/>', i);
  expect(j, `${tag} kendini kapatmıyor — kapı şekli değişmiş`).toBeGreaterThan(-1);
  return src.slice(i, j + 2);
}

describe('exception satırı — yuva bağlı (AnomaliesPage)', () => {
  const src = read('features/anomalies/AnomaliesPage.tsx');

  it('yuvanın üç birimi de import edilmiş ve kanca çağrılmış', () => {
    expect(src).toContain("from '@/components/ai/insightRow'");
    expect(src).toMatch(/useInsightRow\(\)/);
    expect(src).toContain('<InsightRowChip');
    expect(src).toContain('<InsightRowSlot');
  });

  it('çip satırın KENDİ kimliğini taşıyor (fingerprint)', () => {
    expect(element(src, 'InsightRowChip')).toContain('insight.toggle(g.fingerprint)');
    expect(element(src, 'InsightRowChip')).toContain('insight.openId === g.fingerprint');
  });

  it('kart KOŞULLU çizilir — kapalı satır sıfır istek', () => {
    // Yuva ile aynı satırda ya da hemen öncesinde bir açıklık kapısı
    // olmalı; koşulsuz bir yuva her satırda LLM çağrısı demek.
    const i = src.indexOf('<InsightRowSlot');
    const gate = src.slice(Math.max(0, i - 200), i);
    expect(gate, 'InsightRowSlot koşulsuz çiziliyor').toMatch(
      /insight\.openId === g\.fingerprint &&/);
  });

  it('yuva doğru kind + kapatma yolu ile kuruluyor', () => {
    const el = element(src, 'InsightRowSlot');
    expect(el).toContain('kind="exception"');
    expect(el).toContain('id={g.fingerprint}');
    expect(el).toContain('onClose={insight.close}');
    // colSpan admin sütununu SAYAR: yanlış sayı kartı tablonun dışına
    // taşırır (tableLayout:fixed + colgroup).
    expect(el).toMatch(/colSpan=\{isAdmin \? 9 : 8\}/);
  });

  it('worker-yazımı pasif özet satırda ÇİZİLİYOR (sıfır fetch)', () => {
    // ListExceptionGroups ai_summary'yi zaten seçiyor; v0.9.1133'e dek
    // liste onu hiç göstermiyordu. Basım yolu (stripMarkdown) ayrıca
    // markdownSurfaces kapısında.
    expect(src).toMatch(/g\.aiSummary && \(/);
    expect(src).toContain('stripMarkdown(g.aiSummary)');
  });
});

describe('problem satırı — şerit BİRLEŞİK (ProblemsSection)', () => {
  const src = read('features/anomalies/ProblemsSection.tsx');

  it('yuva bağlı ve çip problem kimliğini taşıyor', () => {
    expect(src).toContain("from '@/components/ai/insightRow'");
    expect(src).toMatch(/useInsightRow\(\)/);
    expect(element(src, 'InsightRowChip')).toContain('insight.toggle(p.id)');
    const el = element(src, 'InsightRowSlot');
    expect(el).toContain('kind="problem"');
    expect(el).toContain('id={p.id}');
    // 1 (seçim) + 8 (PROBLEM_COLS) + 2 (Assignee/Triage).
    expect(el).toContain('colSpan={11}');
  });

  it('kart KOŞULLU çizilir', () => {
    const i = src.indexOf('<InsightRowSlot');
    expect(src.slice(Math.max(0, i - 200), i)).toMatch(/insight\.openId === p\.id &&/);
  });

  it('şerit kartı GÖRÜYOR: suppressed + onExpandRequest bağlı', () => {
    const el = element(src, 'RootCauseRibbon');
    expect(el, 'ribbon suppressed almıyor — iki kanıt yüzeyi üst üste açılır')
      .toContain('suppressed={insight.openId === p.id}');
    expect(el, 'onExpandRequest yok — şerit çipi ölü tık olur')
      .toContain('onExpandRequest={insight.close}');
  });

  it('çip şeridin KENDİ satırına giriyor (trailing), kardeş öğe DEĞİL', () => {
    // Kardeş yazımda şerit genişlediğinde flex satırı sığmaz ve çip
    // açılan panelin ALTINA kayar — operatörün az önce tıkladığı düğme
    // yer değiştirir. `trailing` gövdeyi her zaman satırın altında
    // tutuyor. Kap `<div>` de aralarına girmemeli.
    const el = element(src, 'RootCauseRibbon');
    expect(el).toContain('trailing={');
    expect(el).toContain('<InsightRowChip');
    expect(el).not.toContain('<div');
  });

  it('satır map\'i key TAŞIYAN Fragment döndürüyor (MT4)', () => {
    // Satır artık iki <tr> döndürebiliyor; keyless bir <> reconcile'ı
    // yanlış eşleştirir ve sıralama değişince açık kart BAŞKA satırın
    // altında kalır.
    expect(src).toMatch(/import \{ Fragment,/);
    expect(src).toContain('<Fragment key={p.id}>');
  });
});

describe('şerit motoru — bastırma gerçekten gövdeyi kesiyor', () => {
  const src = read('components/RootCauseRibbon.tsx');

  it('gövde `showBody` ile çizilir ve showBody suppressed\'ı okur', () => {
    expect(src).toMatch(/const showBody = open && !suppressed;/);
    expect(src).toContain('{showBody && (');
    // Caret dönüşü de görünen hâli izlemeli; `open` kalırsa kapalı bir
    // gövdenin üstünde açık ok durur.
    expect(src).toContain('transform: showBody ?');
  });

  it('gövde istendiğinde host\'a haber verilir', () => {
    const i = src.indexOf('const onToggle');
    const body = src.slice(i, i + 700);
    expect(body).toMatch(/const next = !showBody;/);
    expect(body).toContain('onExpandRequest?.()');
  });
});

describe('kart TEK kapıdan mount olur', () => {
  it('yuva dışında hiçbir dosya <InsightCard/> çizmiyor', () => {
    // Yuvayı atlayan bir host URL/Esc/tek-kart sözleşmesinin HİÇBİRİNİ
    // almaz — ve tam da bu yüzden sessizce çalışır görünür.
    const walk = (dir: string, out: string[] = []): string[] => {
      for (const e of readdirSync(dir)) {
        const p = join(dir, e);
        if (statSync(p).isDirectory()) walk(p, out);
        else if (/\.tsx$/.test(p) && !/\.test\.tsx$/.test(p)) out.push(p);
      }
      return out;
    };
    const allowed = ['components/ai/insightRow.tsx', 'components/ai/InsightCard.tsx'];
    const offenders = walk(SRC)
      .map(p => relative(SRC, p).split('\\').join('/'))
      .filter(rel => !allowed.includes(rel))
      // Önek çakışmasına kapalı: `<InsightCardFoo` bu kapıyı ALAKASIZ bir
      // dosyada patlatmasın diye sınır aranıyor.
      .filter(rel => /<InsightCard[\s/>]/.test(read(rel)));
    expect(offenders, 'InsightCard yuva dışında mount ediliyor').toEqual([]);
  });
});
