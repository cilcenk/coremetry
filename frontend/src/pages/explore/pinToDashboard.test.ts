// pinToDashboard.test.ts — v0.8.419 Data-Explorer parity DE4. Pins the
// BuilderQuery → dashboard Panel mapping so a builder edit can't silently
// change what a pinned panel renders.
import { describe, expect, it } from 'vitest';
import { isPinnable, queryToPanel, vizFromPanel, vizToPanel } from './pinToDashboard';
import { blankQuery, EXPLORE_VIZ, type ExploreViz } from './model';
import type { MetricPanelConfig, PanelVizType, SpanMetricPanelConfig } from '@/lib/types';

describe('queryToPanel — metric source', () => {
  const q = {
    ...blankQuery('A', 'metric'),
    metric: 'jvm.memory.used', unit: 'MB', agg: 'p95',
    scope: 'checkout', splitBy: ['host.name'],
    filters: [{ k: 'deployment.environment', op: '=' as const, v: ['prod'] }],
  };

  it('maps to a metric panel config verbatim', () => {
    const p = queryToPanel(q, { step: 60 })!;
    expect(p.type).toBe('metric');
    expect(p.width).toBe(2);
    const cfg = p.config as MetricPanelConfig;
    expect(cfg.metricName).toBe('jvm.memory.used');
    expect(cfg.service).toBe('checkout');
    expect(cfg.agg).toBe('p95');
    expect(cfg.groupBy).toBe('host.name');
    expect(cfg.step).toBe(60);
    expect(JSON.parse(cfg.filters!)).toEqual(q.filters);
  });

  it('auto step (0) stays absent so GRAN-C width-aware auto applies', () => {
    const cfg = queryToPanel(q, { step: 0 })!.config as MetricPanelConfig;
    expect(cfg.step).toBeUndefined();
  });

  it('no metric picked yet → not pinnable', () => {
    expect(queryToPanel({ ...q, metric: '' })).toBeNull();
    expect(isPinnable({ ...q, metric: '' })).toBe(false);
  });
});

