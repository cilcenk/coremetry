// kqlFieldToken — UX denetimi F4 / Ö16 regresyon testleri (v0.9.955).
//
// ORİJİNAL BELİRTİ: /logs'ta alan ADI keşfi yoktu. KQL kutusu `field:`
// yazıldıktan SONRA değerleri tamamlıyor ama hangi alanların VAR
// OLDUĞUNU göstermiyordu. Operatör CHANNEL_CODE'u aramak için ES'teki
// tam yazımı (verbatim mi `attributes.` altında mı) DIŞARIDAN bilmek
// zorundaydı — ve yanlış yazımın bedeli sessizdi: sorgu koşuyor, sıfır
// satır dönüyor, bu da "böyle log yok" diye okunuyordu.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { detectFieldNameToken, rankFieldNames, applyFieldName, fieldNameHint } from './kqlFieldToken';

describe('detectFieldNameToken — konum tespiti', () => {
  it('yazılmakta olan sözcüğü yakalar', () => {
    expect(detectFieldNameToken('chan', 4)).toEqual({ prefix: 'chan', start: 0, end: 4 });
  });

  it('boşluktan sonraki sözcük kendi token’ı', () => {
    const t = detectFieldNameToken('level:error chan', 16);
    expect(t).toEqual({ prefix: 'chan', start: 12, end: 16 });
  });

  it("':' VARSA null — orası DEĞER konumu", () => {
    // Alan adı önermek, operatörün yazdığı değeri ezerdi; değer
    // tamamlaması detectFieldToken'ın işi.
    expect(detectFieldNameToken('level:err', 9)).toBeNull();
    expect(detectFieldNameToken('level:', 6)).toBeNull();
  });

  it('TIRNAK İÇİNDE null — orada serbest metin aranıyor', () => {
    expect(detectFieldNameToken('msg:"time out', 13)).toBeNull();
    expect(detectFieldNameToken('"payment fail', 13)).toBeNull();
  });

  it('kapanmış tırnaktan SONRA yine çalışır', () => {
    const t = detectFieldNameToken('msg:"x" chan', 12);
    expect(t?.prefix).toBe('chan');
  });

  it('boş önek null — boş kutuda mapping dökümü açılmaz', () => {
    expect(detectFieldNameToken('', 0)).toBeNull();
    expect(detectFieldNameToken('level:x ', 8)).toBeNull();
  });

  it('parantez sınırları token’ı böler', () => {
    expect(detectFieldNameToken('(chan', 5)).toEqual({ prefix: 'chan', start: 1, end: 5 });
  });

  it('imleç sözcüğün ORTASINDAysa yalnız soldaki kısım önek', () => {
    // "channel" içinde 4. karakterde: önek "chan", sağdaki "nel" korunur.
    expect(detectFieldNameToken('channel', 4)).toEqual({ prefix: 'chan', start: 0, end: 4 });
  });

  it('aralık dışı imleç güvenli', () => {
    expect(detectFieldNameToken('abc', -1)).toBeNull();
    expect(detectFieldNameToken('abc', 99)).toBeNull();
  });
});

describe('rankFieldNames — sıralama sözleşmesi', () => {
  const fields = [
    'attributes.CHANNEL_CODE', 'channel', 'level', 'service.name',
    'trace.id', 'body', 'resource.channel.id',
  ];

  it('önce BAŞLAYANLAR, sonra İÇERENLER', () => {
    const out = rankFieldNames(fields, 'chan');
    expect(out[0]).toBe('channel');
    // CHANNEL_CODE önekle başlamıyor ama içeriyor — Ö16'nın ASIL vakası:
    // operatör ES yolunu bilmiyor, yalnız adın parçasını biliyor.
    expect(out).toContain('attributes.CHANNEL_CODE');
    expect(out.indexOf('channel')).toBeLessThan(out.indexOf('attributes.CHANNEL_CODE'));
  });

  it('büyük/küçük harf duyarsız', () => {
    expect(rankFieldNames(fields, 'CHANNEL')).toContain('channel');
    expect(rankFieldNames(fields, 'channel_code')).toContain('attributes.CHANNEL_CODE');
  });

  it('grup içi alfabetik ve KARARLI — poll’de sıra oynamaz', () => {
    const a = rankFieldNames(fields, 'c');
    const b = rankFieldNames([...fields].reverse(), 'c');
    expect(a).toEqual(b);
  });

  it('limit uygulanır', () => {
    expect(rankFieldNames(fields, '', 3)).toHaveLength(3);
  });

  it('eşleşme yoksa boş', () => {
    expect(rankFieldNames(fields, 'zzz')).toEqual([]);
  });
});

