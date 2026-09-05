import { describe, it, expect } from 'vitest';
import { shouldNudgeExplain, type TraceNudgeInput } from './traceNudge';

// v0.10.432 (D8) — her kapı tek tek: yalnız /trace, id'li, çekmece kapalı,
// ret yok, bu sekmede sorulmamış.
describe('shouldNudgeExplain', () => {
  const base: TraceNudgeInput = { pathname: '/trace', traceId: 'abc', aiOpen: false, declined: false, askedThisTab: false };
  it('shows on a fresh trace open', () => {
    expect(shouldNudgeExplain(base)).toBe(true);
  });
  it.each<[string, Partial<TraceNudgeInput>]>([
    ['public trace never', { pathname: '/public/trace' }],
    ['other page', { pathname: '/traces' }],
    ['no trace id', { traceId: '' }],
    ['drawer already open', { aiOpen: true }],
    ['declined forever', { declined: true }],
    ['asked this tab', { askedThisTab: true }],
    ['span panel open (v0.10.445)', { spanOpen: true }],
  ])('%s → hidden', (_name, patch) => {
    expect(shouldNudgeExplain({ ...base, ...patch })).toBe(false);
  });
});
