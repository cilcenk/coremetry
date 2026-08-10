import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { esErrorKey } from './esErrorKey';
import type { ESQueryError } from '@/lib/types';

// esErrorKey.test.ts — v0.9.876.
//
// Kaynak: tutarlılık denetimi (docs/audit/frontend-consistency-audit.md) BT14,
// icra denetiminin R4 riski.
//
// SEMPTOM (dönüşümün AÇACAĞI kusur): AdminElastic'in "Recent query errors"
// tablosu genişletilebilir satırlar taşıyor ve açık satırı DİZİ İNDEKSİYLE
// tutuyordu (`open === i`). Sıralama yokken indeks ile satır birebirdi.
// useDataTable sıralamayı getirince o varsayım çöküyor: `dt.sortedRows`
// sırayı değiştirir, `i` başka bir hataya karşılık gelir ve operatör bir
// satırı açtığında BAŞKA BİR SORGUNUN gövdesi görünür.
//
// Bu sessiz bir kusur: ekranda hiçbir şey "bozulmuyor", sadece yanlış JSON
// gösteriliyor — üstelik tablonun tek varlık sebebi "hangi sorgu patladı"
// sorusunu cevaplamak.
//
// KORUNAN ÖZELLİK: anahtar SATIRDAN türetiliyor ve gerçekçi çakışma
// senaryolarında ayırt edici kalıyor.

const base: ESQueryError = {
  at: 1_770_000_000_000, op: 'search', index: 'logs-2026.08.10',
  query: '{"query":{"match_all":{}}}', status: 500, error: 'circuit_breaking_exception',
};

describe('esErrorKey — BT14/R4 satır kimliği', () => {
  it('aynı satır aynı anahtarı verir (kararlı)', () => {
    expect(esErrorKey(base)).toBe(esErrorKey({ ...base }));
  });

  it('AYNI MİLİSANİYE farklı op → farklı anahtar', () => {
    // Gerçek senaryo: bir _msearch'ün alt sorguları aynı damgayı taşır.
    // `at` tek başına anahtar olsaydı burada çakışırdı.
    expect(esErrorKey(base)).not.toBe(esErrorKey({ ...base, op: 'msearch count-patterns' }));
  });

  it('aynı ms + aynı op, farklı index → farklı anahtar', () => {
    expect(esErrorKey(base)).not.toBe(esErrorKey({ ...base, index: 'logs-2026.08.09' }));
  });

  it('aynı ms + op + index, farklı query → farklı anahtar', () => {
    expect(esErrorKey(base)).not.toBe(esErrorKey({ ...base, query: '{"query":{"term":{"a":1}}}' }));
  });

  it('gerçekçi bir toplu partide anahtarlar TEKİL', () => {
    // 40 hata: 10 farklı damga × 2 op × 2 index. İndeks-tabanlı kimliğin
    // çöktüğü, anahtar-tabanlı kimliğin ayakta kaldığı şekil.
    const rows: ESQueryError[] = [];
    for (let t = 0; t < 10; t++)
      for (const op of ['search', 'histogram'])
        for (const idx of ['logs-a', 'logs-b'])
          rows.push({ ...base, at: base.at + t, op, index: idx });
    const keys = new Set(rows.map(esErrorKey));
    expect(keys.size).toBe(rows.length);
  });

  it('sıralama anahtarı DEĞİŞTİRMİYOR — R4\'ün özü', () => {
    const rows: ESQueryError[] = [
      { ...base, at: 3, error: 'üç' },
      { ...base, at: 1, error: 'bir' },
      { ...base, at: 2, error: 'iki' },
    ];
    const before = rows.map(esErrorKey);
    const sorted = [...rows].sort((a, b) => b.at - a.at);
    // Aynı satır, sırası değişse de aynı anahtarı taşıyor: indeks-tabanlı
    // kimliğin veremediği garanti tam olarak bu.
    for (const r of sorted) expect(before).toContain(esErrorKey(r));
  });
});

describe('AdminElastic açık-satır durumu indekse geri dönmedi', () => {
  const src = readFileSync(resolve(__dirname, '../AdminElastic.tsx'), 'utf8');

  it('open state string, number DEĞİL', () => {
    expect(src).toContain("useState<string | null>(null)");
    expect(src).not.toMatch(/setOpen\(open === i \?/);
  });

  it('satır anahtarı esErrorKey\'den geliyor', () => {
    expect(src).toContain('esErrorKey(e)');
    expect(src).toContain('open === k');
  });
});