describe('applyFieldName — ":" EKLER', () => {
  it('alanı yazar ve iki nokta ekler', () => {
    const t = detectFieldNameToken('chan', 4)!;
    expect(applyFieldName('chan', t, 'channel')).toEqual({ text: 'channel:', cursor: 8 });
  });

  it('iki nokta ŞART — alan adı tek başına SERBEST METİNDİR', () => {
    // ':' olmadan tamamlama, operatörü tam da kaçındığı hâle (yanlış
    // sorgu → sıfır satır) bırakırdı. Üstelik ':' değer tamamlamasının
    // tetikleyicisi: tek seçimle zincirin ikinci halkası açılıyor.
    const t = detectFieldNameToken('lev', 3)!;
    expect(applyFieldName('lev', t, 'level').text.endsWith(':')).toBe(true);
  });

  it('etrafındaki metni KORUR', () => {
    const t = detectFieldNameToken('level:error chan', 16)!;
    expect(applyFieldName('level:error chan', t, 'channel').text).toBe('level:error channel:');
  });

  it('sağdaki kalıntı korunur (sözcük ortasında seçim)', () => {
    const t = detectFieldNameToken('chanX', 4)!;
    expect(applyFieldName('chanX', t, 'channel').text).toBe('channel:X');
  });
});

// v0.9.970 (Ö16 ikinci tur) — ORİJİNAL BELİRTİ: /api/logs/fields katalogu
// 500 yolda ALFABETİK olarak kırpılıyor ve dinamik mapping'de dört haneli
// alan sayısı rutin (ListFieldsBounded'ın kendi yorumu). Kırpılan bir alanı
// arayan operatör boş liste görüyor, bunu "böyle alan yok" diye okuyordu —
// v0.9.955'in kapatmak için var olduğu yanlış okumanın aynısı, bir katman
// yukarıda. Yan panel dürüsttü ("first N of M"), açılır liste değildi.
describe('fieldNameHint — katalog kırpıklığı dürüstlüğü', () => {
  const cases: Array<{
    name: string; matches: number; shown: number; total?: number;
    want: ReturnType<typeof fieldNameHint>['kind'];
  }> = [
    { name: 'total bilinmiyor → sus (uydurma yok)', matches: 0, shown: 500, total: undefined, want: 'none' },
    { name: 'katalog TAM, eşleşme var → sus', matches: 3, shown: 120, total: 120, want: 'none' },
    { name: 'katalog TAM, eşleşme yok → sus (gerçekten yok)', matches: 0, shown: 120, total: 120, want: 'none' },
    { name: 'kırpık + eşleşme var → liste eksik olabilir', matches: 4, shown: 500, total: 4200, want: 'clipped' },
    { name: 'kırpık + eşleşme YOK → ASIL vaka, söylemek şart', matches: 0, shown: 500, total: 4200, want: 'clipped-no-match' },
    { name: 'total < shown (tutarsız girdi) → sus', matches: 0, shown: 500, total: 10, want: 'none' },
    { name: 'total tam sınırda (== shown) → kırpık DEĞİL', matches: 0, shown: 500, total: 500, want: 'none' },
    { name: 'bir tek alan kırpılmış olsa bile söyle', matches: 0, shown: 500, total: 501, want: 'clipped-no-match' },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(fieldNameHint(c.matches, c.shown, c.total).kind).toBe(c.want);
    });
  }

  it('kırpıkken shown/total TAŞINIR — başlık gerçek sayıları basar', () => {
    const h = fieldNameHint(0, 500, 4200);
    expect(h).toEqual({ kind: 'clipped-no-match', shown: 500, total: 4200 });
  });
});

describe('kablolama — SIFIR ek ES maliyeti (v0.9.955)', () => {
  const SRC = join(__dirname, '..');
  const kql = readFileSync(join(SRC, 'components/KqlSearchInput.tsx'), 'utf8');
  const logs = readFileSync(join(SRC, 'pages/Logs.tsx'), 'utf8');

  it('alan listesi PROP olarak gelir — kutu kendi fetch’ini AÇMAZ', () => {
    // Operatörün "elastic api kullanımını çok artırma" şartı: liste
    // zaten LogFieldsPanel için çekiliyor, ikinci bir tur yasak.
    expect(kql).toMatch(/fields\?: string\[\]/);
    expect(kql).not.toMatch(/api\.logsFields\(/);
  });

  it('/logs mevcut state’i geçiriyor — ikinci bir istek yok', () => {
    expect(logs).toMatch(/fields=\{fields\}/);
    expect((logs.match(/api\.logsFields\(/g) ?? []).length).toBeLessThanOrEqual(1);
  });

  it('iki öneri kaynağı ÇAKIŞMAZ — değer token’ı varken ad önerilmez', () => {
    expect(kql).toMatch(/token \? null : detectFieldNameToken\(value, cursor\)/);
  });

  // v0.9.970 — kırpıklık uyarısının ULAŞILABİLİR olduğunu çivile.
  it('/logs gerçek `total`ı geçiriyor — kırpıklık uydurulmuyor', () => {
    expect(logs).toMatch(/fieldsTotal=\{fieldsTotal\}/);
  });

  it('açılır liste SIFIR satırla da açılabiliyor — yoksa uyarı hiç görünmez', () => {
    // Kritik vaka (clipped-no-match) tam olarak "gösterilecek satır YOK
    // ama söylenecek söz VAR" hâli. Guard yeniden `items.length > 0`e
    // dönerse uyarı sessizce ölür ve kusur geri gelir; klavye guard'ı
    // ayrı satırda ve bilerek dar kalıyor (boşken seçim yapılamaz).
    expect(kql).toContain('open && (items.length > 0 || hintOnly)');
  });
});
