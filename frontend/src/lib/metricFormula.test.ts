import { describe, expect, it } from 'vitest';
import { evalExpr, exprRefs } from './metricFormula';

// Pins the metric-editor formula evaluator (v0.7.128). The error-rate% case
// (A / B * 100) is the headline; the rest guard precedence, parens, unary
// minus, divide-by-zero, unknown ids, and trailing-garbage rejection — the
// classic ways a hand-rolled arithmetic parser goes subtly wrong.
describe('evalExpr', () => {
  const V = { A: 6, B: 200, C: 10 };

  it('error-rate %: A / B * 100', () => {
    expect(evalExpr('A / B * 100', { A: 6, B: 200 })).toBeCloseTo(3, 6);
  });

  it('honours * / before + - (precedence)', () => {
    expect(evalExpr('A + B * C', V)).toBe(6 + 200 * 10);
    expect(evalExpr('A * B + C', V)).toBe(6 * 200 + 10);
  });

  it('respects parentheses', () => {
    expect(evalExpr('(A + C) * 2', V)).toBe((6 + 10) * 2);
    expect(evalExpr('B / (A + C)', { A: 6, B: 200, C: 14 })).toBe(10);
  });

  it('unary minus', () => {
    expect(evalExpr('-A', V)).toBe(-6);
    expect(evalExpr('C - -A', V)).toBe(16);
  });

  it('divide-by-zero → null (rendered as a gap, not NaN/Infinity)', () => {
    expect(evalExpr('A / 0', V)).toBeNull();
    expect(evalExpr('A / D', { ...V, D: 0 })).toBeNull();
  });

  it('unknown id → null', () => {
    expect(evalExpr('A / Z', V)).toBeNull();
  });

  it('trailing garbage / malformed → null (no silent partial eval)', () => {
    expect(evalExpr('A B', V)).toBeNull();
    expect(evalExpr('A +', V)).toBeNull();
    expect(evalExpr('(A + B', V)).toBeNull();
    expect(evalExpr('', V)).toBeNull();
  });

  it('decimals + whitespace tolerance', () => {
    expect(evalExpr('  A  *  1.5  ', V)).toBeCloseTo(9, 6);
  });
});

describe('exprRefs', () => {
  it('extracts distinct query ids', () => {
    expect(exprRefs('A / B * 100').sort()).toEqual(['A', 'B']);
    expect(exprRefs('(A + A) / B').sort()).toEqual(['A', 'B']);
    expect(exprRefs('100').sort()).toEqual([]);
  });
});
