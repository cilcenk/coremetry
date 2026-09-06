import { describe, it, expect } from 'vitest';
import { rowActivation, rowKeyboard } from './a11y';

// v0.10.394 — satır klavye eşdeğeri (dış skill denetimi D3).
describe('rowActivation', () => {
  const mk = (key: string, self = true) => {
    const target = {};
    return { key, target: self ? target : {}, currentTarget: target, preventDefault: () => { prevented = true } } as never;
  };
  let prevented = false;
  it('role=button + tabIndex=0 beyan eder', () => {
    const p = rowActivation(() => {});
    expect(p.role).toBe('button');
    expect(p.tabIndex).toBe(0);
  });
  it('Enter ve Space satırı etkinleştirir, varsayılanı keser', () => {
    let n = 0;
    const p = rowActivation(() => { n++; });
    prevented = false; p.onKeyDown(mk('Enter')); expect(n).toBe(1); expect(prevented).toBe(true);
    prevented = false; p.onKeyDown(mk(' '));     expect(n).toBe(2); expect(prevented).toBe(true);
    prevented = false; p.onKeyDown(mk('a'));     expect(n).toBe(2); expect(prevented).toBe(false);
  });
  it('hücre içindeki bir öğeden gelen Enter satırı ETKİNLEŞTİRMEZ', () => {
    let n = 0;
    const p = rowActivation(() => { n++; });
    p.onKeyDown(mk('Enter', false));
    expect(n).toBe(0);
  });
});

// v0.10.455 — rowKeyboard: yalnız klavye yarısı (onClick yok), Enter/Space etkinleştirir.
describe('rowKeyboard', () => {
  it('carries role/tabIndex/onKeyDown but no onClick', () => {
    let n = 0;
    const p = rowKeyboard(() => { n++; }) as Record<string, unknown>;
    expect(p.role).toBe('button');
    expect(p.tabIndex).toBe(0);
    expect(p.onClick).toBeUndefined();
    const target = {};
    (p.onKeyDown as (e: unknown) => void)({ key: 'Enter', target, currentTarget: target, preventDefault() {} });
    (p.onKeyDown as (e: unknown) => void)({ key: 'x', target, currentTarget: target, preventDefault() {} });
    expect(n).toBe(1);
  });
});

