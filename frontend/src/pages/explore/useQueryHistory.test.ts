// @vitest-environment jsdom
//
// v0.9.1359 — CI kırmızıydı, lokal yeşildi. Bu dosya `sessionStorage`/
// `localStorage` KULLANIYOR ve vitest.config.ts ortamı bilinçli `node`
// (2000+ saf test jsdom bedelini ödemesin; dosya başına opt-in). Node 25
// bu global'leri yerleşik taşıyor, CI'ın Node 22.si TAŞIMIYOR — yani test
// lokalde ÇALIŞMA ZAMANI SÜRÜMÜ sayesinde geçiyordu, kendi hakkıyla değil.
// jsdom onları sürümden bağımsız sağlar.
import { describe, it, expect } from 'vitest';
import {
  mergeHistory,
  parseHistory,
  historyRelTime,
  historyItemView,
  MAX_HISTORY,
  HISTORY_LABEL_MAX,
  type QueryHistoryEntry,
} from './useQueryHistory';

// explore-v2 Phase-1 — pins the recent-queries ("Son sorgular") ring
// semantics. The ring is the entry-screen's "jump back to what I was
// looking at" affordance; if merge/dedupe/cap or the corrupt-JSON
// tolerance regresses, the question-card screen either loses history
// silently or throws on a poisoned localStorage value and white-screens
// the page. Pure-function table tests so the math is guarded without a DOM.

const e = (desc: string, state: unknown = '?x', tm = 1): QueryHistoryEntry =>
  ({ desc, state, tm });

describe('mergeHistory', () => {
  it('prepends a fresh entry (newest first)', () => {
    const out = mergeHistory([e('a')], e('b'));
    expect(out.map(x => x.desc)).toEqual(['b', 'a']);
  });

  it('dedupes by desc — re-running a query bumps it to the front', () => {
    const start = [e('a'), e('b'), e('c')];
    const out = mergeHistory(start, e('b', '?new', 99));
    expect(out.map(x => x.desc)).toEqual(['b', 'a', 'c']);
    // bumped entry carries the new payload + tm, not the stale one.
    expect(out[0].state).toBe('?new');
    expect(out[0].tm).toBe(99);
  });

  it(`caps at MAX_HISTORY (${MAX_HISTORY})`, () => {
    let acc: QueryHistoryEntry[] = [];
    for (const d of ['a', 'b', 'c', 'd', 'e', 'f']) acc = mergeHistory(acc, e(d));
    expect(acc).toHaveLength(MAX_HISTORY);
    // newest MAX_HISTORY survive, oldest dropped.
    expect(acc.map(x => x.desc)).toEqual(['f', 'e', 'd', 'c']);
  });

  it('skips empty descriptions (no blank rows in the list)', () => {
    const start = [e('a')];
    expect(mergeHistory(start, e(''))).toEqual(start);
  });

  it('dedupe + cap together: re-running keeps length within the cap', () => {
    const start = [e('a'), e('b'), e('c'), e('d')]; // already full
    const out = mergeHistory(start, e('c', '?z', 5));
    expect(out).toHaveLength(MAX_HISTORY);
    expect(out.map(x => x.desc)).toEqual(['c', 'a', 'b', 'd']);
  });
});

describe('parseHistory', () => {
  const cases: { name: string; raw: string | null; want: string[] }[] = [
    { name: 'null input → empty', raw: null, want: [] },
    { name: 'empty string → empty', raw: '', want: [] },
    { name: 'corrupt JSON → empty (tolerant, never throws)', raw: '{not json', want: [] },
    { name: 'non-array JSON → empty', raw: '{"desc":"a","tm":1}', want: [] },
    { name: 'array of valid entries', raw: JSON.stringify([e('a'), e('b')]), want: ['a', 'b'] },
    {
      name: 'drops rows missing required fields',
      raw: JSON.stringify([{ desc: 'a', state: '?x', tm: 1 }, { state: '?y' }, { desc: 'c', tm: 3 }]),
      want: ['a', 'c'],
    },
    {
      name: 'caps an over-long persisted array',
      raw: JSON.stringify([e('a'), e('b'), e('c'), e('d'), e('e'), e('f')]),
      want: ['a', 'b', 'c', 'd'],
    },
    {
      // Phase-1 review finding: mergeHistory refuses to WRITE an empty
      // desc, but a hand-edited/poisoned blob row used to pass the read
      // path and render a blank "Son sorgular" row.
      name: 'drops rows with empty desc',
      raw: JSON.stringify([{ desc: '', state: '?x', tm: 1 }, e('a')]),
      want: ['a'],
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(parseHistory(c.raw).map(x => x.desc)).toEqual(c.want);
    });
  }

  it('round-trips a merged ring through JSON', () => {
    let acc: QueryHistoryEntry[] = [];
    for (const d of ['a', 'b', 'c']) acc = mergeHistory(acc, e(d));
    const roundTripped = parseHistory(JSON.stringify(acc));
    expect(roundTripped).toEqual(acc);
  });
});

