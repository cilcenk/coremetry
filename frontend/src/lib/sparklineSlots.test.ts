import { describe, it, expect } from 'vitest';
import { SPARK_SLOT_RUNGS, SPARK_DEFAULT_WIDTH, sparkMaxSlotsForWidth } from './sparkline';

describe('sparkMaxSlotsForWidth (v0.10.286)', () => {
  it('varsayılan 80 px → 80 slot; küçük genişlik 40; geniş 120 tavan', () => {
    expect(sparkMaxSlotsForWidth(SPARK_DEFAULT_WIDTH)).toBe(80);
    expect(sparkMaxSlotsForWidth(30)).toBe(40);
    expect(sparkMaxSlotsForWidth(61)).toBe(80);
    expect(sparkMaxSlotsForWidth(100)).toBe(120);
    expect(sparkMaxSlotsForWidth(400)).toBe(120);
  });
  it('geçersiz genişlik tavana düşer; basamaklar artan', () => {
    expect(sparkMaxSlotsForWidth(0)).toBe(120);
    expect(sparkMaxSlotsForWidth(NaN)).toBe(120);
    expect([...SPARK_SLOT_RUNGS].sort((a, b) => a - b)).toEqual(SPARK_SLOT_RUNGS);
  });
});
