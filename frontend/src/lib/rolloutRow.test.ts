// rolloutRow.test.ts — v0.10.201 sözleşmesi (rolloutRow.ts başlığı).
import { describe, it, expect } from 'vitest';
import { rolloutKey, upsertRollouts, statusTone, rolloutDurationSec, shortRevision, imageDiff, rolloutTracesFilters } from './rolloutRow';
import type { WorkloadRollout } from './types';

const base: WorkloadRollout = {
  clusterId: 'c-1', namespace: 'pay', workload: 'api', kind: 'Deployment', revision: 'api-7fb7dffckb',
  startedAt: 1_700_000_000_000, status: 'in_progress', prevRevision: 'api-6c9d', image: 'reg/api', imageTag: 'release.2', prevImage: 'reg/api', prevImageTag: 'release.1',
  firstSpanAt: 0, trafficConfirmedAt: 0, ksmStartedAt: 0, podsReadyAt: 0, ksmNotReadySince: 0, completedAt: 0,
  detectedBy: 'spans', spanCount: 10, note: '', updatedAt: 1_700_000_100_000,
};

describe('rolloutRow', () => {
  it('kimlik beş parçadan; startedAt farkı ayrı satır', () => {
    expect(rolloutKey(base)).toBe('c-1|pay|api|api-7fb7dffckb|1700000000000');
    expect(rolloutKey({ ...base, startedAt: 1 })).not.toBe(rolloutKey(base));
  });
  it('upsert: yalnız daha yeni updatedAt ezer; yeni kimlik eklenir; eski sıra korunur', () => {
    const older = { ...base, status: 'completed', updatedAt: base.updatedAt - 1 };
    const newer = { ...base, status: 'completed', updatedAt: base.updatedAt + 1 };
    const other = { ...base, revision: 'api-zzzz', updatedAt: 5 };
    let rows = upsertRollouts([base], [older]);
    expect(rows[0].status).toBe('in_progress');
    rows = upsertRollouts(rows, [newer, other]);
    expect(rows[0].status).toBe('completed');
    expect(rows).toHaveLength(2);
    expect(rows[1].revision).toBe('api-zzzz');
    expect(upsertRollouts(rows, [])).toBe(rows);
  });
  it('durum tonu + süre + kısa revizyon + imaj diff', () => {
    expect(statusTone('completed')).toBe('success');
    expect(statusTone('rolled_back')).toBe('danger');
    expect(statusTone('stalled')).toBe('warning');
    expect(statusTone('x')).toBe('neutral');
    expect(rolloutDurationSec(base, base.startedAt + 90_000)).toBe(90);
    expect(rolloutDurationSec({ ...base, completedAt: base.startedAt + 30_000 }, base.startedAt + 90_000)).toBe(30);
    expect(shortRevision('api-7fb7dffckb', 'api')).toBe('7fb7dffckb');
    expect(shortRevision('7fb7dffckb', 'api')).toBe('7fb7dffckb');
    expect(imageDiff(base)).toBe('release.1 → release.2');
    expect(imageDiff({ imageTag: 'r1', prevImageTag: 'r1' })).toBe('r1');
    expect(imageDiff({ imageTag: '', prevImageTag: '' })).toBe('—');
  });
});

// v0.10.211 — STS/DS revizyonu imaj tag'idir: replicaset filtresi yerine
// workload + imaj tag'i. Deployment eski sözleşmede kalır.
describe('rolloutTracesFilters', () => {
  it('Deployment → replicaset + namespace', () => {
    const f = rolloutTracesFilters({ kind: 'Deployment', namespace: 'demo', workload: 'api', revision: 'api-7fb7dffckb' });
    expect(f).toEqual([
      { k: 'resource.k8s.replicaset.name', op: '=', v: ['api-7fb7dffckb'] },
      { k: 'resource.k8s.namespace.name', op: '=', v: ['demo'] },
    ]);
  });
  it('StatefulSet/DaemonSet → workload + imaj tag + namespace', () => {
    const f = rolloutTracesFilters({ kind: 'StatefulSet', namespace: 'db', workload: 'pg', revision: 'release.20260807.1' });
    expect(f[0]).toEqual({ k: 'resource.k8s.statefulset.name', op: '=', v: ['pg'] });
    expect(f[1]).toEqual({ k: 'resource.container.image.tag', op: '=', v: ['release.20260807.1'] });
    const d = rolloutTracesFilters({ kind: 'DaemonSet', namespace: 'sys', workload: 'agent', revision: 't2' });
    expect(d[0].k).toBe('resource.k8s.daemonset.name');
  });
  it("bilinmeyen/boş tür Deployment gibi davranır (eski satırlar)", () => {
    expect(rolloutTracesFilters({ kind: '', namespace: 'n', workload: 'w', revision: 'w-abc' })[0].k).toBe('resource.k8s.replicaset.name');
  });
});

// v0.10.234 — Operator-reported: çekmecede "hangi sürümden hangisine" görünmüyordu,
// "devralındı" olayın türünü söylemiyordu. Değişiklik türü imaj kimliğinden.
import { rolloutChangeKind, changeKindLabel, statusTitle, imageRef } from './rolloutRow';
describe('rolloutChangeKind', () => {
  const base = { prevRevision: 'w-aaa', image: 'reg/app', imageTag: '2.0', prevImage: 'reg/app', prevImageTag: '1.0' };
  it('imaj tag değişti → deployment', () => {
    expect(rolloutChangeKind(base)).toBe('deployment');
    expect(changeKindLabel('deployment')).toBe('Deployment');
  });
  it('imaj repo değişti, tag aynı → deployment', () => {
    expect(rolloutChangeKind({ ...base, prevImage: 'reg/other', imageTag: '1.0' })).toBe('deployment');
  });
  it('imaj aynı, revizyon farklı → config rollout', () => {
    expect(rolloutChangeKind({ ...base, imageTag: '1.0' })).toBe('config');
    expect(changeKindLabel('config')).toBe('Rollout (config)');
  });
  it('önceki revizyon yok → ilk gözlem (imaj olsa da)', () => {
    expect(rolloutChangeKind({ ...base, prevRevision: '' })).toBe('initial');
  });
  it('imaj bilgisi bir tarafta yok → bilinmiyor', () => {
    expect(rolloutChangeKind({ ...base, prevImage: '', prevImageTag: '' })).toBe('unknown');
    expect(rolloutChangeKind({ ...base, image: '', imageTag: '', prevImage: '', prevImageTag: '' })).toBe('unknown');
  });
  it('durum ipuçları her durumda dolu, bilinmeyen boş', () => {
    for (const st of ['in_progress', 'completed', 'rolled_back', 'superseded', 'stalled']) expect(statusTitle(st)).not.toBe('');
    expect(statusTitle('weird')).toBe('');
  });
  it('imageRef', () => {
    expect(imageRef('reg/app', '1.0')).toBe('reg/app:1.0');
    expect(imageRef('reg/app', '')).toBe('reg/app');
    expect(imageRef('', '')).toBe('—');
  });
});
