// @vitest-environment jsdom
// ServiceMap.unified.test.tsx — v0.9.1252 regresyon pini.
//
// Operator-reported: "topology gösterimi service üzerinden gidince
// farklı, doğrudan topology üzerinden gidince farklı — service
// üzerinden gösterilen doğru hali." Kök neden: /service-map focus'u
// kendi harita-süzme yoluyla çiziyordu (hops'suz, harita görseli);
// servis sekmesi FocusedNeighborhood (MV komşuluğu, 2-hop akış).
// Sözleşme: focus seçiliyken /service-map SERVİS SEKMESİYLE AYNI
// bileşeni çizer; focus yokken global örneklem haritası.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

vi.mock('@/components/topology/FocusedNeighborhood', () => ({
  FocusedNeighborhood: (props: { focus: string; hops: number; errorsOnly: boolean }) => (
    <div data-testid="focused-neighborhood"
      data-focus={props.focus} data-hops={props.hops}
      data-eonly={String(props.errorsOnly)} />
  ),
}));
vi.mock('@/components/TopologyFlowGraph', () => ({
  TopologyFlowGraph: () => <div data-testid="flow-graph-map" />,
}));
// Test-başına değiştirilebilir harita verisi: auto-pick (v0.8.265)
// GERÇEK servis düğümü görünce odak seçer — global-harita sözleşmesi
// ancak seçilecek gerçek düğüm yokken (sentetik db düğümü) gözlenir.
let mapNodes: Array<Record<string, unknown>> = [];
vi.mock('@/lib/queries', async (importOriginal) => {
  const mod = await importOriginal<Record<string, unknown>>();
  return {
    ...mod,
    useServiceMap: () => ({
      data: { nodes: mapNodes, edges: [], sampledFrom: 1, totalSpans: 1 },
    }),
  };
});

import ServiceMapPage from './ServiceMap';

let host: HTMLElement | null = null;
let root: Root | null = null;
function mount(url: string): HTMLElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  act(() => {
    root = createRoot(host!);
    root.render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={[url]}>
          <ServiceMapPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  return host!;
}
const byTestId = (el: HTMLElement, id: string) =>
  el.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;

beforeEach(() => {
  mapNodes = [{ service: 'svc-a', spanCount: 1, errorRate: 0, avgMs: 1, p95Ms: 1 }];
  try { localStorage.clear(); } catch { /* Node22 shadow (CorePanel.smoke dersi) */ }
});
afterEach(() => {
  if (root) act(() => root!.unmount());
  host?.remove(); host = null; root = null;
});

describe('ServiceMap focus = servis sekmesi görünümü (v0.9.1252)', () => {
  it('?focus= varken FocusedNeighborhood çizer, harita çizmez', () => {
    const el = mount('/service-map?focus=bsa-pay');
    const fn = byTestId(el, 'focused-neighborhood');
    expect(fn?.dataset.focus).toBe('bsa-pay');
    expect(byTestId(el, 'flow-graph-map')).toBeNull();
  });

  it('hops/eonly kodekleri servis sekmesiyle aynı paramlardan okunur', () => {
    const el = mount('/service-map?focus=bsa-pay&hops=3&eonly=1');
    const fn = byTestId(el, 'focused-neighborhood');
    expect(fn?.dataset.hops).toBe('3');
    expect(fn?.dataset.eonly).toBe('true');
  });

  it('çıplak açılış auto-pick ile AYNI odak görünümüne çıkar (tutarlılık)', () => {
    // v0.8.265 auto-pick: en yoğun gerçek servis odaklanır. Fix öncesi bu
    // odak harita-süzme görünümüne düşüyordu; artık servis sekmesiyle aynı.
    const el = mount('/service-map');
    const fn = byTestId(el, 'focused-neighborhood');
    expect(fn?.dataset.focus).toBe('svc-a');
    expect(byTestId(el, 'flow-graph-map')).toBeNull();
  });

  it('odaklanacak gerçek servis yokken global harita çizilir', () => {
    // Sentetik (db) düğüm auto-pick'e girmez → focus boş kalır → örneklem
    // haritası. FocusedNeighborhood bu dalda ASLA çizilmez.
    mapNodes = [{ service: 'ext-postgres', kind: 'db', spanCount: 1, errorRate: 0, avgMs: 1, p95Ms: 1 }];
    const el = mount('/service-map');
    expect(byTestId(el, 'flow-graph-map')).toBeTruthy();
    expect(byTestId(el, 'focused-neighborhood')).toBeNull();
  });
});
