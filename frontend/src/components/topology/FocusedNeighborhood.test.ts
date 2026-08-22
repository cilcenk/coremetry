import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { assignFocusColumns, externalPinHost } from './FocusedNeighborhood';
import type { GraphEdge, GraphNode } from '@/lib/types';

// v0.8.39 — Operator-reported: the focused topology graph "won't branch" at
// 2 hops — every node piled into ONE vertical column instead of fanning out
// (focus centre, callers left, deps right). Root cause: the old single
// bidirectional BFS summed the ±1 steps along the path, so a node reached as
// caller(-1) → that caller's OTHER dependency(+1) landed at column 0,
// dumping every sibling of the focus into the focus's own column (against
// real data ~26 nodes piled at 0). assignFocusColumns walks the two
// directions SEPARATELY (upstream IN-only → -hop, downstream OUT-only →
// +hop), so callers fan strictly left, deps strictly right, and only the
// focus is at column 0. These cases would fail on the pre-fix path-sum code.

const e = (source: string, target: string): GraphEdge => ({
  source, target, calls: 1, errors: 0, errorRate: 0, rate: 0, avgMs: 1, p99Ms: 1,
});

describe('assignFocusColumns', () => {
  it('keeps a caller-sibling OFF the focus column at 2 hops (the bug)', () => {
    const edges = [
      e('caller', 'focus'),     // caller is upstream of focus
      e('caller', 'sibling'),   // sibling shares the caller — NOT on a focus up/down path
      e('focus', 'dep'),        // dep is downstream of focus
      e('dep', 'deepdep'),      // 2-hop downstream
    ];
    const col = assignFocusColumns(edges, 'focus', 2);

    expect(col.get('focus')).toBe(0);
    expect(col.get('caller')).toBe(-1);   // upstream → left
    expect(col.get('dep')).toBe(1);       // downstream → right
    expect(col.get('deepdep')).toBe(2);   // 2-hop downstream
    // the sibling is reachable only via caller(-1)→sibling, i.e. col 0 under
    // the old path-sum — it must NOT be pinned to the focus column.
    expect(col.get('sibling')).not.toBe(0);
    // in fact a pure up/down walk never reaches it → excluded entirely.
    expect(col.has('sibling')).toBe(false);
    // INVARIANT: nothing but the focus sits at column 0.
    expect([...col.values()].filter(v => v === 0)).toEqual([0]);
  });

  it('fans callers left (negative) and deps right (positive) by hop depth', () => {
    const edges = [e('gp', 'caller'), e('caller', 'focus'), e('focus', 'dep'), e('dep', 'gd')];
    const col = assignFocusColumns(edges, 'focus', 2);
    expect(col.get('caller')).toBe(-1);
    expect(col.get('gp')).toBe(-2);
    expect(col.get('dep')).toBe(1);
    expect(col.get('gd')).toBe(2);
  });

  it('bounds the walk by hops', () => {
    const col = assignFocusColumns([e('focus', 'a'), e('a', 'b'), e('b', 'c')], 'focus', 1);
    expect(col.get('a')).toBe(1);
    expect(col.has('b')).toBe(false); // beyond 1 hop
  });

  it('a cycle takes the closer side by absolute hop distance', () => {
    // focus → a → focus (a is both a 1-hop dep and a 1-hop caller); |+1| == |-1|,
    // downstream is assigned first so a settles on +1 — never 0.
    const col = assignFocusColumns([e('focus', 'a'), e('a', 'focus')], 'focus', 2);
    expect(col.get('focus')).toBe(0);
    expect(Math.abs(col.get('a')!)).toBe(1);
    expect(col.get('a')).not.toBe(0);
  });
});

// v0.9.359 — capPerSide. The flat nearest-first cap could drop EVERY caller:
// ties at |col|=1 kept insertion order, and the walk emits dependencies
// first, so a gateway with 45 deps + 20 callers rendered 39 deps and zero
// callers — fabricating "nothing calls this service".
import { capPerSide } from './FocusedNeighborhood';

