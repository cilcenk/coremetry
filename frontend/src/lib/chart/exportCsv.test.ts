import { describe, it, expect } from 'vitest';
import { alignedToCsv, csvEscape } from './exportCsv';

describe('csvEscape (RFC 4180)', () => {
  it('düz alan olduğu gibi', () => {
    expect(csvEscape('payments')).toBe('payments');
  });
  it('virgül/tırnak/satır sonu tırnaklanır, iç tırnak ikilenir', () => {
    expect(csvEscape('a,b')).toBe('"a,b"');
    expect(csvEscape('x "y" z')).toBe('"x ""y"" z"');
    expect(csvEscape('a\nb')).toBe('"a\nb"');
  });
});

describe('alignedToCsv', () => {
  const t = [1700000000000, 1700000010000]; // ms
  it('başlık + ISO zaman + değerler', () => {
    const csv = alignedToCsv(['p50', 'p99'], [t, [1, 2], [10, 20]]);
    const lines = csv.trimEnd().split('\n');
    expect(lines[0]).toBe('time,p50,p99');
    expect(lines[1]).toBe('2023-11-14T22:13:20.000Z,1,10');
    expect(lines[2]).toBe('2023-11-14T22:13:30.000Z,2,20');
  });

  it('null hücre BOŞ — sıfır yazılmaz', () => {
    const csv = alignedToCsv(['s'], [t, [null, 3]]);
    expect(csv).toContain('2023-11-14T22:13:20.000Z,\n');
    expect(csv).not.toContain(',0\n');
  });

  it('seri adındaki virgül başlığı bozmaz', () => {
    const csv = alignedToCsv(['svc,region'], [[t[0]], [1]]);
    expect(csv.split('\n')[0]).toBe('time,"svc,region"');
  });

  it('boş veri → yalnız başlık', () => {
    expect(alignedToCsv([], [[]])).toBe('time\n');
  });
});
