// chartBuildSignature — v0.8.520 (perf proposal #5 + #15).
//
// The build signature is the pure seam behind MultiLineChart's "rebuild vs
// setData" decision. Symptom it guards against: uPlot charts destroy()+new
// uPlot() on every 30s poll (canvas flicker, lost hover cursor / zoom /
// isolation) because the whole build effect keyed on the fresh `series`
// identity. The contract this file pins:
//   • a DATA-ONLY refresh (same series count/labels/options) → IDENTICAL
//     signature → the build effect is skipped, setData() fast-path runs;
//   • any STRUCTURAL / OPTION change → DIFFERENT signature → full re-create;
//   • callbacks are tracked by PRESENCE, not identity (a fresh arrow each
//     render must not churn); overlays are digested by VALUE, not array
//     identity.

import { describe, it, expect } from 'vitest';
import {
  chartBuildSignature, type ChartBuildSigInput,
  timeChartBuildSignature, type TimeChartSigInput,
  overviewChartBuildSignature, type OverviewChartSigInput,
  timeSeriesPanelBuildSignature, type TSPBuildSigInput,
} from './chartBuildSig';

const base: ChartBuildSigInput = {
  labels: ['frontend', 'checkout', 'cart'],
  unit: 'ms',
  height: 320,
  syncKey: 'svc',
  logScale: false,
  hasZoom: true,
  hasBucketClick: false,
  compareOffsetNs: 0,
  compareLabel: '',
  deploys: [{ timeUnixNs: 1_700_000_000_000_000_000, label: 'v1.2.3', description: '153 spans' }],
  thresholds: [{ value: 500, label: 'SLO 500ms', severity: 'warn' }],
};

const clone = (o: ChartBuildSigInput): ChartBuildSigInput => JSON.parse(JSON.stringify(o));

describe('chartBuildSignature — data-only refresh keeps the same signature (setData fast-path)', () => {
  // Each case is the SAME chart after a poll: the caller hands a brand-new
  // object / arrays, but the structure + options are unchanged. All must equal
  // the base signature so the build effect is skipped.
  const sameCases: [string, ChartBuildSigInput][] = [
    ['fresh object, identical contents', clone(base)],
    ['fresh labels array, same names/order', { ...base, labels: [...base.labels] }],
    ['fresh deploys array, same values', { ...base, deploys: base.deploys!.map(d => ({ ...d })) }],
    ['fresh thresholds array, same values', { ...base, thresholds: base.thresholds!.map(t => ({ ...t })) }],
    // onZoom / onBucketClick are booleans here — same presence ⇒ same sig even
    // though the caller passed a different closure identity upstream.
    ['same zoom/bucket presence', { ...base, hasZoom: true, hasBucketClick: false }],
  ];
  it.each(sameCases)('%s → identical signature', (_name, input) => {
    expect(chartBuildSignature(input)).toBe(chartBuildSignature(base));
  });
});

describe('chartBuildSignature — structural / option change forces a rebuild', () => {
  const diffCases: [string, ChartBuildSigInput][] = [
    ['series added', { ...base, labels: [...base.labels, 'payments'] }],
    ['series removed', { ...base, labels: base.labels.slice(0, 2) }],
    ['series renamed', { ...base, labels: ['frontend', 'checkout', 'basket'] }],
    ['series reordered', { ...base, labels: ['checkout', 'frontend', 'cart'] }],
    ['unit changed', { ...base, unit: 's' }],
    ['height changed', { ...base, height: 280 }],
    ['syncKey changed', { ...base, syncKey: 'other' }],
    ['logScale toggled', { ...base, logScale: true }],
    ['zoom presence toggled off', { ...base, hasZoom: false }],
    ['bucket-click presence toggled on', { ...base, hasBucketClick: true }],
    ['compareOffsetNs changed', { ...base, compareOffsetNs: 86_400_000_000_000 }],
    ['compareLabel changed', { ...base, compareLabel: '24h ago' }],
    ['deploy time changed', { ...base, deploys: [{ ...base.deploys![0], timeUnixNs: 1 }] }],
    ['deploy label changed', { ...base, deploys: [{ ...base.deploys![0], label: 'v9' }] }],
    ['deploy added', { ...base, deploys: [...base.deploys!, { timeUnixNs: 2, label: 'v2' }] }],
    ['threshold value changed', { ...base, thresholds: [{ ...base.thresholds![0], value: 250 }] }],
    ['threshold severity changed', { ...base, thresholds: [{ ...base.thresholds![0], severity: 'err' }] }],
    ['threshold removed', { ...base, thresholds: [] }],
  ];
  it.each(diffCases)('%s → different signature', (_name, input) => {
    expect(chartBuildSignature(input)).not.toBe(chartBuildSignature(base));
  });
});

describe('chartBuildSignature — optional-field normalisation', () => {
  it('treats undefined optionals as their stable defaults', () => {
    const a: ChartBuildSigInput = { labels: ['a'], height: 320, hasZoom: false, hasBucketClick: false };
    const b: ChartBuildSigInput = {
      labels: ['a'], height: 320, hasZoom: false, hasBucketClick: false,
      unit: '', syncKey: '', logScale: false, compareOffsetNs: 0, compareLabel: '',
      deploys: [], thresholds: [],
    };
    expect(chartBuildSignature(a)).toBe(chartBuildSignature(b));
  });

  it('empty vs one deploy differ (empty-overlay guard)', () => {
    const empty: ChartBuildSigInput = { labels: ['a'], height: 320, hasZoom: false, hasBucketClick: false };
    const withDeploy: ChartBuildSigInput = { ...empty, deploys: [{ timeUnixNs: 1, label: 'v1' }] };
    expect(chartBuildSignature(empty)).not.toBe(chartBuildSignature(withDeploy));
  });
});

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
