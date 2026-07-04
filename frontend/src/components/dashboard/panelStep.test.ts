import { describe, expect, it } from 'vitest';
import { effectivePanelStep, estimatePanelPx } from './panelStep';
import { stepForWidth, quantizeWidth } from '@/lib/chartStep';
import type { Panel, MetricPanelConfig, SpanMetricPanelConfig } from '@/lib/types';

// GRAN-C (v0.8.248) — width-aware step for dashboard panels. Two contracts
// pinned here: (1) backward-compat — a dashboard document saved BEFORE this
// release has no step field on its panel configs and must decode straight to
// auto (width-aware), never to a stale literal; (2) the pure resolution
// helpers the renderer + bundle fetch share.

const HOUR = 3600;

describe('effectivePanelStep — resolution contract', () => {
  it('operator-pinned step passes through verbatim (width ignored)', () => {
    expect(effectivePanelStep(300, HOUR, 600)).toBe(300);
    expect(effectivePanelStep(300, HOUR, null)).toBe(300); // no deferral for manual
    expect(effectivePanelStep(1, 30 * 86400, 2400)).toBe(1); // even absurdly fine — backend clamps
  });

  it('auto (undefined or 0) resolves via quantizeWidth + stepForWidth', () => {
    for (const auto of [undefined, 0]) {
      expect(effectivePanelStep(auto, HOUR, 600)).toBe(stepForWidth(HOUR, quantizeWidth(600)));
      expect(effectivePanelStep(auto, HOUR, 600)).toBe(15); // 300 points → raw 12s → 15s rung
    }
  });

  it('auto + unmeasured width (null) → null so the caller defers the fetch', () => {
    expect(effectivePanelStep(undefined, HOUR, null)).toBeNull();
    expect(effectivePanelStep(0, HOUR, null)).toBeNull();
  });

  it('raw (unbucketed) widths are quantized before the ladder', () => {
    // 610px is not a bucket; must behave exactly like its 600px bucket.
    expect(effectivePanelStep(undefined, HOUR, 610)).toBe(effectivePanelStep(undefined, HOUR, 600));
  });
});

describe('backward-compat — pre-GRAN-C dashboard JSON decodes to auto', () => {
  // Verbatim panel shapes as persisted by pre-v0.8.248 releases (the
  // dashboards table stores panels as a JSON string; no migration rewrites
  // them). Neither config carries a step field.
  const OLD_PANELS_JSON = JSON.stringify([
    { id: 'a1', type: 'metric', title: 'CPU', width: 2,
      config: { metricName: 'system.cpu.utilization', agg: 'avg' } },
    { id: 'b2', type: 'spanmetric', title: 'p99', width: 4,
      config: { agg: 'p99', field: 'duration_ms', dsl: 'service_name = "checkout"' } },
  ]);

  it('old metric panel → step undefined → width-aware auto', () => {
    const panels = JSON.parse(OLD_PANELS_JSON) as Panel[];
    const cfg = panels[0].config as MetricPanelConfig;
    expect(cfg.step).toBeUndefined();
    expect(effectivePanelStep(cfg.step, HOUR, 600)).toBe(15);
  });

  it('old spanmetric panel → step undefined → width-aware auto', () => {
    const panels = JSON.parse(OLD_PANELS_JSON) as Panel[];
    const cfg = panels[1].config as SpanMetricPanelConfig;
    expect(cfg.step).toBeUndefined();
    expect(effectivePanelStep(cfg.step, HOUR, 1200)).toBe(stepForWidth(HOUR, 1200));
  });

  it('post-GRAN-C JSON with a pinned step keeps it', () => {
    const panels = JSON.parse(
      '[{"id":"c3","type":"metric","title":"x","width":2,"config":{"metricName":"m","step":600}}]',
    ) as Panel[];
    expect(effectivePanelStep((panels[0].config as MetricPanelConfig).step, 7 * 86400, 600)).toBe(600);
  });
});

describe('estimatePanelPx — bundle-path width estimate', () => {
  it('grid span fraction of the content width', () => {
    expect(estimatePanelPx(1200, 4)).toBe(1200); // full row
    expect(estimatePanelPx(1200, 2)).toBe(600);  // half
    expect(estimatePanelPx(1200, 1)).toBe(300);  // quarter (quantize floors it to 400 downstream)
  });

  it('degenerate spans clamp into the 1..4 grid', () => {
    expect(estimatePanelPx(1200, 0)).toBe(1200);          // 0/absent → treat as full
    expect(estimatePanelPx(1200, 7 as number)).toBe(1200); // over-span → full
  });

  it('composes with effectivePanelStep for the bundle request', () => {
    // Half-width panel on a 1600px content bucket → 800px → 400 points →
    // 1h raw 9s → 10s rung.
    expect(effectivePanelStep(undefined, HOUR, estimatePanelPx(1600, 2))).toBe(10);
  });
});
