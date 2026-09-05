// v0.10.411 — bütçe rozeti dürüstlük kuralları (CoSRE denetimi E8).
import { describe, it, expect } from 'vitest';
import { budgetVerdict, budgetCls } from './aiBudgetView';
import type { AIBudgetStatus } from '@/lib/types';

const st = (budget: Partial<AIBudgetStatus['budget']>, usage: Partial<AIBudgetStatus['usage']> = {}): AIBudgetStatus => ({
  budget: { dailyTokens: 0, dailyCostUsd: 0, p95Ms: 0, ...budget },
  configured: Object.values(budget).some(v => (v ?? 0) > 0),
  windowS: 86400,
  usage: { calls: 10, inputTokens: 0, outputTokens: 0, p95Ms: 0, byModel: [], ...usage },
});

describe('budgetVerdict', () => {
  it('bütçe yoksa ton yok', () => {
    expect(budgetVerdict(null, null)).toEqual({ text: 'bütçe yok' });
    expect(budgetVerdict(st({}), 1).tone).toBeUndefined();
  });

  it('token oranı: %80 uyarı, %100 aşım', () => {
    expect(budgetVerdict(st({ dailyTokens: 1000 }, { inputTokens: 300, outputTokens: 200 }), null))
      .toEqual({ tone: 'ok', text: 'token %50' });
    expect(budgetVerdict(st({ dailyTokens: 1000 }, { inputTokens: 800 }), null).tone).toBe('warn');
    expect(budgetVerdict(st({ dailyTokens: 1000 }, { inputTokens: 1000 }), null).tone).toBe('over');
  });

  it('maliyet bilinmiyorsa yargılanmaz, "?" yazar — $0 değil', () => {
    const v = budgetVerdict(st({ dailyCostUsd: 10 }), null);
    expect(v.tone).toBeUndefined();
    expect(v.text).toBe('maliyet ?');
  });

  it('en kötü eksen kazanır', () => {
    const v = budgetVerdict(st({ dailyTokens: 1000, dailyCostUsd: 10, p95Ms: 2000 },
      { inputTokens: 100, p95Ms: 2500 }), 1);
    expect(v.tone).toBe('over');
    expect(v.text).toBe('token %10 · maliyet %10 · p95 2500/2000 ms');
    expect(budgetCls(v)).toBe('err');
  });
});
