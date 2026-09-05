import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// v0.10.368 — operator-reported: "Service overview yüklenirken önce başka
// response time çıkıyor, sayfa yüklendikten sonra başka". In metric mode
// the KPI tiles decide their source from the metric query's RESULT, so
// while it was loading "no result yet" read as "span" and the tiles
// flashed span P99 / span throughput (206 ms, 69 req/s) before jumping
// to the metric numbers (16 ms, 4.1 req/s). The tiles now wait: metric
// label, "…" value, no spark/delta — span only when the metric truly
// does not exist. Source pin: the gate must stay on BOTH tiles.
const src = readFileSync(fileURLToPath(new URL('./Overview.tsx', import.meta.url)), 'utf8');

describe('Overview KPI tiles wait for the metric query in metric mode', () => {
  it('derives the pending flag from metric mode + metric query loading', () => {
    expect(src).toContain('const metricTilesPending = metricMode && metricTputQ.isLoading;');
  });

  it('labels stay metric-flavoured while pending (no "(span)" flash)', () => {
    expect(src).toContain("rtFromMetric || metricTilesPending ? 'Response time · avg'");
    expect(src).toContain("tputFromMetric || metricTilesPending ? 'Throughput'");
  });

  it('both tiles render "…" with no spark / delta while pending', () => {
    const rt = src.indexOf('<KpiTile lab={rtLabel}');
    const tput = src.indexOf('<KpiTile lab={tputLabel}');
    expect(rt).toBeGreaterThan(0);
    expect(tput).toBeGreaterThan(rt);
    for (const tile of [src.slice(rt, tput), src.slice(tput, src.indexOf('<KpiTile lab="Failure rate"'))]) {
      expect(tile).toContain("val={metricTilesPending ? '…' :");
      expect(tile).toContain('spark={metricTilesPending ? undefined :');
      expect(tile).toContain('delta={metricTilesPending ? null :');
    }
  });

  it('the P99 sub-line never shows a span number under a pending metric tile', () => {
    expect(src).toContain('sub={!metricTilesPending && rtFromMetric && p99Now != null');
  });
});
