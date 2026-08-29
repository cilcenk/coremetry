// codeQuote.test.ts — v0.10.165 sözleşmesi (codeQuote.ts başlığı).
import { describe, it, expect } from 'vitest';
import { stripMarker, parseFileHeader, splitHeader, hasGutter, gutterLines, resourceLabel, isStackTrace, foldStack } from './codeQuote';

describe('dosya başlığı + oluk', () => {
  it('// yol:a-b (ve # / -- / /* */) başlığı çözülür; bozuk aralık reddedilir', () => {
    expect(parseFileHeader('// src/main/java/com/x/Handler.java:238-249')).toEqual({ path: 'src/main/java/com/x/Handler.java', from: 238, to: 249 });
    expect(parseFileHeader('-- mappers/LedgerEntryMapper.xml:31-40')).toEqual({ path: 'mappers/LedgerEntryMapper.xml', from: 31, to: 40 });
    expect(parseFileHeader('/* a/b.sql:1-3 */')).toEqual({ path: 'a/b.sql', from: 1, to: 3 });
    expect(parseFileHeader('// Handler.java:50-40')).toBeNull();
    // v0.10.154 gerçek yakalaması: tek satır, aralıksız; ve `(insertBatch)` eki
    expect(parseFileHeader('// src/main/java/com/x/Handler.java:32')).toEqual({ path: 'src/main/java/com/x/Handler.java', from: 32, to: 32 });
    expect(parseFileHeader('// Foo.java:240-252 (insertBatch)')).toEqual({ path: 'Foo.java', from: 240, to: 252 });
    expect(parseFileHeader('int x = 1;')).toBeNull();
    expect(parseFileHeader(undefined)).toBeNull();
  });
  it('oluk yalnız TÜM boş-olmayan satırlar N| taşıyorsa; >>> soyulur ve vurgulanır', () => {
    const lines = ['  244| try {', '>>> 246|   throw x;', '', '  247| }'];
    expect(hasGutter(lines)).toBe(true);
    expect(gutterLines(lines)).toEqual([
      { no: 244, text: 'try {', hl: false },
      { no: 246, text: '  throw x;', hl: true },
      { no: null, text: '', hl: false },
      { no: 247, text: '}', hl: false },
    ]);
    expect(hasGutter(['244| a', 'b'])).toBe(false); // küçük model öneki düşürdü → düz blok, numara uydurulmaz
    expect(hasGutter(['244| a'])).toBe(false);      // tek satır
  });
  it('stripMarker codeLineMark ile aynı', () => {
    expect(stripMarker('>>> 32| throw x;')).toEqual({ text: '32| throw x;', hl: true });
    expect(stripMarker('    33| }')).toEqual({ text: '    33| }', hl: false });
  });
  it('splitHeader: çözülen başlık → ref; çözülmeyen yorum + oluklu gövde → düz başlık, oluk korunur; öteki → aynen', () => {
    expect(splitHeader(['// a/B.java:1-2', '1| x', '2| y'])).toEqual({ ref: { path: 'a/B.java', from: 1, to: 2 }, headerText: null, body: ['1| x', '2| y'] });
    expect(splitHeader(['// imza (satır 246, pencere dışı): insertBatch', '>>> 246| m.insert(e);', '247| }'])).toEqual({ ref: null, headerText: 'imza (satır 246, pencere dışı): insertBatch', body: ['>>> 246| m.insert(e);', '247| }'] });
    expect(splitHeader(['int x = 1;', '246| a', '247| b'])).toEqual({ ref: null, headerText: null, body: ['int x = 1;', '246| a', '247| b'] });
    expect(splitHeader(['// yorum', 'düz kod'])).toEqual({ ref: null, headerText: null, body: ['// yorum', 'düz kod'] });
  });
  it('kaynak penceresi etiketi uzantıdan; yalnız *Mapper*.xml mapper', () => {
    expect(resourceLabel('LedgerEntryMapper.xml')).toBe('kaynak penceresi (mapper)');
    expect(resourceLabel('mappers/ledger-mapper.xml')).toBe('kaynak penceresi (mapper)');
    expect(resourceLabel('pom.xml')).toBe('kaynak penceresi (XML)');
    expect(resourceLabel('q.SQL')).toBe('kaynak penceresi (SQL)');
    expect(resourceLabel('Handler.java')).toBeNull();
    expect(resourceLabel(undefined)).toBeNull();
  });
});

describe('stack katlama', () => {
  const fw = (n: number) => Array.from({ length: n }, (_, i) => `\tat org.springframework.web.Filter${i}.doFilter(Filter.java:${100 + i})`);
  it('isStackTrace ≥ 6 kare', () => {
    expect(isStackTrace(['java.lang.NullPointerException: x', ...fw(6)])).toBe(true);
    expect(isStackTrace(['a', 'b', '\tat x.y(Z.java:1)'])).toBe(false);
  });
  it('baş en derin UYGULAMA karesini kapsar (framework kareleri sayılmaz); 8..24; kalan ≤3 ise katlanmaz', () => {
    const lines = ['java.util.concurrent.TimeoutException', ...fw(3), '\tat com.payments.orch.saga.PaymentSaga.awaitLedger(PaymentSaga.java:124)', ...fw(2),
      '\tat com.payments.orch.PaymentController.authorize(PaymentController.java:57)', ...fw(40)];
    const f = foldStack(lines);
    expect(f.head.length).toBe(8); // uygulama karesi 7. satırda (0-tabanlı 6) → max(8, 7) = 8
    expect(f.rest.length).toBe(lines.length - 8);
    const deep = ['E', ...fw(12), '\tat com.x.App.run(App.java:9)', ...fw(30)];
    expect(foldStack(deep).head.length).toBe(14); // en derin uygulama karesi 14. satır
    expect(foldStack(['E', ...fw(9)]).rest.length).toBe(0); // 10 satır: kalan 2 ≤ 3 → katlanmaz
    expect(foldStack(['E', ...fw(60)]).head.length).toBe(8);
  });
});
