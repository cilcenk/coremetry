import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { liftBadge } from './logFieldLift';

// v0.10.509 (C5) — lift rozeti: ±5 puan altı düz; kapalıyken yok.
describe('liftBadge', () => {
  it('kapalı ya da lift yok → none', () => {
    expect(liftBadge({ lift: 30 }, false)).toEqual({ kind: 'none' });
    expect(liftBadge({}, true)).toEqual({ kind: 'none' });
  });
  it('yukarı / aşağı / düz', () => {
    expect(liftBadge({ lift: 42.4 }, true)).toMatchObject({ kind: 'up', label: '+42 pt' });
    expect(liftBadge({ lift: -7.25 }, true)).toMatchObject({ kind: 'down', label: '-7.3 pt' });
    expect(liftBadge({ lift: 2 }, true)).toMatchObject({ kind: 'flat', label: '+2.0 pt' });
  });
});

// Kaynak pini: panel düğmesi yalnız genişletilen alanın sorgusuna errorLift ekler.
describe('LogFieldsPanel hatalıyı ayıran (v0.10.509)', () => {
  const src = readFileSync(resolve(__dirname, '../components/LogFieldsPanel.tsx'), 'utf8');
  it('errorLift sorgu anahtarında ve parametrede; rozet liftBadge ile', () => {
    expect(src).toContain("...(lift ? { errorLift: 1 as const } : {})");
    expect(src).toContain("queryKey: ['logs', 'fieldstats', field, scope, size, lift]");
    expect(src).toContain('liftBadge(v, !!lift && !!d.errorLift && !d.errorLift.degraded)');
  });
});
