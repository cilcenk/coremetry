// rolloutRow.test.ts — v0.10.201 sözleşmesi (rolloutRow.ts başlığı).
import { describe, it, expect } from 'vitest';
import { rolloutKey, upsertRollouts, statusTone, rolloutDurationSec, shortRevision, imageDiff } from './rolloutRow';
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
