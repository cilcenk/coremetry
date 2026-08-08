import { describe, it, expect } from 'vitest';
import {
  endpointP99Delta,
  worseningCount,
  listedTotals,
  endpointRowKey,
  decodeEndpointRowKey,
  encodeExpandedParam,
  decodeExpandedParam,
  toggleExpanded,
  endpointsSourceNote,
  isEndpointsParamFree,
  ENDPOINTS_RESERVED_PARAMS,
  DETAIL_PARAM,
  EXP_PARAM,
  EXP_MAX,
} from './endpointHonesty';

// endpointHonesty regresyon testleri — v0.9.818.
//
// Sabitlenen dört yalan:
//   1. prior=0 "YENİ" demekti; prior okuma da top-N olduğu için doğru
//      cümle "listede yeni" ve delta HESAPLANAMAZ (Infinity değil).
//   2. KPI'lar görünen satırların toplamı; "filo" iddiası yok.
//   3. ?detail= / ?exp= paramları rezerve adlarla ÇAKIŞMAZ ve codec
//      bozuk girdide çökmez.
//   4. env/cluster seçiliyken kaynak notu ÇIKAR, seçili değilken ÇIKMAZ.

describe('endpointP99Delta', () => {
  const cases: Array<{
    name: string; cur: number; prior?: number;
    want: { kind: 'listNew' } | { kind: 'delta'; pct: number };
  }> = [
    { name: 'prior yok → listede yeni', cur: 120, prior: undefined, want: { kind: 'listNew' } },
    { name: 'prior 0 → listede yeni (Infinity DEĞİL)', cur: 120, prior: 0, want: { kind: 'listNew' } },
    { name: 'prior negatif → listede yeni', cur: 120, prior: -3, want: { kind: 'listNew' } },
    { name: 'prior NaN → listede yeni', cur: 120, prior: NaN, want: { kind: 'listNew' } },
    { name: 'cur NaN → listede yeni', cur: NaN, prior: 100, want: { kind: 'listNew' } },
    { name: 'iki kat yavaşladı', cur: 200, prior: 100, want: { kind: 'delta', pct: 100 } },
    { name: 'yarıya indi', cur: 50, prior: 100, want: { kind: 'delta', pct: -50 } },
    { name: 'değişmedi', cur: 100, prior: 100, want: { kind: 'delta', pct: 0 } },
    { name: 'cur 0 → -%100 (ölçülmüş bir iyileşme)', cur: 0, prior: 100, want: { kind: 'delta', pct: -100 } },
  ];
  for (const c of cases) {
    it(c.name, () => {
      const got = endpointP99Delta(c.cur, c.prior);
      expect(got.kind).toBe(c.want.kind);
      if (got.kind === 'delta' && c.want.kind === 'delta') {
        expect(got.pct).toBeCloseTo(c.want.pct, 6);
      }
    });
  }

  it('sonuç asla Infinity taşımaz', () => {
    const d = endpointP99Delta(500, 0);
    expect(d.kind).toBe('listNew');
    if (d.kind === 'delta') expect(Number.isFinite(d.pct)).toBe(true);
  });
});

describe('worseningCount', () => {
  const rows = [
    { p99Ms: 200, priorP99Ms: 100 }, // +%100 → sayılır
    { p99Ms: 106, priorP99Ms: 100 }, // +%6   → sayılır
    { p99Ms: 103, priorP99Ms: 100 }, // +%3   → eşik altı, sayılmaz
    { p99Ms: 50, priorP99Ms: 100 },  // iyileşme, sayılmaz
    { p99Ms: 900, priorP99Ms: 0 },   // listede yeni, sayılmaz
    { p99Ms: 900 },                  // prior alanı hiç yok, sayılmaz
  ];
  it('yalnız eşiği aşan ÖLÇÜLMÜŞ kötüleşmeleri sayar', () => {
    expect(worseningCount(rows)).toBe(2);
  });
  it('eşik ayarlanabilir', () => {
    expect(worseningCount(rows, 50)).toBe(1);
    expect(worseningCount(rows, 0)).toBe(3); // +100, +6, +3
  });
  it('boş liste 0', () => {
    expect(worseningCount([])).toBe(0);
  });
  it('"listede yeni" satır kötüleşmiş SAYILMAZ (taban yok)', () => {
    expect(worseningCount([{ p99Ms: 1e9, priorP99Ms: 0 }])).toBe(0);
  });
});

describe('listedTotals', () => {
  it('görünen satırları toplar, oranı TOPLAMLARDAN türetir', () => {
    const t = listedTotals([
      { calls: 1000, errors: 10 },
      { calls: 10, errors: 5 },
    ]);
    expect(t.rows).toBe(2);
    expect(t.calls).toBe(1010);
    expect(t.errors).toBe(15);
    // Satır oranlarının ortalaması (%1 + %50)/2 = %25.5 OLURDU; doğrusu bu:
    expect(t.errorRate).toBeCloseTo(15 / 1010 * 100, 6);
  });
  it('boş liste sıfır bölmesi yapmaz', () => {
    const t = listedTotals([]);
    expect(t).toEqual({ rows: 0, calls: 0, errors: 0, errorRate: 0 });
  });
  it('çağrı yokken hata oranı 0 (NaN değil)', () => {
    const t = listedTotals([{ calls: 0, errors: 0 }]);
    expect(Number.isNaN(t.errorRate)).toBe(false);
    expect(t.errorRate).toBe(0);
  });
});

