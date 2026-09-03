// @vitest-environment jsdom
// CompareToggle.contract.test.tsx — v0.10.315: dört mod, aria-pressed
// yalnız seçili olanda, tık → onChange(mod).
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { CompareToggle } from './CompareToggle';

let host: HTMLDivElement | null = null; let root: Root | null = null;
afterEach(() => { act(() => { root?.unmount(); }); host?.remove(); host = null; root = null; });

describe('CompareToggle', () => {
  it('dört düğme, seçili basılı, tık modu verir', () => {
    const onChange = vi.fn();
    host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host);
    act(() => { root!.render(<CompareToggle value="24h" onChange={onChange} />); });
    const btns = Array.from(host.querySelectorAll('button'));
    expect(btns.map(b => b.textContent)).toEqual(['off', '24h', '7d', 'prev window']);
    expect(btns.map(b => b.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'false', 'false']);
    act(() => { btns[3].click(); });
    expect(onChange).toHaveBeenCalledWith('prev');
    act(() => { btns[0].click(); });
    expect(onChange).toHaveBeenCalledWith('off');
  });
});
