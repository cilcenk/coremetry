// msgOperations.test.ts — v0.10.563. '' = "SDK yaymıyor", 0 çağrı DEĞİL;
// bilinen semconv sözcükleri ÇEVRİLMEZ; null/undefined zarf boş diziye düşer.
import { describe, it, expect } from 'vitest';
import { opLabelTR, isOpMissing, msgOperationRows, OP_MISSING_LABEL } from './msgOperations';
import type { MsgOperationStat } from '@/lib/types';

describe('opLabelTR', () => {
  it("boş / boşluk / null / undefined → '(yaymıyor)'", () => {
    for (const v of ['', '   ', null, undefined]) {
      expect(opLabelTR(v)).toBe(OP_MISSING_LABEL);
      expect(isOpMissing(v)).toBe(true);
    }
  });

  it('semconv sözcükleri olduğu gibi kalır (çeviri YOK)', () => {
    for (const op of ['publish', 'receive', 'process', 'settle', 'create']) {
      expect(opLabelTR(op)).toBe(op);
      expect(isOpMissing(op)).toBe(false);
    }
  });

  it('bilinmeyen tür de aynen geçer — beyaz liste yok', () => {
    expect(opLabelTR('deliver')).toBe('deliver');
  });

  it('kenar boşlukları kırpılır ama sözcük bozulmaz', () => {
    expect(opLabelTR('  publish ')).toBe('publish');
  });
});

describe('msgOperationRows', () => {
  const row: MsgOperationStat = {
    operation: 'publish', spanCount: 3, errorCount: 0, errorRate: 0,
    avgDurationMs: 1, p50DurationMs: 1, p95DurationMs: 2, p99DurationMs: 3,
  };

  it('undefined (pre-563 önbellek) ve null (Go nil) → []', () => {
    expect(msgOperationRows(undefined)).toEqual([]);
    expect(msgOperationRows(null)).toEqual([]);
  });

  it('dolu dizi aynen döner', () => {
    expect(msgOperationRows([row])).toEqual([row]);
  });

  it('boş dizi ile "alan yok" ayrımı ÇAĞIRANDA kalmaz — ikisi de []', () => {
    expect(msgOperationRows([])).toEqual([]);
  });
});
