// sourceHref — GroupTable satırından kaynağa iniş kapısı (v0.9.930)
//
// Ne çiviliyor: pivotun TAŞIDIĞI paramlar. Pencere, sorgunun filtreleri,
// satırın daraltması, scope ve rootOnly:false — beşi de düşerse hedef
// SESSİZCE başka bir soruyu cevaplar. Bu depoda pencere düşürme dört ayrı
// sürümde gemiye gitti (v0.9.208, v0.9.213 ×3) ve her seferinde belirti
// aynıydı: boş liste "böyle trace yok" gibi okunuyor.
//
// Reddedilen dallar da pinli: bir gün biri "metrik satırından da pivotlasak"
// diye açarsa, o değişikliğin bilinçli olması gerekiyor.
import { describe, it, expect } from 'vitest';
import { exploreSourceHref, mergeAndChips } from './sourceHref';
import { blankQuery, type BuilderQuery } from './model';
import type { FilterGroup } from '@/lib/types';

const WINDOW = { preset: '30m' } as const;

function q(over: Partial<BuilderQuery> = {}): BuilderQuery {
  return { ...blankQuery('A'), ...over };
}

/** Kabul edilen href'in query paramlarını çöz. */
function params(href: string): URLSearchParams {
  const i = href.indexOf('?');
  expect(i).toBeGreaterThan(0);
  expect(href.slice(0, i)).toBe('/traces');
  return new URLSearchParams(href.slice(i + 1));
}

function ok(t: ReturnType<typeof exploreSourceHref>): string {
  if (!t.ok) throw new Error(`beklenmedik ret: ${t.why}`);
  return t.href;
}

describe('taşınan paramlar', () => {
  it('PENCERE her zaman taşınıyor', () => {
    const p = params(ok(exploreSourceHref(q(), [], WINDOW)));
    expect(p.get('range')).toBe('30m');
  });

  it('mutlak pencere de taşınıyor (ns → custom:msMs)', () => {
    const p = params(ok(exploreSourceHref(q(), [], { fromNs: 1_000_000_000_000, toNs: 2_000_000_000_000 })));
    expect(p.get('range')).toBe('custom:1000000-2000000');
  });

  it('MEVCUT FİLTRELER taşınıyor', () => {
    const href = ok(exploreSourceHref(
      q({ filters: [{ k: 'http.status_code', op: '=', v: ['500'] }] }), [], WINDOW));
    expect(JSON.parse(params(href).get('filters') ?? '[]')).toEqual([
      { k: 'http.status_code', op: '=', v: ['500'] },
    ]);
  });

  it('SATIRIN daraltması filtrelerin SONUNA ekleniyor', () => {
    const href = ok(exploreSourceHref(
      q({ filters: [{ k: 'http.status_code', op: '=', v: ['500'] }] }),
      [{ k: 'k8s.pod.name', v: 'api-7f9' }], WINDOW));
    expect(JSON.parse(params(href).get('filters') ?? '[]')).toEqual([
      { k: 'http.status_code', op: '=', v: ['500'] },
      { k: 'k8s.pod.name', op: '=', v: ['api-7f9'] },
    ]);
  });

  it('çok boyutlu satırın HER çifti taşınıyor', () => {
    const href = ok(exploreSourceHref(
      q(), [{ k: 'pod', v: 'a' }, { k: 'zone', v: 'eu-1' }], WINDOW));
    expect(JSON.parse(params(href).get('filters') ?? '[]')).toEqual([
      { k: 'pod', op: '=', v: ['a'] },
      { k: 'zone', op: '=', v: ['eu-1'] },
    ]);
  });

  it('scope `service=` paramına gidiyor (hedefte GÖRÜNÜR narrowing)', () => {
    const p = params(ok(exploreSourceHref(q({ scope: 'checkout' }), [], WINDOW)));
    expect(p.get('service')).toBe('checkout');
  });

  it('scope yoksa service paramı hiç basılmıyor', () => {
    expect(params(ok(exploreSourceHref(q(), [], WINDOW))).has('service')).toBe(false);
  });

  it('rootOnly KAPALI — Explore span sorgusu çocuk spanları da ölçer', () => {
    // Açık kalsaydı liste boş çıkardı (v0.8.585 sınıfı).
    expect(params(ok(exploreSourceHref(q(), [], WINDOW))).get('rootOnly')).toBe('false');
  });

  it('liste görünümü', () => {
    expect(params(ok(exploreSourceHref(q(), [], WINDOW))).get('view')).toBe('list');
  });

  it('splitsiz panelin tek satırı da bir popülasyon (boş pairs REDDEDİLMİYOR)', () => {
    expect(exploreSourceHref(q(), [], WINDOW).ok).toBe(true);
  });
});

