// legendVisibility — Grafana-parite #2. Lejant görünürlük çekirdeğinin
// kontratı: toggle/isolate/reset + "hepsi gizliyse hepsini geri getir" kuralı.
// StatsLegend (OVC/TC), TimeSeriesPanel lejantı ve MultiLineChart
// uPlot-lejantı bu semantiğe delege eder — panel-başına kopya yok.

import { describe, it, expect } from 'vitest';
import {
  toggleSeriesVisibility,
  isolateSeriesVisibility,
  resetSeriesVisibility,
  encodeHiddenLabels,
  decodeHiddenLabels,
  visibilityFor,
  defaultLatencyHidden,
  loadLegendVisibility,
  saveLegendVisibility,
  LEGEND_VIS_KEY_PREFIX,
  type StorageLike,
} from './legendVisibility';

describe('toggleSeriesVisibility', () => {
  it('hides a visible series', () => {
    expect(toggleSeriesVisibility([true, true, true], 1)).toEqual([true, false, true]);
  });

  it('re-shows a hidden series', () => {
    expect(toggleSeriesVisibility([true, false, true], 1)).toEqual([true, true, true]);
  });

  it('hiding the LAST visible series restores all (never an empty chart)', () => {
    expect(toggleSeriesVisibility([false, true, false], 1)).toEqual([true, true, true]);
  });

  it('single-series chart: toggling it off restores it (same rule)', () => {
    expect(toggleSeriesVisibility([true], 0)).toEqual([true]);
  });

  it('does not mutate the input', () => {
    const vis = [true, true];
    toggleSeriesVisibility(vis, 0);
    expect(vis).toEqual([true, true]);
  });

  it.each([-1, 3])('out-of-range index %i is a no-op copy', (i) => {
    expect(toggleSeriesVisibility([true, false, true], i)).toEqual([true, false, true]);
  });
});

describe('isolateSeriesVisibility', () => {
  it('isolates one series out of all-visible', () => {
    expect(isolateSeriesVisibility([true, true, true], 1)).toEqual([false, true, false]);
  });

  it('isolating the already-isolated series restores all (isolate-toggle)', () => {
    expect(isolateSeriesVisibility([false, true, false], 1)).toEqual([true, true, true]);
  });

  it('isolating a HIDDEN series shows only it (matches MLC/TSP legacy)', () => {
    expect(isolateSeriesVisibility([true, false, true], 1)).toEqual([false, true, false]);
  });

  it('re-isolating switches the isolated series', () => {
    expect(isolateSeriesVisibility([true, false, false], 2)).toEqual([false, false, true]);
  });

  it('does not mutate the input', () => {
    const vis = [true, true];
    isolateSeriesVisibility(vis, 0);
    expect(vis).toEqual([true, true]);
  });

  it.each([-1, 3])('out-of-range index %i is a no-op copy', (i) => {
    expect(isolateSeriesVisibility([true, false, true], i)).toEqual([true, false, true]);
  });
});

describe('resetSeriesVisibility', () => {
  it('returns n trues', () => {
    expect(resetSeriesVisibility(3)).toEqual([true, true, true]);
  });

  it('n=0 → empty; negative clamps to empty', () => {
    expect(resetSeriesVisibility(0)).toEqual([]);
    expect(resetSeriesVisibility(-2)).toEqual([]);
  });
});

// ── Persist katmanı (Grafana-parite madde 4 sweep) ──────────────────────────

describe('encodeHiddenLabels / decodeHiddenLabels', () => {
  it('round-trips hidden labels', () => {
    const raw = encodeHiddenLabels(['avg', 'p50', 'p99'], [true, true, false]);
    expect(decodeHiddenLabels(raw)).toEqual(['p99']);
  });

  it('all-visible encodes as an EXPLICIT empty list (user override survives)', () => {
    const raw = encodeHiddenLabels(['a', 'b'], [true, true]);
    expect(decodeHiddenLabels(raw)).toEqual([]);
  });

  it('dedupes duplicate labels (MLC compare-ghost twins)', () => {
    const raw = encodeHiddenLabels(['p99', 'p99'], [false, false]);
    expect(decodeHiddenLabels(raw)).toEqual(['p99']);
  });

  it('v0.9.206: a label with ANY visible index never encodes as hidden (isolate over compare twins)', () => {
    // compare mode: full row set = eff + ghost twins sharing raw labels;
    // plain-click isolate on current p95 hides everything else INCLUDING
    // p95's ghost twin — p95 must NOT land in storage.
    const labels = ['avg', 'p95', 'p99', 'avg', 'p95', 'p99'];
    const vis = [false, true, false, false, false, false];
    expect(decodeHiddenLabels(encodeHiddenLabels(labels, vis))).toEqual(['avg', 'p99']);
  });

  it('decode: null / garbage / non-string-array → null (no record)', () => {
    expect(decodeHiddenLabels(null)).toBeNull();
    expect(decodeHiddenLabels('not-json')).toBeNull();
    expect(decodeHiddenLabels('{"a":1}')).toBeNull();
    expect(decodeHiddenLabels('[1,2]')).toBeNull();
  });
});

describe('visibilityFor', () => {
  it('no stored + no default → all visible', () => {
    expect(visibilityFor(['a', 'b'], null)).toEqual([true, true]);
  });

  it('default applies when nothing is stored', () => {
    expect(visibilityFor(['avg', 'P50', 'P99'], null, ['P99'])).toEqual([true, true, false]);
  });

  it('stored user choice OVERRIDES the default — including explicit all-visible', () => {
    expect(visibilityFor(['avg', 'P99'], [], ['P99'])).toEqual([true, true]);
    expect(visibilityFor(['avg', 'P99'], ['avg'], ['P99'])).toEqual([false, true]);
  });

  it('stored labels absent from the series are ignored', () => {
    expect(visibilityFor(['a', 'b'], ['gone'])).toEqual([true, true]);
  });

  it('a selection that would hide EVERYTHING restores all (core rule)', () => {
    expect(visibilityFor(['a', 'b'], ['a', 'b'])).toEqual([true, true]);
  });
});

