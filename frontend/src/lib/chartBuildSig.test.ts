// Chart build signatures — v0.8.520/531 (perf proposal #5 + #15).
//
// A build signature is the pure seam behind a chart's "rebuild vs setData"
// decision. Symptom they guard against: uPlot charts destroy()+new uPlot()
// on every 30s poll (canvas flicker, lost hover cursor / zoom / isolation)
// because the whole build effect keyed on the fresh `series` identity. The
// contract this file pins, for each surviving engine:
//   • a DATA-ONLY refresh (same series count/labels/options) → IDENTICAL
//     signature → the build effect is skipped, setData() fast-path runs;
//   • any STRUCTURAL / OPTION change → DIFFERENT signature → full re-create;
//   • callbacks are tracked by PRESENCE, not identity (a fresh arrow each
//     render must not churn); overlays are digested by VALUE, not array
//     identity.
//
// v0.9.844 — `chartBuildSignature` (MLC'nin kendi imzası) ve onun ÜÇ describe
// bloğu SİLİNDİ: eski MultiLineChart gövdesi söküldü, yani imzalayacak bir
// build effect kalmadı. Kalan üç imza (TimeChart / OverviewChart /
// TimeSeriesPanel) bayraksız tek-yol motorlara ait ve aynı kontratı taşıyor.

import { describe, it, expect } from 'vitest';
import {
  timeChartBuildSignature, type TimeChartSigInput,
  overviewChartBuildSignature, type OverviewChartSigInput,
  timeSeriesPanelBuildSignature, type TSPBuildSigInput,
} from './chartBuildSig';

// ─────────────────────────────────────────────────────────────────────────────
// timeChartBuildSignature — v0.8.531 (perf #5/#15). The <TimeChart> seam
// (VolumeChart + ProblemDetail occurrences). Same contract: a data-only poll
// (same series SHAPE, fresh point arrays upstream) → identical signature →
// setData; any shape/unit/overlay/callback-presence change → rebuild. The
// churn this guards: VolumeChart hands a fresh `fmtRight` arrow every render, so
// only fmt PRESENCE — not identity — may live in the signature.
const tcBase: TimeChartSigInput = {
  series: [
    { key: 'total', label: 'ok spans', color: 'var(--accent)', type: 'bar', axis: 'left' },
    { key: 'err', label: 'errors', color: 'var(--err)', type: 'bar', axis: 'left' },
    { key: 'p50', label: 'p50 latency', color: 'var(--orange)', type: 'line', axis: 'right', width: 1.6 },
  ],
  height: 140,
  leftUnit: '',
  rightUnit: ' ms',
  deployMarkers: [1_700_000_000],
  syncKey: 'traces',
  hasBrush: true,
  hasFmtLeft: false,
  hasFmtRight: true,
  hasFmtX: false,
  renderable: true,
};

describe('timeChartBuildSignature — data-only refresh keeps the same signature', () => {
  const same: [string, TimeChartSigInput][] = [
    ['fresh series array, same shape', { ...tcBase, series: tcBase.series.map(s => ({ ...s })) }],
    ['fresh deployMarkers, same values', { ...tcBase, deployMarkers: [...tcBase.deployMarkers!] }],
    ['same brush + fmt presence (fresh arrows upstream)', { ...tcBase, hasBrush: true, hasFmtRight: true }],
  ];
  it.each(same)('%s → identical signature', (_n, input) => {
    expect(timeChartBuildSignature(input)).toBe(timeChartBuildSignature(tcBase));
  });
});

