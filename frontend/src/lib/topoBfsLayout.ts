// topoBfsLayout.ts — BFS katmanlı + barycenter yerleşimi, SAF (v0.10.133,
// perf bütçesi P4 istemci yarısı; docs/perf/perf-budget-2026-08-28.md §3).
//
// ÖLÇÜM (TopologyFlowGraph.perf.test.tsx, 500 düğüm / 20k kenar): eski
// satır içi hâl 1.9 s — iki O(E) döngü sıcak yolda: BFS her frontier
// düğümü için TÜM kenarları tarıyordu (O(n·E)), barycenter comparator'ın
// İÇİNDE `edges.filter` çağrılıyordu (O(E·n log n)). Bu sürüm iki
// komşuluk indeksini (caller → callees, callee → callers) kenar sırasını
// koruyarak BİR kez kurar; BFS ve barycenter listelerden okur → O(E + n log n).
//
// SÖZLEŞME: çıktı eski algoritmayla BİREBİR aynı (topoBfsLayout.test.ts
// referans kopyasıyla karşılaştırır): kök seçimi, 12 derinlik tavanı,
// erişilemeyenler son sütun, 0. sütun ada göre, diğerleri barycenter
// (ebeveyn yoksa lh/2), aynı ziyaret sırası (frontier × kenar sırası).

export interface LayoutNode { service: string; spanCount: number }
export interface LayoutEdge { caller: string; callee: string }
export interface LayoutResult {
  pos: Map<string, { x: number; y: number }>;
  lw: number;
  lh: number;
}

export function bfsBarycenterLayout(
  nodes: LayoutNode[], edges: LayoutEdge[], width: number, height: number, rowPitch: number,
): LayoutResult {
  // Komşuluk indeksleri — kenar sırası korunur (ziyaret sırası sözleşmesi).
  const out = new Map<string, string[]>();
  const inc = new Map<string, string[]>();
  for (const e of edges) {
    let o = out.get(e.caller);
    if (!o) { o = []; out.set(e.caller, o); }
    o.push(e.callee);
    let i = inc.get(e.callee);
    if (!i) { i = []; inc.set(e.callee, i); }
    i.push(e.caller);
  }
  let roots = nodes.filter(n => !inc.has(n.service)).map(n => n.service);
  if (roots.length === 0 && nodes.length > 0) {
    // Döngü: en yüksek hacimli servisi kök say.
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
      const callees = out.get(f);
      if (!callees) continue;
      for (const callee of callees) {
        if (!depth.has(callee)) { depth.set(callee, d); next.add(callee); }
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
  const lh = Math.max(height, maxCol * rowPitch);
  const padX = 110;
  const lw = Math.max(width, padX * 2 + Math.max(0, columns.length - 1) * 180);

  const pos = new Map<string, { x: number; y: number }>();
  const colW = columns.length > 1 ? (lw - padX * 2) / (columns.length - 1) : 0;
  columns.forEach((col, ci) => {
    let sorted: string[];
    if (ci === 0) {
      sorted = col.slice().sort();
    } else {
      // Barycenter sütun başına BİR kez hesaplanır (comparator içinde değil):
      // aynı değerler, aynı kararlı sıralama.
      const bary = new Map<string, number>();
      for (const svc of col) {
        const callers = inc.get(svc);
        if (!callers || callers.length === 0) { bary.set(svc, lh / 2); continue; }
        let s = 0;
        for (const c of callers) s += pos.get(c)?.y ?? lh / 2;
        bary.set(svc, s / callers.length);
      }
      sorted = col.slice().sort((a, b) => bary.get(a)! - bary.get(b)!);
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