describe('defaultLatencyHidden', () => {
  it('hides p99 when sibling series exist (avg+p50+p95 stay on)', () => {
    expect(defaultLatencyHidden(['avg', 'P50', 'P95', 'P99'])).toEqual(['P99']);
    expect(defaultLatencyHidden(['avg', 'p50', 'p90', 'p95', 'p99'])).toEqual(['p99']);
  });

  it('keepP99 (threshold p99-bound panel) keeps p99 visible', () => {
    expect(defaultLatencyHidden(['avg', 'p50', 'p99'], { keepP99: true })).toEqual([]);
  });

  it('p99-only chart is never blanked by the default', () => {
    expect(defaultLatencyHidden(['p99'])).toEqual([]);
  });

  it('no p99 series → nothing hidden', () => {
    expect(defaultLatencyHidden(['avg', 'p50', 'p95'])).toEqual([]);
  });
});

describe('load/saveLegendVisibility (StorageLike)', () => {
  const mkStorage = (): StorageLike & { map: Map<string, string> } => {
    const map = new Map<string, string>();
    return { map, getItem: k => map.get(k) ?? null, setItem: (k, v) => { map.set(k, v); } };
  };

  it('save → load round-trips through the prefixed key', () => {
    const s = mkStorage();
    saveLegendVisibility('ov-response-time', ['avg', 'P99'], [true, false], s);
    expect(s.map.has(LEGEND_VIS_KEY_PREFIX + 'ov-response-time')).toBe(true);
    expect(loadLegendVisibility('ov-response-time', s)).toEqual(['P99']);
  });

  it('missing key / null storage → null', () => {
    expect(loadLegendVisibility('nope', mkStorage())).toBeNull();
    expect(loadLegendVisibility('nope', null)).toBeNull();
  });

  it('user re-showing everything persists as [] and beats the default via visibilityFor', () => {
    const s = mkStorage();
    saveLegendVisibility('k', ['avg', 'P99'], [true, true], s);
    const stored = loadLegendVisibility('k', s);
    expect(stored).toEqual([]);
    expect(visibilityFor(['avg', 'P99'], stored, ['P99'])).toEqual([true, true]);
  });
});

// ── v0.9.206 review-fix: compare modu kalıcı seçimi zehirlemesin ────────────
// MLC compare'de bundle.labels her ham etiketi iki kez taşır (current + ghost).
// Eski akış: isolate → HERHANGİ-index-gizli encode TÜM etiketleri (izole
// edileni dahil) kayda sokuyor → visibilityFor restore-all'a takılıp hepsi
// görünür dönüyor → non-null kayıt p99 default'unu kalıcı eziyordu. Fix iki
// katmanlı: MLC save/seed yalnız GERÇEK (eff) dilimiyle çalışır + encode
// her-index-gizli semantiğine geçti. Bu blok her iki katmanı da pinler.
describe('compare-mode isolate persistence (v0.9.206 review-fix)', () => {
  const mkStorage = (): StorageLike & { map: Map<string, string> } => {
    const map = new Map<string, string>();
    return { map, getItem: k => map.get(k) ?? null, setItem: (k, v) => { map.set(k, v); } };
  };

  it('compare ghost rows never enter storage (MLC saves only the real slice)', () => {
    const s = mkStorage();
    const realLabels = ['avg', 'p50', 'p95', 'p99'];
    const allLabels = [...realLabels, ...realLabels]; // + ghost twins
    // seed (no record): p99 default hidden, ghost mirrors its twin
    const realVis = visibilityFor(realLabels, loadLegendVisibility('svc-duration-band', s), ['p99']);
    const vis = allLabels.map((l, i) =>
      i < realLabels.length ? realVis[i] : (realVis[realLabels.indexOf(l)] ?? true));
    expect(vis).toEqual([true, true, true, false, true, true, true, false]);
    // plain click isolates current p95 (full-row index 2)
    const next = isolateSeriesVisibility(vis, 2);
    // MLC save path: ONLY the real slice reaches storage
    saveLegendVisibility('svc-duration-band', realLabels, next.slice(0, realLabels.length), s);
    expect(loadLegendVisibility('svc-duration-band', s)).toEqual(['avg', 'p50', 'p99']);
  });

  it('isolate in compare mode round-trips: isolation survives, default not clobbered', () => {
    const s = mkStorage();
    const realLabels = ['avg', 'p50', 'p95', 'p99'];
    const vis = visibilityFor(realLabels, null, ['p99']);
    const next = isolateSeriesVisibility(vis, 2); // isolate p95
    saveLegendVisibility('svc-duration-band', realLabels, next, s);
    const stored = loadLegendVisibility('svc-duration-band', s);
    // pre-fix this stored ALL labels → restore-all → all-visible incl. p99
    expect(stored).toEqual(['avg', 'p50', 'p99']);
    expect(visibilityFor(realLabels, stored, ['p99'])).toEqual([false, false, true, false]);
  });

  it('defense in depth: even a full twin-list save keeps the isolated label out of storage', () => {
    // if the FULL (twins included) list ever reaches encode again, the
    // isolated label still has a visible index and must stay unstored
    const s = mkStorage();
    const labels = ['p50', 'p95', 'p50', 'p95'];
    const next = isolateSeriesVisibility([true, true, true, true], 1); // [F,T,F,F]
    saveLegendVisibility('k', labels, next, s);
    expect(loadLegendVisibility('k', s)).toEqual(['p50']);
  });
});
