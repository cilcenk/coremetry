import { describe, expect, it } from 'vitest';
import { moveColumn, reconcileColOrder } from './tableColumns';

// v0.7.47 — Traces column reorder + resize. These pin the pure order math that
// the drag-and-drop + persistence rely on (CLAUDE.md #11).
const FIXED = ['time', 'service', 'operation', 'duration', 'spans', 'status'];

describe('reconcileColOrder', () => {
  it('first run / empty order → fixed columns in default order', () => {
    expect(reconcileColOrder([], FIXED, [])).toEqual(FIXED);
  });
  it('appends newly-added attribute columns, keeps existing arrangement', () => {
    const order = ['service', 'time', 'duration', 'spans', 'operation', 'status'];
    expect(reconcileColOrder(order, FIXED, ['http.method'])).toEqual([...order, 'http.method']);
  });
  it('drops removed attribute columns', () => {
    const order = ['time', 'http.method', 'service', 'operation', 'duration', 'spans', 'status'];
    expect(reconcileColOrder(order, FIXED, [])).toEqual(['time', 'service', 'operation', 'duration', 'spans', 'status']);
  });
  it('appends a fixed column missing from a stale persisted order (new build)', () => {
    const stale = ['time', 'service', 'operation', 'duration']; // pre-"spans"/"status"
    expect(reconcileColOrder(stale, FIXED, [])).toEqual(['time', 'service', 'operation', 'duration', 'spans', 'status']);
  });
  it('preserves a custom arrangement with extras interleaved', () => {
    const order = ['service', 'http.method', 'time', 'operation', 'duration', 'spans', 'status'];
    expect(reconcileColOrder(order, FIXED, ['http.method'])).toEqual(order);
  });
});

describe('moveColumn', () => {
  it('moves a column to immediately before the target', () => {
    expect(moveColumn(FIXED, 'duration', 'time')).toEqual(['duration', 'time', 'service', 'operation', 'spans', 'status']);
  });
  it('moving onto itself is a no-op (returns same ref)', () => {
    expect(moveColumn(FIXED, 'time', 'time')).toBe(FIXED);
  });
  it('moving a later column before an earlier one', () => {
    expect(moveColumn(FIXED, 'status', 'service')).toEqual(['time', 'status', 'service', 'operation', 'duration', 'spans']);
  });
  it('appends when the target is absent', () => {
    expect(moveColumn(FIXED, 'time', 'nope')).toEqual(['service', 'operation', 'duration', 'spans', 'status', 'time']);
  });
});
