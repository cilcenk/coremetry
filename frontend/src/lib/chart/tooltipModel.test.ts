// tooltipModel — v0.9.101 (Grafana-parity Adım 1). The pure "all series"
// tooltip model: drop empty values, sort by value desc, format via fmtSmart.
// This table pins the contract so every panel's hover tooltip orders + formats
// identically (the whole reason it's shared, not per-panel).

import { describe, it, expect } from 'vitest';
import { sortedTooltipRows, capTooltipRows, type TooltipItem } from './tooltipModel';

describe('sortedTooltipRows — ordering', () => {
  it('sorts by value DESC by default (hottest series first)', () => {
    const items: TooltipItem[] = [
      { label: 'a', color: '#1', value: 10 },
      { label: 'b', color: '#2', value: 90 },
      { label: 'c', color: '#3', value: 50 },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['b', 'c', 'a']);
  });

  it('sort:"none" preserves caller order (e.g. p50/p95/p99 ladder)', () => {
    const items: TooltipItem[] = [
      { label: 'p50', color: '#1', value: 5 },
      { label: 'p95', color: '#2', value: 40 },
      { label: 'p99', color: '#3', value: 120 },
    ];
    expect(sortedTooltipRows(items, 'none').map(r => r.label)).toEqual(['p50', 'p95', 'p99']);
  });

  it('is stable for ties (equal values keep input order → no poll reshuffle)', () => {
    const items: TooltipItem[] = [
      { label: 'first', color: '#1', value: 7 },
      { label: 'second', color: '#2', value: 7 },
      { label: 'third', color: '#3', value: 7 },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['first', 'second', 'third']);
  });
});

describe('sortedTooltipRows — empties dropped', () => {
  it('drops null / undefined / NaN / Infinity values', () => {
    const items: TooltipItem[] = [
      { label: 'ok', color: '#1', value: 3 },
      { label: 'null', color: '#2', value: null },
      { label: 'undef', color: '#3', value: undefined },
      { label: 'nan', color: '#4', value: NaN },
      { label: 'inf', color: '#5', value: Infinity },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['ok']);
  });

  it('keeps a real zero (0 is a value, not a gap)', () => {
    const items: TooltipItem[] = [
      { label: 'zero', color: '#1', value: 0 },
      { label: 'pos', color: '#2', value: 5 },
    ];
    expect(sortedTooltipRows(items).map(r => r.label)).toEqual(['pos', 'zero']);
  });
});

describe('sortedTooltipRows — fmtSmart units', () => {
  it('formats each value through the shared unit-aware formatter', () => {
    const rows = sortedTooltipRows([
      { label: 'lat', color: '#1', value: 234, unit: 'ms' },
      { label: 'rate', color: '#2', value: 12500, unit: '' },
      { label: 'err', color: '#3', value: 3.4, unit: '%' },
    ]);
    const byLabel = Object.fromEntries(rows.map(r => [r.label, r.text]));
    expect(byLabel.lat).toBe('234ms');
    expect(byLabel.rate).toBe('12.5k');
    expect(byLabel.err).toBe('3.40%'); // fmtSmart: 2 decimals below 10%
  });

  it('handles a unit with a leading space (dual-axis presets pass " ms")', () => {
    const [row] = sortedTooltipRows([{ label: 'p99', color: '#1', value: 1500, unit: ' ms' }]);
    expect(row.text).toBe('1.5s'); // ms auto-promotes to s past 1000
  });

  it('carries the raw value through for bold-nearest callers', () => {
    const [row] = sortedTooltipRows([{ label: 'x', color: '#1', value: 42, unit: 'ms' }]);
    expect(row.value).toBe(42);
  });
});

// Review 8/8 #6 (madde-2) — lejanttan gizlenen seri tooltip'ten düşer. Dört
// preset de aynı deseni kullanır: gizli seride value:null geçilir ve model
// satırı atar (MLC visibleRef[i]===false → null; OVC/TC visRef; TSP
// !visibleRef[i]). Bu tablo, MLC'nin izole senaryosunu (11/12 gizli) pinler:
// tooltip yalnız görünür seriyi listeler, bold-nearest seçimi gizli (çizili
// olmayan) bir seriye asla düşemez — çünkü satır hiç üretilmez.
describe('sortedTooltipRows — legend-hidden series (value forced null) drop', () => {
  const visible = [false, true, false]; // MLC visibleRef mirror: only idx 1 on
  const raw = [
    { label: 'hidden-big', color: '#1', value: 1000 },
    { label: 'visible-small', color: '#2', value: 5 },
    { label: 'hidden-mid', color: '#3', value: 400 },
  ];

  it('hidden series (11/12 isolate case) never reach the tooltip rows', () => {
    const rows = sortedTooltipRows(raw.map((r, i) => ({
      ...r,
      value: visible[i] === false ? null : r.value, // the shared preset mapping
    })));
    expect(rows.map(r => r.label)).toEqual(['visible-small']);
  });

  it('with nothing hidden the mapping is a no-op (verbatim old behaviour)', () => {
    const rows = sortedTooltipRows(raw.map((r, i) => ({
      ...r,
      value: ([true, true, true] as boolean[])[i] === false ? null : r.value,
    })));
    expect(rows.map(r => r.label)).toEqual(['hidden-big', 'hidden-mid', 'visible-small']); // DESC
  });
});

// v0.9.710 — fmt override (CorePanel display-processor yolu).
//
// İlk bağlayış fmt'i SÖZLEŞMEYE EKLEMEDEN geçirmişti: TS excess-property
// denetimi map-callback çıkarımından geçmiyor, alan sessizce yutuluyordu
// ve display-processor biçimi hiç basılmıyordu. Bugünün yedinci
// "yazılmış-ama-bağlanmamış" vakası — bu test sınıfı çiviliyor.
describe('fmt override', () => {
  it('fmt verilirse fmtSmart atlanır', () => {
    const rows = sortedTooltipRows([
      { label: 'a', color: '#000', value: 1500, fmt: '1.50 s' },
    ]);
    expect(rows[0].text).toBe('1.50 s');
  });
  it('fmt yoksa fmtSmart yolu aynen', () => {
    const rows = sortedTooltipRows([
      { label: 'a', color: '#000', value: 1500, unit: 'ms' },
    ]);
    expect(rows[0].text).not.toBe('');
    expect(rows[0].text).toMatch(/s|ms/);
  });
  it('fmt olsa bile null değer yine düşer — gap 0 okumaz', () => {
    const rows = sortedTooltipRows([
      { label: 'a', color: '#000', value: null, fmt: 'HAYALET' },
    ]);
    expect(rows).toEqual([]);
  });
});

// v0.9.750 — tooltip üst-N sınırı: 65×3 serili ekranda tooltip grafiği
// örtüyordu; ilk max satır + "+N seri daha" özeti.
describe('capTooltipRows (v0.9.750)', () => {
  const mk = (n: number) => Array.from({ length: n }, (_, i) => ({
    label: `s${i}`, color: '#fff', text: `${i}`, value: i,
  }));
  it('taşmada ilk max + özet satırı', () => {
    const out = capTooltipRows(mk(20), 8);
    expect(out).toHaveLength(9);
    expect(out[8].label).toBe('+12 seri daha');
    expect(out[8].text).toBe('');
  });
  it('taşma yoksa aynen', () => {
    expect(capTooltipRows(mk(5), 8)).toHaveLength(5);
  });
  it('max<=0 sınırsız', () => {
    expect(capTooltipRows(mk(30), 0)).toHaveLength(30);
  });
});
