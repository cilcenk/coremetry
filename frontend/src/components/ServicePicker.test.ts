import { describe, expect, it } from 'vitest';
import { shouldAutoCommit } from './ServicePicker';

// v0.7.27 — Operator-reported: in service-topology "Focus on", typing the FIRST
// letter of a service immediately loaded it. Root cause was the picker's
// datalist-pick heuristic auto-firing onEnter on the first keystroke from an
// empty field. shouldAutoCommit now only treats a >1-char growth (a datalist
// pick or paste of a full option value) as a commit — incremental typing never
// grows by more than one char at a time. Regression test per CLAUDE.md #11.

describe('shouldAutoCommit', () => {
  it('does NOT commit on the first keystroke from empty (the reported bug)', () => {
    // "b" is the first char of "bsa-config-server"; even if "b" were a known
    // 1-char option, a single-char change must not auto-commit.
    expect(shouldAutoCommit('', 'b', true)).toBe(false);
  });

  it('does NOT commit while typing one char at a time through a prefix', () => {
    // orders → orders-api: each step grows by one char, never a pick.
    expect(shouldAutoCommit('orders', 'orders-', true)).toBe(false);
    expect(shouldAutoCommit('order', 'orders', true)).toBe(false);
  });

  it('commits when a datalist pick replaces the field with a full option (>1 char jump)', () => {
    expect(shouldAutoCommit('', 'payment-api', true)).toBe(true);
    expect(shouldAutoCommit('pay', 'payment-api', true)).toBe(true);
  });

  it('never commits when the value is not a known option, however it changed', () => {
    expect(shouldAutoCommit('', 'payment-api', false)).toBe(false);
    expect(shouldAutoCommit('pay', 'payment-xyz', false)).toBe(false);
  });

  it('does NOT commit on deletion / backspacing to a shorter exact match', () => {
    // User editing down to a prefix that happens to be a known option — not a pick.
    expect(shouldAutoCommit('orders-api', 'orders', true)).toBe(false);
  });

  it('requires strictly more than one char of growth', () => {
    // Exactly +1 char is typing, not a pick.
    expect(shouldAutoCommit('order', 'orders', true)).toBe(false);
    // +2 chars is a jump (e.g. an autocomplete fill / paste).
    expect(shouldAutoCommit('order', 'orders!', true)).toBe(true);
  });
});
