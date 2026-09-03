// @vitest-environment jsdom
// useCompareParam.test.tsx — v0.10.315: URL kaynak (prior→prev, 24h),
// seçim URL'ye yazılır (prev→prior, off siler, yabancı anahtar korunur),
// seedKey yalnız URL boşken tohumlar ve URL'ye yazar.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { useCompareParam } from './useCompareParam';
import type { CompareMode } from './compareParam';

// Bu vitest ortamında localStorage işlevsiz (setItem yok) → storage
// sarmalayıcısı bellek haritasıyla mock'lanır; hook yalnız getRaw/setRaw görür.
const { mem } = vi.hoisted(() => ({ mem: new Map<string, string>() }));
vi.mock('@/lib/storage', () => ({
  getRaw: (k: string) => mem.get(k) ?? null,
  setRaw: (k: string, v: string) => { mem.set(k, v); },
}));

let host: HTMLDivElement | null = null; let root: Root | null = null;
let last: { compare: CompareMode; set: (m: CompareMode) => void; search: string } | null = null;

function Probe({ seedKey }: { seedKey?: string }) {
  const [compare, set] = useCompareParam(seedKey ? { seedKey } : {});
  const loc = useLocation();
  last = { compare, set, search: loc.search };
  return null;
}
function mount(url: string, seedKey?: string) {
  host = document.createElement('div'); document.body.appendChild(host); root = createRoot(host);
  act(() => { root!.render(<MemoryRouter initialEntries={[url]}><Probe seedKey={seedKey} /></MemoryRouter>); });
}
afterEach(() => { act(() => { root?.unmount(); }); host?.remove(); host = null; root = null; last = null; mem.clear(); });

describe('useCompareParam', () => {
  it('URL prior → prev; 24h → 24h; yok → off', () => {
    mount('/pod?compare=prior'); expect(last!.compare).toBe('prev');
    act(() => { root!.unmount(); }); mount('/pod?compare=24h'); expect(last!.compare).toBe('24h');
    act(() => { root!.unmount(); }); mount('/pod?range=1h'); expect(last!.compare).toBe('off');
  });
  it('set → URL: prev=prior, off siler, yabancı anahtar korunur', () => {
    mount('/pod?range=1h&pod=x');
    act(() => { last!.set('prev'); });
    expect(new URLSearchParams(last!.search).get('compare')).toBe('prior');
    expect(new URLSearchParams(last!.search).get('pod')).toBe('x');
    expect(last!.compare).toBe('prev');
    act(() => { last!.set('off'); });
    expect(new URLSearchParams(last!.search).has('compare')).toBe(false);
    expect(last!.compare).toBe('off');
  });
  it('seedKey: URL boşken tohum URL\'ye yazılır; URL doluysa tohum ezmez', () => {
    mem.set('t.compare', '7d');
    mount('/service?svc=a', 't.compare');
    expect(new URLSearchParams(last!.search).get('compare')).toBe('7d');
    expect(last!.compare).toBe('7d');
    act(() => { root!.unmount(); });
    mount('/service?compare=24h', 't.compare');
    expect(last!.compare).toBe('24h');
    act(() => { last!.set('prev'); });
    expect(mem.get('t.compare')).toBe('prev');
  });
});
