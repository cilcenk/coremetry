// TopologyFlowGraph.perf.test.tsx — İSTEMCİ TARAFI yerleşim bütçesi
// (docs/perf/perf-budget-2026-08-28.md P4, v0.10.116). Tarayıcısız:
// react-dom/server ile render → useMemo yerleşimi + SVG üretimi ölçülür
// (boya hariç). Taban (2026-08-28, laptop): 50/148 → 5 ms · 100/496 →
// 13 ms · 300/3k → 189 ms · 500/10k → 940 ms · 500/20k (sunucu tavanı)
// → 1.906 ms. v0.10.133 komşuluk indeksi (lib/topoBfsLayout.ts) sonrası
// aynı makine: 300/3k → 83 ms · 500/10k → 199 ms · 500/20k → 336 ms
// (5×; kalan maliyet 20k kenarın SVG dizesi, yerleşim değil). Eşik
// tavanda 1.5 s (4× pay, CI makinesi dahil) — eski 4 s gevşek kalırdı;
// medyan of 3.
import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { TopologyFlowGraph } from './TopologyFlowGraph';
import type { ServiceMap } from '../lib/types';
import type React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
const qc = new QueryClient({ defaultOptions: { queries: { retry: false, enabled: false } } });
const wrap = (el: React.ReactElement) => <MemoryRouter><QueryClientProvider client={qc}>{el}</QueryClientProvider></MemoryRouter>;

function synth(nodes: number, edgesPerNode: number): ServiceMap {
  const ns = Array.from({ length: nodes }, (_, i) => ({ service: `svc-${i}`, spanCount: 1000 - (i % 997), errorRate: 0 }));
  const es: ServiceMap['edges'] = [];
  for (let i = 0; i < nodes; i++) {
    for (let k = 1; k <= edgesPerNode; k++) {
      const j = (i * 7 + k * 13) % nodes;
      if (j !== i) es.push({ caller: `svc-${i}`, callee: `svc-${j}`, traceCount: 10, spanCount: 50, errorCount: 1 });
    }
  }
  return { nodes: ns, edges: es, sampledFrom: 0, totalSpans: 0 } as ServiceMap;
}

describe('TopologyFlowGraph layout cost (perf probe)', () => {
  it('renders synthetic maps and reports ms', { timeout: 120_000 }, () => {
    const out: string[] = [];
    for (const [n, e] of [[50, 3], [100, 5], [300, 10], [500, 20], [500, 40]] as const) {
      const data = synth(n, e);
      renderToString(wrap(<TopologyFlowGraph data={data} focus={null} hoverNode={null} onHoverNode={() => {}} onSelectNode={() => {}} dropMessaging={false} />));
      const runs: number[] = [];
      for (let r = 0; r < 3; r++) {
        const t0 = performance.now();
        renderToString(wrap(<TopologyFlowGraph data={data} focus={null} hoverNode={null} onHoverNode={() => {}} onSelectNode={() => {}} dropMessaging={false} />));
        runs.push(performance.now() - t0);
      }
      runs.sort((a, b) => a - b);
      out.push(`nodes=${n} edges=${data.edges.length} median=${runs[1].toFixed(1)}ms min=${runs[0].toFixed(1)} max=${runs[2].toFixed(1)}`);
    }
    console.log('\nTOPOLOGY_LAYOUT_PERF\n' + out.join('\n'));
    // Bütçe: sunucu tavanı (500 düğüm / 20k kenar) medyan ≤ 1500 ms (v0.10.133).
    const cap = out[out.length - 1];
    const med = Number(/median=([\d.]+)ms/.exec(cap)?.[1] ?? 'NaN');
    expect(med, `yerleşim bütçesi aşıldı (500/20k): ${cap}`).toBeLessThan(1500);
  });
});