describe('capPerSide', () => {
  const mk = (callers: number, deps: number) => {
    const col = new Map<string, number>([['focus', 0]]);
    for (let i = 0; i < callers; i++) col.set(`c${i}`, -1 - (i % 2)); // -1/-2 karışık
    for (let i = 0; i < deps; i++) col.set(`d${i}`, 1 + (i % 2));
    return { ids: [...col.keys()], colOf: (id: string) => col.get(id) ?? 0 };
  };

  it('under the cap: untouched', () => {
    const { ids, colOf } = mk(5, 5);
    const r = capPerSide(ids, colOf, 40, 'focus');
    expect(r.keep.length).toBe(11);
    expect(r.collapsed).toBe(0);
  });

  it('the review scenario: 45 deps + 20 callers keeps BOTH sides', () => {
    const { ids, colOf } = mk(20, 45);
    const r = capPerSide(ids, colOf, 40, 'focus');
    expect(r.keep.length).toBe(40);
    const callers = r.keep.filter(id => colOf(id) < 0).length;
    const deps = r.keep.filter(id => colOf(id) > 0).length;
    // budget=39, half=19: çağıran 19 (1'i düşer), bağımlılık 20 — ama SIFIR
    // çağıran asla; eski davranış 0/39'du.
    expect(callers).toBe(19);
    expect(deps).toBe(20);
    expect(r.collapsed).toBe(26); // 66 düğümden 40 kaldı
  });

  it('an under-full side donates its remainder', () => {
    const { ids, colOf } = mk(3, 60);
    const r = capPerSide(ids, colOf, 40, 'focus');
    expect(r.keep.filter(id => colOf(id) < 0).length).toBe(3);   // hepsi
    expect(r.keep.filter(id => colOf(id) > 0).length).toBe(36);  // 39-3
  });

  it('focus is always kept', () => {
    const { ids, colOf } = mk(50, 50);
    const r = capPerSide(ids, colOf, 10, 'focus');
    expect(r.keep).toContain('focus');
    expect(r.keep.length).toBe(10);
  });

  it('within a side the cut is nearest-first', () => {
    const col = new Map<string, number>([['focus', 0], ['near', 1], ['far', 2], ['x', -1]]);
    const r = capPerSide([...col.keys()], id => col.get(id) ?? 0, 3, 'focus');
    expect(r.keep).toContain('near');
    expect(r.keep).not.toContain('far');
  });
});

// ── v0.9.1255 — pin'li dış düğümün yol kırılımı fetch KAPISI ──────────
//
// Operator-reported: dış düğümde host değil hangi UÇ olduğu anlamlı. Pin
// kartı artık /api/external/host çağırıyor — ve o ucun yol yarısı HAM
// spans okuması. Kapı bu yüzden düğüm TÜRÜNDE: servis / db / queue
// düğümü pin'lendiğinde istek ATILMAMALI. Atılsaydı her pin bir ham
// tarama tetiklerdi ve dönen cevap da boş olurdu (sunucu tarafındaki
// GLOBAL NOT IN enstrümante servisleri zaten eliyor) — bedeli ödeyip
// cevapsızlık almak.
describe('externalPinHost — pin kartı fetch kapısı', () => {
  const node = (over: Partial<GraphNode>): GraphNode => ({
    id: 'x', name: 'x', kind: 'service',
    calls: 0, errors: 0, errorRate: 0, rate: 0, ...over,
  });

  it('dış düğüm host üretir', () => {
    expect(externalPinHost(node({ id: 'ext:esbprod.example.internal', name: 'esbprod.example.internal', kind: 'external' })))
      .toBe('esbprod.example.internal');
  });

  it('SERVİS düğümü pin\'lenince istek atılmaz (asıl regresyon)', () => {
    expect(externalPinHost(node({ id: 'payments', name: 'payments', kind: 'service' }))).toBeNull();
  });

  it('db / queue / internal düğümleri de istek atmaz', () => {
    for (const kind of ['database', 'queue', 'internal'] as GraphNode['kind'][]) {
      expect(externalPinHost(node({ id: `db:oracle@o`, name: 'oracle@o', kind }))).toBeNull();
    }
  });

  it('pin YOKSA (hover) istek atılmaz — hover davranışı değişmedi', () => {
    expect(externalPinHost(null)).toBeNull();
    expect(externalPinHost(undefined)).toBeNull();
  });

  it('adı boş gelen dış düğümde id ön eki soyulur', () => {
    expect(externalPinHost(node({ id: 'ext:api.stripe.com', name: '', kind: 'external' })))
      .toBe('api.stripe.com');
  });
});

// Kapının GERÇEKTEN sorguya bağlı olduğunu pinler. Saf fonksiyonun
// doğru cevap vermesi, bileşenin o cevabı KULLANDIĞINI kanıtlamaz —
// `enabled` satırı düşerse useQuery her pin'de koşardı ve yukarıdaki
// beş test yine yeşil kalırdı.
describe('externalPinHost bağlantısı', () => {
  const src = readFileSync(
    fileURLToPath(new URL('./FocusedNeighborhood.tsx', import.meta.url)), 'utf8');

  it('kapı pinnedNode üstünden kuruluyor (hoverNode DEĞİL)', () => {
    expect(src).toContain('const extHost = externalPinHost(pinnedNode)');
  });

  it('useQuery kapıya bağlı', () => {
    expect(src).toContain('enabled: !!extHost');
  });

  it('staleTime sunucu TTL\'ine eşit veya üstünde (30s)', () => {
    const q = src.slice(src.indexOf('const extDetail = useQuery'));
    const m = /staleTime:\s*([\d_]+)/.exec(q);
    expect(m).not.toBeNull();
    expect(Number(m![1].replace(/_/g, ''))).toBeGreaterThanOrEqual(30_000);
  });
});
