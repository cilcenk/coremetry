// v0.10.133 — perf bütçesi P4 (istemci yarısı): BFS + barycenter yerleşimi
// O(E·n log n)'den O(E + n log n)'e — komşuluk indeksi. Sözleşme:
// ÇIKTI BİREBİR AYNI (aynı sütun, aynı sıra, aynı x/y) — referans
// algoritma (TopologyFlowGraph.tsx'in v0.8.296 satır içi hâli) bu dosyada
// kopya olarak durur ve üç sentetik grafta (kök/dal, döngü, erişilemeyen
// ada, çoklu ebeveyn) yeni yerleşimle karşılaştırılır.
import { describe, it, expect } from 'vitest';
import { bfsBarycenterLayout } from './topoBfsLayout';

type Node = { service: string; spanCount: number };
type Edge = { caller: string; callee: string };

// Referans: eski satır içi algoritma, birebir (yalnız data.* → parametre).
function referenceLayout(nodes: Node[], edges: Edge[], width: number, height: number, ROW_PITCH: number) {
  const incoming = new Map<string, number>();
  for (const e of edges) incoming.set(e.callee, (incoming.get(e.callee) ?? 0) + 1);
  let roots = nodes.filter(n => !incoming.has(n.service)).map(n => n.service);
  if (roots.length === 0 && nodes.length > 0) {
    roots = [nodes.slice().sort((a, b) => b.spanCount - a.spanCount)[0].service];
  }
  const depth = new Map<string, number>();
  roots.forEach(r => depth.set(r, 0));
  let frontier = [...roots];
  let d = 0;
  while (frontier.length && d < 12) {
    d++;
    const next = new Set<string>();
    for (const f of frontier) {
      for (const e of edges) {
        if (e.caller === f && !depth.has(e.callee)) { depth.set(e.callee, d); next.add(e.callee); }
      }
    }
    frontier = Array.from(next);
  }
  let maxDepth = 0;
  for (const v of depth.values()) maxDepth = Math.max(maxDepth, v);
  for (const n of nodes) if (!depth.has(n.service)) depth.set(n.service, maxDepth + 1);
  maxDepth = Math.max(...Array.from(depth.values()), 0);
  const columns: string[][] = Array.from({ length: maxDepth + 1 }, () => []);
  for (const n of nodes) columns[depth.get(n.service)!].push(n.service);
  const maxCol = Math.max(1, ...columns.map(c => c.length));
  const lh = Math.max(height, maxCol * ROW_PITCH);
  const padX = 110;
  const lw = Math.max(width, padX * 2 + Math.max(0, columns.length - 1) * 180);
  const pos = new Map<string, { x: number; y: number }>();
  const colW = columns.length > 1 ? (lw - padX * 2) / (columns.length - 1) : 0;
  columns.forEach((col, ci) => {
    const sorted = ci === 0
      ? col.slice().sort()
      : col.slice().sort((a, b) => barycenter(a) - barycenter(b));
    function barycenter(svc: string): number {
      const ys = edges.filter(e => e.callee === svc).map(e => pos.get(e.caller)?.y ?? lh / 2);
      return ys.length ? ys.reduce((s, y) => s + y, 0) / ys.length : lh / 2;
    }
    sorted.forEach((svc, i) => {
      pos.set(svc, {
        x: columns.length === 1 ? lw / 2 : padX + ci * colW,
        y: ((i + 1) / (sorted.length + 1)) * lh,
      });
    });
  });
  return { pos, lw, lh };
}

function synth(nodes: number, edgesPerNode: number, seed = 7): { nodes: Node[]; edges: Edge[] } {
  const ns = Array.from({ length: nodes }, (_, i) => ({ service: `svc-${i}`, spanCount: 1000 - (i % 997) }));
  const es: Edge[] = [];
  for (let i = 0; i < nodes; i++) {
    for (let k = 1; k <= edgesPerNode; k++) {
      const j = (i * seed + k * 13) % nodes;
      if (j !== i) es.push({ caller: `svc-${i}`, callee: `svc-${j}` });
    }
  }
  return { nodes: ns, edges: es };
}

const cases: { name: string; nodes: Node[]; edges: Edge[] }[] = [
  { name: 'kök + dallar + çoklu ebeveyn', nodes: ['gw', 'a', 'b', 'c', 'd'].map(s => ({ service: s, spanCount: 10 })),
    edges: [{ caller: 'gw', callee: 'a' }, { caller: 'gw', callee: 'b' }, { caller: 'a', callee: 'c' }, { caller: 'b', callee: 'c' }, { caller: 'c', callee: 'd' }, { caller: 'gw', callee: 'd' }] },
  { name: 'döngü (kök yok) + erişilemeyen ada', nodes: [{ service: 'x', spanCount: 5 }, { service: 'y', spanCount: 9 }, { service: 'z', spanCount: 1 }, { service: 'island', spanCount: 3 }, { service: 'island2', spanCount: 2 }],
    edges: [{ caller: 'x', callee: 'y' }, { caller: 'y', callee: 'z' }, { caller: 'z', callee: 'x' }, { caller: 'island', callee: 'island2' }, { caller: 'island2', callee: 'island' }] },
  { name: 'sentetik 300/10 (kenar tekrarı dahil)', ...synth(300, 10) },
  { name: 'sentetik 500/40 (tavan)', ...synth(500, 40, 11) },
  { name: 'tek düğüm', nodes: [{ service: 'solo', spanCount: 1 }], edges: [] },
  { name: 'boş', nodes: [], edges: [] },
];

describe('bfsBarycenterLayout — referansla birebir', () => {
  for (const c of cases) {
    it(c.name, () => {
      const ref = referenceLayout(c.nodes, c.edges, 800, 600, 46);
      const got = bfsBarycenterLayout(c.nodes, c.edges, 800, 600, 46);
      expect(got.lw).toBe(ref.lw);
      expect(got.lh).toBe(ref.lh);
      expect(got.pos.size).toBe(ref.pos.size);
      for (const [svc, p] of ref.pos) {
        const q = got.pos.get(svc);
        expect(q, `${svc} yerleşimde yok`).toBeDefined();
        expect(q!.x).toBeCloseTo(p.x, 9);
        expect(q!.y).toBeCloseTo(p.y, 9);
      }
    });
  }
});