describe('ifade edilemeyen dallar gerekçeli reddediliyor', () => {
  const cases: Array<{ name: string; query: BuilderQuery; needle: string }> = [
    {
      name: 'metrik sorgusunun gezilebilir ham kaynağı yok',
      query: q({ source: 'metric', metric: 'http.server.duration' }),
      needle: 'Metrik sorgusunun',
    },
    {
      name: 'DSL /traces paramlarına taşınamıyor',
      query: q({ dsl: 'duration > 1s and status = error' }),
      needle: 'DSL',
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      const t = exploreSourceHref(c.query, [], WINDOW);
      expect(t.ok).toBe(false);
      if (!t.ok) expect(t.why).toContain(c.needle);
    });
  }

  it('ret SESSİZ değil — her gerekçe okunabilir bir cümle', () => {
    for (const c of cases) {
      const t = exploreSourceHref(c.query, [], WINDOW);
      if (!t.ok) expect(t.why.length).toBeGreaterThan(20);
    }
  });
});

describe('gruplu filtre (gerçek OR / iç içe)', () => {
  const orGroup: FilterGroup = {
    join: 'OR',
    filters: [
      { k: 'db.name', op: '=', v: ['orders'] },
      { k: 'peer.service', op: '=', v: ['orders'] },
    ],
  };

  it('OR grubu filterGroup= ile taşınıyor, filters= ile DEĞİL', () => {
    const p = params(ok(exploreSourceHref(q({ filterGroup: orGroup }), [], WINDOW)));
    expect(p.has('filterGroup')).toBe(true);
    // /traces sözleşmesi: ikisi karşılıklı dışlayan.
    expect(p.has('filters')).toBe(false);
    expect(JSON.parse(p.get('filterGroup') ?? '{}')).toEqual(orGroup);
  });

  it('OR + satır daraltması → çipler AND kökünde, OR alt grupta', () => {
    const href = ok(exploreSourceHref(
      q({ filterGroup: orGroup }), [{ k: 'pod', v: 'a' }], WINDOW));
    expect(JSON.parse(params(href).get('filterGroup') ?? '{}')).toEqual({
      join: 'AND',
      filters: [{ k: 'pod', op: '=', v: ['a'] }],
      groups: [{ join: 'OR', filters: orGroup.filters }],
    });
  });

  it('AND kökü + satır daraltması → derinlik ARTMADAN kökte birleşiyor', () => {
    const andRoot: FilterGroup = {
      join: 'AND',
      filters: [{ k: 'http.method', op: '=', v: ['POST'] }],
      groups: [{ join: 'OR', filters: orGroup.filters }],
    };
    const decoded = JSON.parse(
      params(ok(exploreSourceHref(q({ filterGroup: andRoot }), [{ k: 'pod', v: 'a' }], WINDOW)))
        .get('filterGroup') ?? '{}');
    expect(decoded.filters).toEqual([
      { k: 'http.method', op: '=', v: ['POST'] },
      { k: 'pod', op: '=', v: ['a'] },
    ]);
    // Alt grup KORUNDU: sarmalanmış olsaydı encodeFilterGroup onu düşürürdü.
    expect(decoded.groups).toEqual([{ join: 'OR', filters: orGroup.filters }]);
  });

  it('OR + KENDİ alt grubu olan kök + daraltma → gerekçeli ret', () => {
    // Sarmalama alt grubu sessizce düşürür (types.ts: ≤1 seviye iç içe),
    // yani hedef DAHA GENİŞ bir liste açardı.
    const nested: FilterGroup = {
      join: 'OR',
      filters: [{ k: 'a', op: '=', v: ['1'] }],
      groups: [{ join: 'AND', filters: [{ k: 'b', op: '=', v: ['2'] }] }],
    };
    const t = exploreSourceHref(q({ filterGroup: nested }), [{ k: 'pod', v: 'a' }], WINDOW);
    expect(t.ok).toBe(false);
    if (!t.ok) expect(t.why).toContain('tek seviyeli');
  });
});

describe('mergeAndChips — derinlik değişmezi', () => {
  const or: FilterGroup = { join: 'OR', filters: [{ k: 'a', op: '=', v: ['1'] }] };

  it('çip yoksa grup AYNEN döner', () => {
    expect(mergeAndChips(or, [])).toBe(or);
  });

  it('AND kökünde sarmalama YOK', () => {
    const and: FilterGroup = { join: 'AND', filters: [], groups: [or] };
    const m = mergeAndChips(and, [{ k: 'p', op: '=', v: ['v'] }]);
    expect(m?.groups).toEqual([or]);      // alt grup korundu
    expect(m?.join).toBe('AND');
  });

  it('join yazılmamışsa AND varsayılıyor', () => {
    const bare = { filters: [{ k: 'a', op: '=' as const, v: ['1'] }] } as FilterGroup;
    const m = mergeAndChips(bare, [{ k: 'p', op: '=', v: ['v'] }]);
    expect(m?.filters).toHaveLength(2);
    expect(m?.groups).toBeUndefined();
  });

  it('alt grubu olan OR kökü → null (ifade edilemez)', () => {
    const nested: FilterGroup = { ...or, groups: [{ join: 'AND', filters: [{ k: 'b', op: '=', v: ['2'] }] }] };
    expect(mergeAndChips(nested, [{ k: 'p', op: '=', v: ['v'] }])).toBe(null);
  });
});
