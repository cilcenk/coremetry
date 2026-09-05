// v0.10.411 — bütçe formu ↔ tel çevirisi (aiTuning deseni).
import { describe, it, expect } from 'vitest';
import { budgetToForm, budgetToWire } from './aiBudget';

describe('budgetToForm / budgetToWire', () => {
  it('sıfır = boş kutu, boş kutu = 0', () => {
    expect(budgetToForm({ dailyTokens: 0, dailyCostUsd: 0, p95Ms: 0 })).toEqual({ dailyTokens: '', dailyCostUsd: '', p95Ms: '' });
    expect(budgetToWire({ dailyTokens: '', dailyCostUsd: '', p95Ms: '' })).toEqual({ dailyTokens: 0, dailyCostUsd: 0, p95Ms: 0 });
  });

  it('gidiş-dönüş', () => {
    const b = { dailyTokens: 500000, dailyCostUsd: 12.5, p95Ms: 3000 };
    expect(budgetToWire(budgetToForm(b))).toEqual(b);
  });

  it('sayı olmayan ve kesirli token reddedilir — sessizce 0 yazılmaz', () => {
    expect(typeof budgetToWire({ dailyTokens: 'abc', dailyCostUsd: '', p95Ms: '' })).toBe('string');
    expect(typeof budgetToWire({ dailyTokens: '1.5', dailyCostUsd: '', p95Ms: '' })).toBe('string');
    expect(typeof budgetToWire({ dailyTokens: '', dailyCostUsd: '-1', p95Ms: '' })).toBe('string');
  });
});