describe('queryToPanel — span source', () => {
  const q = {
    ...blankQuery('B'),
    agg: 'p99', scope: 'payments', splitBy: ['name'],
    dsl: 'http.status_code >= 500',
    filters: [{ k: 'http.method', op: '=' as const, v: ['POST'] }],
  };

  it('folds the scope into a leading service.name filter', () => {
    const cfg = queryToPanel(q)!.config as SpanMetricPanelConfig;
    expect(JSON.parse(cfg.filters!)).toEqual([
      { k: 'service.name', op: '=', v: ['payments'] },
      { k: 'http.method', op: '=', v: ['POST'] },
    ]);
    expect(cfg.agg).toBe('p99');
    expect(cfg.groupBy).toBe('name');
    expect(cfg.dsl).toBe('http.status_code >= 500');
    // default field (duration_ms) is omitted, not repeated
    expect(cfg.field).toBeUndefined();
  });

  it('spanmetric panel type with default title from queryDesc', () => {
    const p = queryToPanel(q)!;
    expect(p.type).toBe('spanmetric');
    expect(p.title.length).toBeGreaterThan(0);
    expect(queryToPanel(q, { title: 'My tile' })!.title).toBe('My tile');
  });

  it('genuine OR filter group refuses the pin (no silent flatten)', () => {
    const grouped = {
      ...q,
      filterGroup: { join: 'OR' as const, filters: q.filters },
    };
    expect(isPinnable(grouped)).toBe(false);
    expect(queryToPanel(grouped)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// v0.9.786 — viz taşıma. Birim-karıştırma kuralının (her Nh/Nd, her ms/s
// dalı ayrı test) viz hali: TEK bir viz değerini denemek yalan söyler,
// çünkü hata tam da eşleşmeyen adlarda ('bars' vs 'bar') ve zaman-serisi
// OLMAYAN markların düşüşünde saklı. Her ExploreViz değeri satır alır.
// ---------------------------------------------------------------------------
describe('vizToPanel — ExploreViz → PanelVizType', () => {
  const rows: [ExploreViz, PanelVizType | undefined][] = [
    ['line', undefined],          // alan yokluğu = panel varsayılanı
    ['bars', 'bar'],              // yazım kayması — asıl hata buradaydı
    ['area', 'area'],
    ['stacked', 'stacked-area'],  // Explore'un stacked'i ALAN yığını
    ['stat', undefined],          // zaman-serisi markı değil
    ['toplist', undefined],
    ['pie', undefined],
    ['table', undefined],
    ['heatmap', undefined],
  ];
  for (const [from, to] of rows) {
    it(`${from} → ${to ?? '(taşınmaz)'}`, () => {
      expect(vizToPanel(from)).toBe(to);
    });
  }

  it('tüm union değerleri kapsanır — yeni bir viz sessizce düşmesin', () => {
    expect(rows.map(r => r[0]).sort()).toEqual([...EXPLORE_VIZ].sort());
  });

  it('undefined → taşınmaz', () => {
    expect(vizToPanel(undefined)).toBeUndefined();
  });
});

describe('vizFromPanel — PanelVizType → ExploreViz', () => {
  const rows: [PanelVizType | undefined, ExploreViz][] = [
    ['line', 'line'],
    ['bar', 'bars'],
    ['area', 'area'],
    ['stacked-area', 'stacked'],
    // Explore ikizi yok: yığma semantiktir, mark değil — yığma korunur.
    ['stacked-bar', 'stacked'],
    [undefined, 'line'],
  ];
  for (const [from, to] of rows) {
    it(`${from ?? 'undefined'} → ${to}`, () => {
      expect(vizFromPanel(from)).toBe(to);
    });
  }

  it('zaman-serisi markları GİDİP GERİ aynı kalır', () => {
    for (const v of ['line', 'bars', 'area', 'stacked'] as ExploreViz[]) {
      expect(vizFromPanel(vizToPanel(v))).toBe(v);
    }
  });

  it('zaman-serisi olmayan markların dönüşü line', () => {
    for (const v of ['stat', 'toplist', 'pie', 'table', 'heatmap'] as ExploreViz[]) {
      expect(vizFromPanel(vizToPanel(v))).toBe('line');
    }
  });
});

describe('queryToPanel — viz config e iner', () => {
  const span = {
    ...blankQuery('A', 'span'),
    agg: 'p99', scope: 'payments', splitBy: ['name'],
  };

  it('bars pinlenince panel bar olur (v0.9.786 hatası)', () => {
    const cfg = queryToPanel(span, { viz: 'bars' })!.config as SpanMetricPanelConfig;
    expect(cfg.viz).toBe('bar');
  });

  it('line/verilmemiş viz alanı YAZMAZ — eski config şekli korunur', () => {
    expect((queryToPanel(span, { viz: 'line' })!.config as SpanMetricPanelConfig).viz)
      .toBeUndefined();
    expect((queryToPanel(span)!.config as SpanMetricPanelConfig).viz).toBeUndefined();
  });

  it('stacked → stacked-area, area → area', () => {
    expect((queryToPanel(span, { viz: 'stacked' })!.config as SpanMetricPanelConfig).viz)
      .toBe('stacked-area');
    expect((queryToPanel(span, { viz: 'area' })!.config as SpanMetricPanelConfig).viz)
      .toBe('area');
  });

  it('metric paneli viz TAŞIMAZ — MetricPanelConfig te alan yok', () => {
    const p = queryToPanel(
      { ...blankQuery('A', 'metric'), metric: 'jvm.memory.used' }, { viz: 'bars' })!;
    expect(p.type).toBe('metric');
    expect((p.config as Record<string, unknown>).viz).toBeUndefined();
  });
});
