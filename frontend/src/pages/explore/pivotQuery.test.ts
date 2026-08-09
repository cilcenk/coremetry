import { describe, it, expect } from 'vitest';
import { pivotQuery } from './pivotQuery';
import { blankQuery, seriesGroupPairs, type BuilderQuery } from './model';
import type { FilterExpr } from '@/lib/types';

// v0.9.848 — GroupTable satırından pivot.
//
// Pivot bir FİLTRE ÜRETİCİSİDİR ve yanlış ürettiğinde belirtisi "hata" değil
// "başka veri"dir: panel yine çizer, sayılar yine akar, yalnız operatörün
// sandığı küme değildir. Bu yüzden çip birleşme/çakışma vakalarının hepsi
// burada tabloda — özellikle aynı anahtarda eski bir çipin üzerine yazma ve
// karşıt (=/!=) çiplerin temizlenmesi.

const f = (k: string, v: string, op: FilterExpr['op'] = '='): FilterExpr =>
  ({ k, op, v: [v] });

const q = (over: Partial<BuilderQuery>): BuilderQuery =>
  ({ ...blankQuery('A'), ...over });

describe('pivotQuery — only (yalnız bu değere daralt)', () => {
  it('çipi ekler ve o anahtarı splitBy\'dan DÜŞÜRÜR', () => {
    const out = pivotQuery(
      q({ splitBy: ['service.name'] }), [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(out).not.toBeNull();
    expect(out!.filters).toEqual([f('service.name', 'checkout')]);
    // Daraltılan boyut artık tek değerli — splitte bırakmak tek serilik bir
    // "fan-out" olurdu.
    expect(out!.splitBy).toEqual([]);
  });

  it('mevcut çipleri KORUR, yalnız kendi anahtarını ezer', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name'],
      filters: [f('kind', 'server'), f('service.name', 'cart')],
    }), [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(out!.filters).toEqual([f('kind', 'server'), f('service.name', 'checkout')]);
  });

  it('aynı anahtardaki ÇOKLU eski `=` çipinin hepsi düşer (tek daraltma kalır)', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name'],
      filters: [f('service.name', 'a'), f('service.name', 'b')],
    }), [{ k: 'service.name', v: 'c' }], 'only');
    expect(out!.filters).toEqual([f('service.name', 'c')]);
  });

  it('ÇELİŞEN `!= v` çipi düşer (aynı anda hem yalnız-bu hem bu-hariç olamaz)', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name'],
      filters: [f('service.name', 'checkout', '!=')],
    }), [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(out!.filters).toEqual([f('service.name', 'checkout')]);
  });

  it('BAŞKA değerdeki `!=` çipi KALIR (çelişki yok, daralma birikir)', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name', 'name'],
      filters: [f('name', 'healthz', '!=')],
    }), [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(out!.filters).toEqual([f('name', 'healthz', '!='), f('service.name', 'checkout')]);
    // Yalnız pivotlanan anahtar splitten düşer; diğeri durur.
    expect(out!.splitBy).toEqual(['name']);
  });

  it('çok boyutlu satır: HER çift için çip, HER anahtar splitten düşer', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name', 'name'],
    }), [{ k: 'service.name', v: 'checkout' }, { k: 'name', v: 'GET /cart' }], 'only');
    expect(out!.filters).toEqual([f('service.name', 'checkout'), f('name', 'GET /cart')]);
    expect(out!.splitBy).toEqual([]);
  });

  it('IN çipi (çok değerli) ezilmez — pivotun tekil çipiyle çelişmez', () => {
    const inChip: FilterExpr = { k: 'kind', op: 'IN', v: ['server', 'client'] };
    const out = pivotQuery(q({
      splitBy: ['service.name'], filters: [inChip],
    }), [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(out!.filters).toEqual([inChip, f('service.name', 'checkout')]);
  });

  it('zaten daraltılmışsa null (çip sırası oynamaz → cache MISS olmaz)', () => {
    const before = q({ splitBy: [], filters: [f('service.name', 'checkout')] });
    expect(pivotQuery(before, [{ k: 'service.name', v: 'checkout' }], 'only')).toBeNull();
  });

  it('çip aynı ama split hâlâ duruyorsa null DEĞİL (split düşürülür)', () => {
    const before = q({ splitBy: ['service.name'], filters: [f('service.name', 'checkout')] });
    const out = pivotQuery(before, [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(out).not.toBeNull();
    expect(out!.splitBy).toEqual([]);
  });

  it('girdiyi MUTASYONA uğratmaz', () => {
    const before = q({ splitBy: ['service.name'], filters: [f('kind', 'server')] });
    pivotQuery(before, [{ k: 'service.name', v: 'checkout' }], 'only');
    expect(before.filters).toEqual([f('kind', 'server')]);
    expect(before.splitBy).toEqual(['service.name']);
  });
});

describe('pivotQuery — exclude (bu değeri hariç tut)', () => {
  it('`!=` çipi ekler, splitBy AYNEN kalır (kalanı karşılaştırmaya devam)', () => {
    const out = pivotQuery(
      q({ splitBy: ['service.name'] }), [{ k: 'service.name', v: 'noisy' }], 'exclude');
    expect(out!.filters).toEqual([f('service.name', 'noisy', '!=')]);
    expect(out!.splitBy).toEqual(['service.name']);
  });

  it('birden çok hariç birikir (AND\'li dışlama anlamlıdır)', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name'], filters: [f('service.name', 'a', '!=')],
    }), [{ k: 'service.name', v: 'b' }], 'exclude');
    expect(out!.filters).toEqual([
      f('service.name', 'a', '!='), f('service.name', 'b', '!='),
    ]);
  });

  it('ÇELİŞEN `= v` çipi düşer (boş panelin sebebi hiçbir yerde yazmazdı)', () => {
    const out = pivotQuery(q({
      splitBy: ['service.name'], filters: [f('service.name', 'checkout')],
    }), [{ k: 'service.name', v: 'checkout' }], 'exclude');
    expect(out!.filters).toEqual([f('service.name', 'checkout', '!=')]);
  });

  it('zaten hariç tutulmuşsa null', () => {
    const before = q({ filters: [f('service.name', 'noisy', '!=')] });
    expect(pivotQuery(before, [{ k: 'service.name', v: 'noisy' }], 'exclude')).toBeNull();
  });

  it('ÇOK BOYUTLU satırda null — kombinasyonun değili AND çiple yazılamaz', () => {
    const out = pivotQuery(q({ splitBy: ['service.name', 'name'] }),
      [{ k: 'service.name', v: 'checkout' }, { k: 'name', v: 'GET /cart' }], 'exclude');
    expect(out).toBeNull();
  });
});