describe('timeChartBuildSignature — structure/option change forces a rebuild', () => {
  const diff: [string, TimeChartSigInput][] = [
    ['series added', { ...tcBase, series: [...tcBase.series, { key: 'x', label: 'x', color: 'var(--teal)', type: 'line' }] }],
    ['series removed', { ...tcBase, series: tcBase.series.slice(0, 2) }],
    ['series recoloured', { ...tcBase, series: [{ ...tcBase.series[0], color: 'var(--teal)' }, tcBase.series[1], tcBase.series[2]] }],
    ['series retyped', { ...tcBase, series: [{ ...tcBase.series[0], type: 'area' }, tcBase.series[1], tcBase.series[2]] }],
    ['series re-axised', { ...tcBase, series: [{ ...tcBase.series[0], axis: 'right' }, tcBase.series[1], tcBase.series[2]] }],
    ['series width changed', { ...tcBase, series: [tcBase.series[0], tcBase.series[1], { ...tcBase.series[2], width: 2.5 }] }],
    ['height changed', { ...tcBase, height: 110 }],
    ['leftUnit changed', { ...tcBase, leftUnit: ' req' }],
    ['rightUnit changed', { ...tcBase, rightUnit: ' s' }],
    ['deployMarkers value changed', { ...tcBase, deployMarkers: [1] }],
    ['deployMarkers added', { ...tcBase, deployMarkers: [1_700_000_000, 1_700_000_600] }],
    ['syncKey changed', { ...tcBase, syncKey: 'other' }],
    ['brush presence toggled off', { ...tcBase, hasBrush: false }],
    ['fmtLeft presence toggled on', { ...tcBase, hasFmtLeft: true }],
    ['fmtX presence toggled on (flips axis space)', { ...tcBase, hasFmtX: true }],
    ['renderable toggled (empty→data)', { ...tcBase, renderable: false }],
  ];
  it.each(diff)('%s → different signature', (_n, input) => {
    expect(timeChartBuildSignature(input)).not.toBe(timeChartBuildSignature(tcBase));
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// overviewChartBuildSignature — v0.8.531. The compact service-Overview RED
// chart seam (Overview + Incident).
const ovBase: OverviewChartSigInput = {
  series: [
    { label: 'p50', color: 'var(--accent)' },
    { label: 'p95', color: 'var(--orange)' },
    { label: 'p99', color: 'var(--err)' },
  ],
  height: 150,
  mode: 'line',
  unit: ' ms',
  deployAtSec: 1_700_000_000,
  deployLabel: 'v1.0.0',
  renderable: true,
};

describe('overviewChartBuildSignature — data-only refresh keeps the same signature', () => {
  const same: [string, OverviewChartSigInput][] = [
    ['fresh series array, same shape', { ...ovBase, series: ovBase.series.map(s => ({ ...s })) }],
    ['deep clone', JSON.parse(JSON.stringify(ovBase))],
  ];
  it.each(same)('%s → identical signature', (_n, input) => {
    expect(overviewChartBuildSignature(input)).toBe(overviewChartBuildSignature(ovBase));
  });
});

describe('overviewChartBuildSignature — structure/option change forces a rebuild', () => {
  const diff: [string, OverviewChartSigInput][] = [
    ['line count changed', { ...ovBase, series: ovBase.series.slice(0, 1) }],
    ['line recoloured', { ...ovBase, series: [{ ...ovBase.series[0], color: 'var(--teal)' }, ovBase.series[1], ovBase.series[2]] }],
    ['mode changed (line→area)', { ...ovBase, mode: 'area' }],
    ['mode changed (line→stacked)', { ...ovBase, mode: 'stacked' }],
    ['unit changed', { ...ovBase, unit: '%' }],
    ['height changed', { ...ovBase, height: 110 }],
    ['deployAtSec changed', { ...ovBase, deployAtSec: 1_700_000_600 }],
    ['deploy cleared', { ...ovBase, deployAtSec: null }],
    ['deployLabel changed', { ...ovBase, deployLabel: 'v2.0.0' }],
    ['renderable toggled', { ...ovBase, renderable: false }],
    // v0.8.534 — drag-zoom presence: the setSelect hook + cursor.drag are
    // wired at build time only when onZoom exists, so a none→some flip must
    // rebuild to register them.
    ['zoom presence toggled on', { ...ovBase, hasZoom: true }],
    // Grafana-parite M1 — cursor.sync build anında kablolanır; key
    // gelişi/değişimi rebuild ister (Service sayfası imleç senkronu).
    ['sync key added', { ...ovBase, syncKey: 'service:api' }],
  ];
  it.each(diff)('%s → different signature', (_n, input) => {
    expect(overviewChartBuildSignature(input)).not.toBe(overviewChartBuildSignature(ovBase));
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// timeSeriesPanelBuildSignature — v0.8.531. The Grafana-grade Explore primitive
// seam. Note the two data-DERIVED-but-structural fields: `hasExemplars` (the
// draw/click hooks are only registered when exemplars are present, so a
// none→some transition must rebuild) and `pointsTier` (the point-dot visibility
// threshold, bucketed so a steady poll keeps the same tier). Exemplar/point
// VALUES ride refs, so they are deliberately NOT in the signature.
const tspBase: TSPBuildSigInput = {
  series: [
    { label: 'rate', color: 'var(--accent)', axis: 'left', unit: ' req/s' },
    { label: 'p95', color: 'var(--orange)', axis: 'right', unit: ' ms', dash: [4, 4] },
  ],
  mode: 'line',
  logScale: false,
  syncKey: 'explore',
  hasZoom: true,
  height: 320,
  deploys: [1_700_000_000_000_000_000],
  events: [{ timeUnixNs: 1_700_000_500_000_000_000, kind: 'deploy', label: 'v1' }],
  thresholds: [{ value: 500, label: 'SLO', color: 'var(--warn)' }],
  hasExemplars: true,
  pointsTier: 1,
  renderable: true,
};

describe('timeSeriesPanelBuildSignature — data-only refresh keeps the same signature', () => {
  const same: [string, TSPBuildSigInput][] = [
    ['fresh series array, same shape', { ...tspBase, series: tspBase.series.map(s => ({ ...s, dash: s.dash ? [...s.dash] : undefined })) }],
    ['fresh deploys, same values', { ...tspBase, deploys: [...tspBase.deploys!] }],
    ['fresh events, same values', { ...tspBase, events: tspBase.events!.map(e => ({ ...e })) }],
    ['fresh thresholds, same values', { ...tspBase, thresholds: tspBase.thresholds!.map(t => ({ ...t })) }],
    // Exemplar VALUES change every poll but presence + tier are stable → same sig.
    ['same exemplar presence + point tier (fresh values upstream)', { ...tspBase, hasExemplars: true, pointsTier: 1 }],
  ];
  it.each(same)('%s → identical signature', (_n, input) => {
    expect(timeSeriesPanelBuildSignature(input)).toBe(timeSeriesPanelBuildSignature(tspBase));
  });
});

describe('timeSeriesPanelBuildSignature — structure/option change forces a rebuild', () => {
  const diff: [string, TSPBuildSigInput][] = [
    ['series added', { ...tspBase, series: [...tspBase.series, { label: 'p99', color: 'var(--err)' }] }],
    ['series renamed', { ...tspBase, series: [{ ...tspBase.series[0], label: 'throughput' }, tspBase.series[1]] }],
    ['series re-axised', { ...tspBase, series: [{ ...tspBase.series[0], axis: 'right' }, tspBase.series[1]] }],
    ['series unit changed', { ...tspBase, series: [{ ...tspBase.series[0], unit: ' rps' }, tspBase.series[1]] }],
    ['series dash changed', { ...tspBase, series: [tspBase.series[0], { ...tspBase.series[1], dash: [2, 2] }] }],
    ['mode changed', { ...tspBase, mode: 'stacked' }],
    ['logScale toggled', { ...tspBase, logScale: true }],
    ['syncKey changed', { ...tspBase, syncKey: 'other' }],
    ['zoom presence toggled off', { ...tspBase, hasZoom: false }],
    ['height changed', { ...tspBase, height: 280 }],
    ['deploy value changed', { ...tspBase, deploys: [1] }],
    ['event kind changed', { ...tspBase, events: [{ ...tspBase.events![0], kind: 'incident' }] }],
    ['threshold value changed', { ...tspBase, thresholds: [{ ...tspBase.thresholds![0], value: 250 }] }],
    ['threshold removed', { ...tspBase, thresholds: [] }],
    ['exemplar presence toggled off', { ...tspBase, hasExemplars: false }],
    ['point tier changed (crosses dot threshold)', { ...tspBase, pointsTier: 0 }],
    ['renderable toggled', { ...tspBase, renderable: false }],
  ];
  it.each(diff)('%s → different signature', (_n, input) => {
    expect(timeSeriesPanelBuildSignature(input)).not.toBe(timeSeriesPanelBuildSignature(tspBase));
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Grafana-parite M3 — x-bölgeleri (problem/anomali pencereleri) her dört
// imzada + renk-tabanlı thresholds TC/OVC'de. Draw hook'lar/plugin'ler bölge
// ve eşik DİZİLERİNİ build anında closure'lar; kontrat: aynı-değerli taze
// dizi (30s poll) imzayı OYNATMAZ (fast-path), değer değişimi (yeni problem
// penceresi / eşik) imzayı OYNATIR (rebuild → taze closure).
describe('Grafana-parite M3 — regions + colour-thresholds in the signatures', () => {
  const region = { fromSec: 1_700_000_000, toSec: 1_700_003_600, color: 'var(--err)', label: 'P1' };
  const cth = { value: 1.5, label: 'err ≤ 1.50%', color: 'var(--err)' };

  // v0.9.844 — DÖRT MLC bölge vakası silindi: chartBuildSignature eski
  // motorla birlikte gitti. Kalan üç imza (TC / OVC / TSP) aynı
  // regionsDigest'i paylaşıyor, yani kontrat hâlâ kapılı.

  it('TC: fresh thresholds/regions, same values → identical signature', () => {
    expect(timeChartBuildSignature({ ...tcBase, thresholds: [{ ...cth }], regions: [{ ...region }] }))
      .toBe(timeChartBuildSignature({ ...tcBase, thresholds: [{ ...cth }], regions: [{ ...region }] }));
  });
  it('TC: threshold value changed → different signature', () => {
    expect(timeChartBuildSignature({ ...tcBase, thresholds: [{ ...cth, value: 3 }] }))
      .not.toBe(timeChartBuildSignature({ ...tcBase, thresholds: [cth] }));
  });
  it('TC: region label changed → different signature', () => {
    expect(timeChartBuildSignature({ ...tcBase, regions: [{ ...region, label: 'OPEN' }] }))
      .not.toBe(timeChartBuildSignature({ ...tcBase, regions: [region] }));
  });

  it('OVC: fresh thresholds/regions, same values → identical signature', () => {
    expect(overviewChartBuildSignature({ ...ovBase, thresholds: [{ ...cth }], regions: [{ ...region }] }))
      .toBe(overviewChartBuildSignature({ ...ovBase, thresholds: [{ ...cth }], regions: [{ ...region }] }));
  });
  it('OVC: threshold appears (none→some) → different signature', () => {
    expect(overviewChartBuildSignature({ ...ovBase, thresholds: [cth] }))
      .not.toBe(overviewChartBuildSignature(ovBase));
  });
  it('OVC: region colour changed → different signature', () => {
    expect(overviewChartBuildSignature({ ...ovBase, regions: [{ ...region, color: 'var(--warn)' }] }))
      .not.toBe(overviewChartBuildSignature({ ...ovBase, regions: [region] }));
  });

  it('TSP: fresh regions, same values → identical signature', () => {
    expect(timeSeriesPanelBuildSignature({ ...tspBase, regions: [{ ...region }] }))
      .toBe(timeSeriesPanelBuildSignature({ ...tspBase, regions: [{ ...region }] }));
  });
  it('TSP: region window changed → different signature', () => {
    expect(timeSeriesPanelBuildSignature({ ...tspBase, regions: [{ ...region, fromSec: 1 }] }))
      .not.toBe(timeSeriesPanelBuildSignature({ ...tspBase, regions: [region] }));
  });
});
