import { describe, it, expect } from 'vitest';
import { SERIES_PALETTES, SERIES_SLOTS, assignSeriesSlots, preferredSlot, seriesColorsFor } from './chartFmt';

// v0.10.510 (dış skill denetimi D7, mockup onaylı) — sekiz yuvalı, tema
// başına adımlanmış palet; durum renkleri paletin dışında; panel içi yuva
// ataması sekize kadar çakışmasız, ad tercihli, sabitlemeli.
const STATUS = {
  dark: ['#3fb950', '#d29922', '#ff5252'], light: ['#1a7f37', '#9a6700', '#cf222e'], redhat: ['#3e8635', '#f0ab00', '#c9190b'],
} as const;

describe('SERIES_PALETTES', () => {
  it('her temada 8 tekil renk, durum tokenlarıyla çakışmaz', () => {
    for (const [theme, pal] of Object.entries(SERIES_PALETTES)) {
      expect(pal).toHaveLength(SERIES_SLOTS);
      expect(new Set(pal.map(c => c.toLowerCase())).size).toBe(SERIES_SLOTS);
      for (const st of STATUS[theme as keyof typeof STATUS]) expect(pal.map(c => c.toLowerCase())).not.toContain(st);
    }
  });
  it('yuva 1 temanın vurgusu', () => {
    expect(SERIES_PALETTES.dark[0]).toBe('#388bfd');
    expect(SERIES_PALETTES.light[0]).toBe('#0969da');
    expect(SERIES_PALETTES.redhat[0]).toBe('#0066cc');
  });
});

describe('assignSeriesSlots', () => {
  const labels = ['api-gateway', 'payments', 'cart', 'checkout', 'inventory', 'search', 'auth', 'notifications'];
  it('sekiz seri, sekiz farklı yuva', () => {
    const m = assignSeriesSlots(labels);
    expect(new Set(m.values()).size).toBe(8);
    for (const l of labels) expect(m.get(l)).toBeGreaterThanOrEqual(0);
  });
  it('tercih edilen yuva boşsa aynen alınır', () => {
    const m = assignSeriesSlots(['payments']);
    expect(m.get('payments')).toBe(preferredSlot('payments'));
  });
  it('sabitleme: süzgeç seri sayısını değiştirince kalanlar yerinde kalır', () => {
    const first = assignSeriesSlots(labels);
    const fewer = labels.filter((_, i) => i % 2 === 0);
    const second = assignSeriesSlots(fewer, first);
    for (const l of fewer) expect(second.get(l)).toBe(first.get(l));
  });
  it('dokuzuncu seri dolaşır (çakışma kaçınılmaz, çağıran katlar)', () => {
    const m = assignSeriesSlots([...labels, 'ninth']);
    expect(m.size).toBe(9);
    expect(m.get('ninth')).toBe(preferredSlot('ninth'));
  });
  it('seriesColorsFor temaya göre renk verir', () => {
    const dark = seriesColorsFor(['a', 'b'], undefined, 'dark');
    const light = seriesColorsFor(['a', 'b'], undefined, 'light');
    expect(dark.get('a')).not.toBe(light.get('a'));
    expect(SERIES_PALETTES.dark).toContain(dark.get('a'));
    expect(SERIES_PALETTES.light).toContain(light.get('a'));
  });
});
