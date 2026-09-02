// traceWaterfall.perf.test.tsx — v0.10.278 (audit §7.3): 500/1000/5000/20000
// span'de renderToString maliyeti (boya hariç; TopologyFlowGraph.perf deseni:
// ısınma + 3 koşu medyanı, tek tavan). content-visibility kazancını ÖLÇMEZ;
// sanal yolun (≥400 satır) kazancını ölçer. Tavan ölçülerek konuldu (2026-09-02
// M-serisi: 5000 → ~40 ms, 20000 → ~150 ms) × ~10 CI payı.
import { describe, it, expect } from 'vitest';
import { renderToString } from 'react-dom/server';
import { TraceWaterfall } from './TraceWaterfall';
import type { SpanRow } from '@/lib/types';

function synth(n: number): SpanRow[] {
  const out: SpanRow[] = [{ traceId: 't', spanId: 'root', parentSpanId: '', serviceName: 'api', name: 'GET /x', startTime: 0, endTime: n * 100_000 + 1_000_000, statusCode: 'ok', kind: 'server', attributes: {} } as unknown as SpanRow];
  for (let i = 1; i < n; i++) {
    const parent = i <= 8 ? 'root' : `s${Math.floor((i - 1) / 8)}`;
    out.push({ traceId: 't', spanId: `s${i}`, parentSpanId: parent, serviceName: `svc${i % 12}`, name: `op${i % 40}`, startTime: i * 100_000, endTime: i * 100_000 + 50_000, statusCode: i % 97 === 0 ? 'error' : 'ok', kind: 'client', attributes: {} } as unknown as SpanRow);
  }
  return out;
}

describe('TraceWaterfall render cost (perf probe)', () => {
  it('renders synthetic traces and reports ms', { timeout: 120_000 }, () => {
    const out: string[] = [];
    for (const n of [500, 1000, 5000, 20000]) {
      const spans = synth(n);
      renderToString(<TraceWaterfall spans={spans} selectedId={null} onSelect={() => {}} />);
      const runs: number[] = [];
      for (let r = 0; r < 3; r++) {
        const t0 = performance.now();
        renderToString(<TraceWaterfall spans={spans} selectedId={null} onSelect={() => {}} />);
        runs.push(performance.now() - t0);
      }
      runs.sort((a, b) => a - b);
      out.push(`spans=${n} median=${runs[1].toFixed(1)}ms min=${runs[0].toFixed(1)} max=${runs[2].toFixed(1)}`);
    }
    console.log('\nTRACE_WATERFALL_PERF\n' + out.join('\n'));
    const med5k = Number(/median=([\d.]+)ms/.exec(out[2])?.[1] ?? 'NaN');
    expect(med5k, `5000 span render bütçesi aşıldı: ${out[2]}`).toBeLessThan(2000);
  });
});
