// aiBudget.ts — v0.10.411: bütçe formu ↔ tel çevirisi, SAF (aiTuning
// deseni). Boş kutu 0 ("tavan yok"); sayı olmayan/negatif/kesirli token
// hata metni döner — sessizce 0 yazılmaz.
import type { AIBudget } from '@/lib/types';

export type BudgetForm = { dailyTokens: string; dailyCostUsd: string; p95Ms: string };

export function budgetToForm(b: AIBudget): BudgetForm {
  const s = (n: number) => (n > 0 ? String(n) : '');
  return { dailyTokens: s(b.dailyTokens), dailyCostUsd: s(b.dailyCostUsd), p95Ms: s(b.p95Ms) };
}

/** Form → tel; sayı olmayan alan hata verir (sessizce 0 yazmaz). */
export function budgetToWire(f: BudgetForm): AIBudget | string {
  const num = (label: string, v: string, int: boolean): number | string => {
    const t = v.trim();
    if (t === '') return 0;
    const n = Number(t);
    if (!Number.isFinite(n) || n < 0 || (int && !Number.isInteger(n))) return `${label}: geçersiz sayı`;
    return n;
  };
  const dailyTokens = num('Günlük token', f.dailyTokens, true);
  const dailyCostUsd = num('Günlük $', f.dailyCostUsd, false);
  const p95Ms = num('p95 ms', f.p95Ms, true);
  for (const v of [dailyTokens, dailyCostUsd, p95Ms]) if (typeof v === 'string') return v;
  return { dailyTokens: dailyTokens as number, dailyCostUsd: dailyCostUsd as number, p95Ms: p95Ms as number };
}
