import { describe, expect, it } from 'vitest';
import { retentionSummaryTR, retentionToForm, retentionToWire } from './aiChatRetention';

// v0.10.561 — sohbet arşivi saklama formu.
describe('aiChatRetention', () => {
  it('form ↔ tel', () => {
    expect(retentionToForm({ days: 30 })).toBe('30');
    expect(retentionToForm(null)).toBe('');
    expect(retentionToWire('')).toEqual({ days: 90 });
    expect(retentionToWire('0')).toEqual({ days: 0 });
    expect(retentionToWire(' 45 ')).toEqual({ days: 45 });
    expect(typeof retentionToWire('abc')).toBe('string');
    expect(typeof retentionToWire('-1')).toBe('string');
    expect(typeof retentionToWire('4000')).toBe('string');
  });
  it('özet', () => {
    expect(retentionSummaryTR({ days: 0 })).toMatch(/kapalı/);
    expect(retentionSummaryTR({ days: 90 })).toMatch(/90 gün/);
  });
});
