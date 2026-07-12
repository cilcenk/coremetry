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
import { chartBuildSignature, type ChartBuildSigInput } from './chartBuildSig';

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
