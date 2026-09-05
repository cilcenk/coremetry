// v0.10.411 — AI bütçe rozetinin SAF karar mantığı (CoSRE denetimi E8).
//
// rcaQualityView ile aynı dürüstlük kuralı: bilinmeyen "sıfır" değil.
// Fiyatı bilinmeyen model maliyeti null döner; maliyet tavanı varken
// maliyet bilinmiyorsa o eksen YARGILANMAZ ve rozet bunu söyler —
// yalnız yerel model koşan kurulum "$0, bütçe içinde" okumasın.
import type { AIBudgetStatus } from '@/lib/types';

export type BudgetTone = 'ok' | 'warn' | 'over';

export interface BudgetVerdict {
  /** undefined = bütçe tanımlı değil ya da hiçbir eksen yargılanamadı */
  tone?: BudgetTone;
  /** KPI karosunda gösterilen kısa metin */
  text: string;
}

const WARN_AT = 0.8;

function toneOf(ratio: number): BudgetTone {
  if (ratio >= 1) return 'over';
  if (ratio >= WARN_AT) return 'warn';
  return 'ok';
}

const RANK: Record<BudgetTone, number> = { ok: 0, warn: 1, over: 2 };

/**
 * Bütçe kararı — yargılanan her eksende oran; en kötü ton kazanır.
 * @param cost son 24 saatin dolar maliyeti; null = fiyatı bilinmeyen model(ler)
 */
export function budgetVerdict(st: AIBudgetStatus | null | undefined, cost: number | null): BudgetVerdict {
  if (!st || !st.configured) return { text: 'bütçe yok' };
  const parts: string[] = [];
  let tone: BudgetTone | undefined;
  const bump = (t: BudgetTone) => { if (tone === undefined || RANK[t] > RANK[tone]) tone = t; };
  const b = st.budget;
  const u = st.usage;
  if (b.dailyTokens > 0) {
    const r = (u.inputTokens + u.outputTokens) / b.dailyTokens;
    parts.push(`token %${(r * 100).toFixed(0)}`);
    bump(toneOf(r));
  }
  if (b.dailyCostUsd > 0) {
    if (cost === null) {
      parts.push('maliyet ?');
    } else {
      const r = cost / b.dailyCostUsd;
      parts.push(`maliyet %${(r * 100).toFixed(0)}`);
      bump(toneOf(r));
    }
  }
  if (b.p95Ms > 0) {
    const r = u.p95Ms / b.p95Ms;
    parts.push(`p95 ${u.p95Ms.toFixed(0)}/${b.p95Ms} ms`);
    bump(toneOf(r));
  }
  return { tone, text: parts.join(' · ') };
}

/** KPI karosu renk sınıfı — over kırmızı. */
export function budgetCls(v: BudgetVerdict): 'ok' | 'warn' | 'err' | undefined {
  if (v.tone === 'over') return 'err';
  return v.tone;
}
