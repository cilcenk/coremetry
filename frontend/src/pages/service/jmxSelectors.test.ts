import { describe, it, expect } from 'vitest';
import { dsToken, reconcile, isDatasourcePanel, applyDsIsolate } from './jmxSelectors';

// v0.9.150 — regression guard for the Service→Infra JMX Pod/Datasource
// selectors. Adversarial review of v0.9.149 confirmed SIX ways a stale or
// over-broad selector blanked the JMX section; each block below pins one.

describe('dsToken — datasource is always the first " · " token', () => {
  it('By-datasource name is the datasource itself', () => {
    expect(dsToken('ExampleDS')).toBe('ExampleDS');
  });
  it('By-pod name (datasource · pod) → datasource', () => {
    expect(dsToken('ExampleDS · app-7d9f-x2')).toBe('ExampleDS');
  });
  it('XA datasource (xa label first-non-empty) still resolves', () => {
    expect(dsToken('ExampleXADS · app-7d9f-x2')).toBe('ExampleXADS');
  });
  it('non-datasource jboss metric (empty name) → ""', () => {
    expect(dsToken('')).toBe('');
  });
});

describe('reconcile — stale selection falls back to "all" (review #2/#3/#5)', () => {
  const pods = ['app-a-1', 'app-a-2'];
  it('keeps a live selection', () => {
    expect(reconcile('app-a-1', pods)).toBe('app-a-1');
  });
  it('drops a pod from a previous cluster / pre-deploy name → ""', () => {
    expect(reconcile('app-b-9', pods)).toBe('');
  });
  it('drops a datasource that vanished after a re-fetch → ""', () => {
    expect(reconcile('DS-A', [])).toBe('');
  });
  it('empty selection stays empty', () => {
    expect(reconcile('', pods)).toBe('');
  });
});

describe('isDatasourcePanel — only datasource panels are filterable (review #1)', () => {
  it('true when a series carries a datasource', () => {
    expect(isDatasourcePanel(['DS-A', 'DS-B'])).toBe(true);
  });
  it('false for a non-datasource jboss panel (single empty-named series)', () => {
    expect(isDatasourcePanel([''])).toBe(false);
  });
  it('false for an empty series set', () => {
    expect(isDatasourcePanel([])).toBe(false);
  });
});

describe('applyDsIsolate — non-datasource panels survive a datasource pick (review #1)', () => {
  const dsPanel = [{ name: 'DS-A' }, { name: 'DS-B' }, { name: 'DS-C' }];
  const nonDsPanel = [{ name: '' }]; // e.g. jboss_undertow_request_count

  it('isolates a datasource panel to the picked datasource', () => {
    expect(applyDsIsolate(dsPanel, 'DS-B')).toEqual([{ name: 'DS-B' }]);
  });
  it('leaves a non-datasource panel UNTOUCHED when a datasource is picked', () => {
    // The v0.9.149 bug: dsToken("")==="" never equalled "DS-B", so the panel
    // was filtered to [] and vanished. It must pass through instead.
    expect(applyDsIsolate(nonDsPanel, 'DS-B')).toEqual(nonDsPanel);
  });
  it('empty filter shows every series', () => {
    expect(applyDsIsolate(dsPanel, '')).toEqual(dsPanel);
  });
  it('By-pod datasource series isolate by their datasource token', () => {
    const byPod = [{ name: 'DS-A · pod-1' }, { name: 'DS-B · pod-1' }, { name: 'DS-A · pod-2' }];
    expect(applyDsIsolate(byPod, 'DS-A')).toEqual([{ name: 'DS-A · pod-1' }, { name: 'DS-A · pod-2' }]);
  });
});
