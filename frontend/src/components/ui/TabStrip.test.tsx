// @vitest-environment jsdom
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { TabStrip } from './TabStrip';

// v0.10.456 (dış skill denetimi D5) — sekme şeridi atomu: tablist/tab ARIA,
// roving tabindex, ok tuşları odağı taşır ve etkinleştirir, devre dışı
// sekme atlanır; ham `.tab-strip` yalnız dilim-2 kalıntılarında.
let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => { act(() => root?.unmount()); root = null; host?.remove(); host = null; });

function mount(onChange: (k: string) => void, value = 'a') {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => {
    root!.render(<TabStrip ariaLabel="t" value={value} onChange={onChange}
      tabs={[{ key: 'a', label: 'A' }, { key: 'b', label: 'B', disabled: true }, { key: 'c', label: 'C' }]} />);
  });
  return host;
}
const key = (el: Element, k: string) => act(() => { el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })); });

describe('TabStrip', () => {
  it('renders tablist semantics and activates by click and arrow keys', () => {
    const onChange = vi.fn();
    const h = mount(onChange);
    const list = h.querySelector('[role="tablist"]')!;
    expect(list.getAttribute('aria-label')).toBe('t');
    const tabs = Array.from(h.querySelectorAll<HTMLButtonElement>('[role="tab"]'));
    expect(tabs).toHaveLength(3);
    expect(tabs[0].getAttribute('aria-selected')).toBe('true');
    expect(tabs[0].tabIndex).toBe(0);
    expect(tabs[2].tabIndex).toBe(-1);
    expect(tabs[1].disabled).toBe(true);
    act(() => { tabs[2].click(); });
    expect(onChange).toHaveBeenLastCalledWith('c');
    key(tabs[0], 'ArrowRight'); // b devre dışı → c
    expect(onChange).toHaveBeenLastCalledWith('c');
    key(tabs[0], 'End');
    expect(onChange).toHaveBeenLastCalledWith('c');
    key(tabs[0], 'ArrowLeft'); // sarar → c
    expect(onChange).toHaveBeenLastCalledWith('c');
    expect(onChange).toHaveBeenCalledTimes(4);
  });
  it('raw .tab-strip markup remains only in the slice-2 leftovers', () => {
    const src = resolve(__dirname, '../..');
    const left: string[] = [];
    const walk = (d: string) => {
      for (const e of readdirSync(d)) {
        const p = join(d, e);
        if (statSync(p).isDirectory()) walk(p);
        else if (/\.tsx$/.test(e) && !/\.test\.tsx$/.test(e) && !p.endsWith('ui/TabStrip.tsx') && /className=(?:"tab-strip|\{`tab-strip)/.test(readFileSync(p, 'utf8'))) left.push(p.slice(src.length + 1));
      }
    };
    walk(src);
    expect(left.sort()).toEqual(['pages/Clusters.tsx', 'pages/Trace.tsx']);
  });
});