// ── v0.9.849 — halkanın GÖRÜNEN hâli ────────────────────────────────────────
//
// Halka v0.9.562'den beri yazıyordu ama okuyanı yoktu (giriş ekranı
// kaldırılınca tek tüketicisi gitti). Görünür olduğu andan itibaren iki
// şey sessizce yanlış olabilir: kırpma (bozuk glif) ve göreli zaman
// (geri alınmış saatte "-3 dk önce"). İkisi de burada tabloda.

describe('historyRelTime', () => {
  const now = 1_754_000_000_000;
  const cases: Array<{ ago: number; want: string; why: string }> = [
    { ago: 0, want: 'şimdi', why: 'az önce kaydedildi' },
    { ago: 59_000, want: 'şimdi', why: 'bir dakikanın altı' },
    { ago: 60_000, want: '1 dk önce', why: 'dakika eşiği' },
    { ago: 59 * 60_000, want: '59 dk önce', why: 'saatin hemen altı' },
    { ago: 3_600_000, want: '1 sa önce', why: 'saat eşiği' },
    { ago: 23 * 3_600_000, want: '23 sa önce', why: 'günün hemen altı' },
    { ago: 86_400_000, want: '1 gün önce', why: 'gün eşiği' },
    { ago: 9 * 86_400_000, want: '9 gün önce', why: 'eski kayıt' },
  ];
  for (const c of cases) {
    it(`${c.why} → "${c.want}"`, () => {
      expect(historyRelTime(now - c.ago, now)).toBe(c.want);
    });
  }

  it('GELECEK damgalı kayıt "şimdi" — saati geri alınmış makinede "-3 dk önce" yazmaz', () => {
    expect(historyRelTime(now + 10 * 60_000, now)).toBe('şimdi');
  });
});

describe('historyItemView', () => {
  const now = 1_754_000_000_000;

  it('kısa özet AYNEN geçer, tam metin title\'da', () => {
    const v = historyItemView(e('A: rate · checkout', '?q=1', now), now);
    expect(v.text).toBe('A: rate · checkout');
    expect(v.title).toContain('A: rate · checkout');
    expect(v.title).toContain('şimdi');
  });

  it('uzun özet kırpılır ve … ile biter (sınır dahil)', () => {
    const long = 'x'.repeat(HISTORY_LABEL_MAX + 40);
    const v = historyItemView(e(long, '?q=1', now), now);
    expect(Array.from(v.text)).toHaveLength(HISTORY_LABEL_MAX);
    expect(v.text.endsWith('…')).toBe(true);
    // Kırpma bilgi KAYBI değil GİZLEME: tam metin title'da duruyor.
    expect(v.title).toContain(long);
  });

  it('tam sınırdaki özet KIRPILMAZ', () => {
    const exact = 'y'.repeat(HISTORY_LABEL_MAX);
    expect(historyItemView(e(exact, '?q=1', now), now).text).toBe(exact);
  });

  it('kırpma KOD NOKTASI bazında — surrogate çifti ikiye bölünmez', () => {
    // Her biri UTF-16'da İKİ birim tutan glifler; slice() ile kırpılsaydı
    // metnin sonunda yarım bir surrogate (bozuk glif) kalırdı.
    const emoji = '🚀'.repeat(HISTORY_LABEL_MAX + 5);
    const v = historyItemView(e(emoji, '?q=1', now), now);
    expect(Array.from(v.text)).toHaveLength(HISTORY_LABEL_MAX);
    // Yarım surrogate kalmadı: geri çevirim kayıpsız.
    expect(v.text).toBe(Array.from(v.text).join(''));
    expect(v.text.endsWith('…')).toBe(true);
  });

  it('uygulanabilir kayıt arama dizesini taşır', () => {
    expect(historyItemView(e('d', '?q=abc', now), now).search).toBe('?q=abc');
  });

  const unusable: Array<{ state: unknown; why: string }> = [
    { state: '', why: 'boş dize' },
    { state: 'q=abc', why: "'?' ile başlamıyor" },
    { state: 42, why: 'sayı' },
    { state: null, why: 'null' },
    { state: { q: 'x' }, why: 'nesne (Phase-1 öncesi bir şekil)' },
  ];
  for (const c of unusable) {
    it(`uygulanamaz kayıt (${c.why}) → search boş`, () => {
      expect(historyItemView(e('d', c.state, now), now).search).toBe('');
    });
  }

  // Alan tamamen EKSİK olabilir (parseHistory state'i doğrulamaz, bilerek:
  // Phase-1'den beri opak). Varsayılan parametreye düşmemek için kayıt
  // burada elle kuruluyor.
  it('uygulanamaz kayıt (state alanı yok) → search boş', () => {
    const bare = { desc: 'd', tm: now } as QueryHistoryEntry;
    expect(historyItemView(bare, now).search).toBe('');
  });
});
