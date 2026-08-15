import { describe, it, expect } from 'vitest';
import { operatorEventsToRegions, EVENT_KIND_COLOUR, EVENT_DEFAULT_COLOUR } from './eventRegions';

// v0.9.1044 — Ş3 kapanışı: Endpoints karoları DOM-overlay EventMarkers
// yerine chart-içi bölge alır. Bu tablo iki sözleşmeyi mühürler:
// (1) ns→sec + sıfır-genişlik (fromSec === toSec = dikey işaret),
// (2) kind→renk eşlemesi ve bilinmeyen kind'ın gri düşmesi.

describe('operatorEventsToRegions', () => {
  const t = 1786773600_000_000_000; // unix ns

  it('ns→sec, sıfır-genişlik bölge', () => {
    const r = operatorEventsToRegions([{ time: t, kind: 'deploy', label: 'v1.2.3' }]);
    expect(r).toHaveLength(1);
    expect(r[0].fromSec).toBe(1786773600);
    expect(r[0].toSec).toBe(1786773600);
  });

  it('bilinen kind kendi rengini alır, etiket "kind · label"', () => {
    const r = operatorEventsToRegions([{ time: t, kind: 'incident', label: 'db down' }]);
    expect(r[0].color).toBe(EVENT_KIND_COLOUR.incident);
    expect(r[0].label).toBe('incident · db down');
  });

  it('bilinmeyen kind gri düşer; boş label yalnız kind', () => {
    const r = operatorEventsToRegions([{ time: t, kind: 'custom', label: '' }]);
    expect(r[0].color).toBe(EVENT_DEFAULT_COLOUR);
    expect(r[0].label).toBe('custom');
  });

  it('boş/null giriş boş dizi (bölge katmanı hiç kurulmaz)', () => {
    expect(operatorEventsToRegions([])).toEqual([]);
    expect(operatorEventsToRegions(null)).toEqual([]);
    expect(operatorEventsToRegions(undefined)).toEqual([]);
  });
});
