// AI cost rate utilities (v0.5.167).
//
// Cost computation lives client-side because:
//   • Rates churn (OpenAI publishes new prices monthly); operators
//     can override via /api/ai/rates without redeploying the server.
//   • The /ai page already loads ai_calls rows — multiplying by
//     a rate is a no-op join, no second backend round-trip.
//   • Local models (Ollama, vLLM, LM Studio) are free — we don't
//     want a hardcoded $-per-1M floor sneaking into the table.
//
// `BUNDLED_RATES` is a starting point sourced from each provider's
// public pricing page (snapshotted, not live). Operators with
// per-account discounts or unusual model picks edit the values
// in Settings; overrides win key-by-key (no need to re-quote
// untouched models).
import type { AIRate } from './types';

export type AIRateTable = Record<string, AIRate>;

// USD per 1M tokens — verified against each provider's public
// pricing page on 2025-12 (snapshot, not live). When the table
// drifts an operator can override entries via Settings without
// re-deploying; an override-empty install starts seeing accurate
// numbers right away.
export const BUNDLED_RATES: AIRateTable = {
  // OpenAI
  'gpt-4o':              { inputPer1M: 2.50, outputPer1M: 10.00 },
  'gpt-4o-mini':         { inputPer1M: 0.15, outputPer1M: 0.60 },
  'gpt-4-turbo':         { inputPer1M: 10.00, outputPer1M: 30.00 },
  'gpt-4':               { inputPer1M: 30.00, outputPer1M: 60.00 },
  'gpt-3.5-turbo':       { inputPer1M: 0.50, outputPer1M: 1.50 },
  'o1-preview':          { inputPer1M: 15.00, outputPer1M: 60.00 },
  'o1-mini':             { inputPer1M: 3.00, outputPer1M: 12.00 },
  // Anthropic
  'claude-opus-4-7':           { inputPer1M: 15.00, outputPer1M: 75.00 },
  'claude-opus-4-6':           { inputPer1M: 15.00, outputPer1M: 75.00 },
  'claude-sonnet-4-6':         { inputPer1M: 3.00,  outputPer1M: 15.00 },
  'claude-haiku-4-5':          { inputPer1M: 0.80,  outputPer1M: 4.00 },
  'claude-3-5-sonnet-20241022': { inputPer1M: 3.00,  outputPer1M: 15.00 },
  'claude-3-5-haiku-20241022':  { inputPer1M: 0.80,  outputPer1M: 4.00 },
  'claude-3-opus-20240229':     { inputPer1M: 15.00, outputPer1M: 75.00 },
  // Common local / OSS — explicit 0 so the UI doesn't flag them
  // as "unknown rate" and operators see "free" instead.
  'llama3.1:8b':         { inputPer1M: 0, outputPer1M: 0 },
  'llama3.1:70b':        { inputPer1M: 0, outputPer1M: 0 },
  'llama3.2':            { inputPer1M: 0, outputPer1M: 0 },
  'qwen2.5:7b':          { inputPer1M: 0, outputPer1M: 0 },
  'qwen2.5:14b':         { inputPer1M: 0, outputPer1M: 0 },
  'qwen2.5-coder':       { inputPer1M: 0, outputPer1M: 0 },
  'mistral':             { inputPer1M: 0, outputPer1M: 0 },
  'mistral-nemo':        { inputPer1M: 0, outputPer1M: 0 },
  'phi3.5':              { inputPer1M: 0, outputPer1M: 0 },
};

// Merge bundled defaults with operator overrides — overrides win
// key-by-key so an admin re-quoting one model doesn't clear the
// rest of the table.
export function mergeRates(overrides: AIRateTable | null | undefined): AIRateTable {
  return { ...BUNDLED_RATES, ...(overrides ?? {}) };
}

// Best-effort rate lookup. Falls back to a fuzzy match on the
// model prefix (so "llama3.1:8b-instruct-q4" still hits
// "llama3.1:8b") before giving up. Unknown returns null — the UI
// surfaces this as "—" rather than a misleading $0.
export function rateFor(rates: AIRateTable, model: string): AIRate | null {
  if (!model) return null;
  if (rates[model]) return rates[model];
  // Fuzzy prefix — common for Ollama tags like ":latest", ":q4_k_m".
  for (const key of Object.keys(rates)) {
    if (model.startsWith(key)) return rates[key];
  }
  return null;
}

// Dollar cost for one call given its token counts + model rate.
// Returns null when no rate is known so the UI can render "—".
export function costForCall(rates: AIRateTable, model: string,
                            inputTokens: number, outputTokens: number): number | null {
  const r = rateFor(rates, model);
  if (r === null) return null;
  return (inputTokens / 1_000_000) * r.inputPer1M
       + (outputTokens / 1_000_000) * r.outputPer1M;
}

// Compact $ display — $0.0023 / $0.45 / $12.30 / $1.2k. The /ai
// page packs many rows so we keep this short.
export function fmtCost(cost: number | null): string {
  if (cost === null) return '—';
  if (cost === 0) return '$0';
  if (cost < 0.01) return '$' + cost.toFixed(4);
  if (cost < 1)    return '$' + cost.toFixed(3);
  if (cost < 100)  return '$' + cost.toFixed(2);
  if (cost < 1000) return '$' + cost.toFixed(0);
  return '$' + (cost / 1000).toFixed(1) + 'k';
}