describe('pivotQuery — uygulanamaz durumlar', () => {
  it('GRUPLU (OR) filtre açıkken null — düz çip sorguya hiç girmezdi', () => {
    const grouped = q({
      splitBy: ['service.name'],
      filterGroup: { join: 'OR', filters: [f('kind', 'server'), f('kind', 'client')] },
    });
    for (const mode of ['only', 'exclude'] as const) {
      expect(pivotQuery(grouped, [{ k: 'service.name', v: 'checkout' }], mode)).toBeNull();
    }
  });

  it('DÜZ-AND grubu pivotu ENGELLEMEZ (o grup zaten çip satırıyla aynı şey)', () => {
    const flat = q({
      splitBy: ['service.name'],
      filterGroup: { join: 'AND', filters: [f('kind', 'server')] },
    });
    expect(pivotQuery(flat, [{ k: 'service.name', v: 'checkout' }], 'only')).not.toBeNull();
  });

  it('boş çift listesinde null', () => {
    expect(pivotQuery(q({}), [], 'only')).toBeNull();
    expect(pivotQuery(q({}), [], 'exclude')).toBeNull();
  });
});

// seriesGroupPairs — pivotun GİRDİSİ. seriesGroupLabel ile aynı band soyma
// kuralını paylaşmak zorunda: ayrışırlarsa satırda okunan etiket ile o
// satırdan üretilen filtre farklı boyutlara işaret eder.
describe('seriesGroupPairs', () => {
  it('splitBy ile groupKey\'i sırayla eşler', () => {
    expect(seriesGroupPairs(q({ splitBy: ['service.name', 'name'] }), ['checkout', 'GET /cart']))
      .toEqual([{ k: 'service.name', v: 'checkout' }, { k: 'name', v: 'GET /cart' }]);
  });

  it('band: quantile etiketi SON elemandan soyulur (seriesGroupLabel ile aynı)', () => {
    expect(seriesGroupPairs(q({ agg: 'band', splitBy: ['service.name'] }), ['checkout', 'p95']))
      .toEqual([{ k: 'service.name', v: 'checkout' }]);
  });

  it('splitBy boşken çift üretmez (splitsiz panelde pivot yok)', () => {
    expect(seriesGroupPairs(q({ splitBy: [] }), [])).toEqual([]);
  });

  it('adlandırılamayan fazla groupKey elemanı ATLANIR (uydurma anahtar yok)', () => {
    expect(seriesGroupPairs(q({ splitBy: ['service.name'] }), ['checkout', 'artık']))
      .toEqual([{ k: 'service.name', v: 'checkout' }]);
  });
});