describe('satır kimliği codec', () => {
  const tricky: Array<[string, string]> = [
    ['checkout-api', '/orders/:id'],
    ['a|b', '/x|y'],              // ayraç alan içinde
    ['svc', '/a,b'],              // virgül alan içinde (liste ayracı)
    ['tr-şubə', '/müşteri/ödeme'], // ASCII dışı
    ['s p a c e', '/with space'],
    ['pct', '/100%'],             // ham % — encode edilmezse decode patlardı
  ];
  for (const [svc, path] of tricky) {
    it(`round-trip: ${svc} ${path}`, () => {
      const k = endpointRowKey(svc, path);
      expect(k.split('|')).toHaveLength(2);
      expect(decodeEndpointRowKey(k)).toEqual({ service: svc, path });
    });
  }
  it('bozuk anahtar null döner (çökmez)', () => {
    expect(decodeEndpointRowKey('tekparca')).toBeNull();
    expect(decodeEndpointRowKey('a|b|c')).toBeNull();
    expect(decodeEndpointRowKey('|b')).toBeNull();
    expect(decodeEndpointRowKey('a|')).toBeNull();
    expect(decodeEndpointRowKey('%E0%A4%A|x')).toBeNull(); // bozuk escape
  });
});

describe('?exp= codec', () => {
  it('round-trip, sıra korunur', () => {
    const keys = [
      endpointRowKey('svc-a', '/a'),
      endpointRowKey('svc-b', '/b,c'),
    ];
    const enc = encodeExpandedParam(keys);
    expect(enc.split(',')).toHaveLength(2); // alan içi virgül sınır uydurmaz
    expect([...decodeExpandedParam(enc)]).toEqual(keys);
  });
  it('boş küme boş string, boş string boş küme', () => {
    expect(encodeExpandedParam([])).toBe('');
    expect(decodeExpandedParam('')).toEqual(new Set());
    expect(decodeExpandedParam(null)).toEqual(new Set());
    expect(decodeExpandedParam(undefined)).toEqual(new Set());
  });
  it('bozuk parçaları atar, sağlamları tutar', () => {
    const good = endpointRowKey('svc', '/ok');
    expect([...decodeExpandedParam(`bozuk,${good},a|b|c,`)]).toEqual([good]);
  });
  it('tavan iki tarafta da aynı', () => {
    const many = Array.from({ length: EXP_MAX + 7 }, (_, i) => endpointRowKey('s', `/p${i}`));
    expect(encodeExpandedParam(many).split(',')).toHaveLength(EXP_MAX);
    expect(decodeExpandedParam(many.join(',')).size).toBe(EXP_MAX);
  });
  it('toggleExpanded saf — girdiyi mutasyona uğratmaz', () => {
    const a = new Set(['x']);
    const b = toggleExpanded(a, 'y');
    expect([...a]).toEqual(['x']);
    expect([...b]).toEqual(['x', 'y']);
    expect([...toggleExpanded(b, 'x')]).toEqual(['y']);
  });
  it('toggleExpanded tavanı aşmaz', () => {
    let s = new Set<string>();
    for (let i = 0; i < EXP_MAX + 5; i++) s = toggleExpanded(s, `k${i}`);
    expect(s.size).toBe(EXP_MAX);
  });
});

describe('URL param çakışması', () => {
  it('?detail= ve ?exp= rezerve adlarla çakışmaz', () => {
    expect(isEndpointsParamFree(DETAIL_PARAM)).toBe(true);
    expect(isEndpointsParamFree(EXP_PARAM)).toBe(true);
  });
  it('rezerve adlar gerçekten rezerve okunur', () => {
    for (const p of ENDPOINTS_RESERVED_PARAMS) {
      expect(isEndpointsParamFree(p)).toBe(false);
    }
  });
  it('rezerve liste sayfanın bildiği paramları içerir', () => {
    for (const p of ['range', 'env', 'endpoint', 'cols', 's_endpoints']) {
      expect(ENDPOINTS_RESERVED_PARAMS as readonly string[]).toContain(p);
    }
  });
});

describe('endpointsSourceNote', () => {
  it('filtre yokken not YOK (gürültü basmaz)', () => {
    expect(endpointsSourceNote()).toBeNull();
    expect(endpointsSourceNote('', '')).toBeNull();
  });
  it('cluster seçiliyken çıkar ve cluster der', () => {
    const n = endpointsSourceNote('prod-eu', '');
    expect(n).toContain('cluster');
    expect(n).not.toContain('env +');
  });
  it('env seçiliyken çıkar ve env der', () => {
    expect(endpointsSourceNote('', 'uat')).toContain('env');
  });
  it('ikisi birdeyken ikisini de sayar', () => {
    const n = endpointsSourceNote('prod-eu', 'uat')!;
    expect(n).toContain('env + cluster');
  });
  it('her hâlinde iki popülasyon farkını da söyler', () => {
    const n = endpointsSourceNote('c', '')!;
    expect(n).toContain('exemplar');
    expect(n.toLowerCase()).toContain('client');
  });
});
