import { describe, it, expect } from 'vitest';
import { panelToBuilder, panelToExploreHref } from './panelToExplore';
import { queryToPanel } from './pinToDashboard';
import { decodeBuilder } from './urlCodec';
import { blankQuery } from './model';
import type { FilterExpr, Panel, PanelVizType } from '@/lib/types';

// v0.9.773 — panelToExplore is the inverse of pinToDashboard. The two
// properties worth pinning are the ones a hand-written converter gets wrong:
// (1) a pinned query re-opens as the SAME query, and (2) a panel shape the
// builder cannot express returns null rather than a lookalike.
const mk = (p: Partial<Panel> & Pick<Panel, 'type' | 'config'>): Panel => ({
  id: 'p1', title: 'T', width: 2, ...p,
} as Panel);

const hrefState = (href: string) => {
  const sp = new URLSearchParams(href.slice(href.indexOf('?') + 1));
  return decodeBuilder(sp.get('q'));
};

describe('panelToBuilder — metric panels', () => {
  it('round-trips a pinned metric query', () => {
    const q = {
      ...blankQuery('A', 'metric'),
      metric: 'http.server.duration',
      agg: 'p95',
      scope: 'checkout',
      splitBy: ['http.route', 'k8s.namespace.name'],
      filters: [{ k: 'deployment.environment', op: '=', v: ['prod'] }] as FilterExpr[],
    };
    const panel = queryToPanel(q, { step: 60 });
    expect(panel).not.toBeNull();
    const back = panelToBuilder(panel as Panel, undefined);
    expect(back).not.toBeNull();
    const rq = back!.queries[0];
    expect(rq.source).toBe('metric');
    expect(rq.metric).toBe('http.server.duration');
    expect(rq.agg).toBe('p95');
    expect(rq.scope).toBe('checkout');
    expect(rq.splitBy).toEqual(['http.route', 'k8s.namespace.name']);
    expect(rq.filters).toEqual(q.filters);
    expect(back!.step).toBe(60);
  });

  it('bare metric config falls back to avg / no scope / auto step', () => {
    const back = panelToBuilder(mk({ type: 'metric', config: { metricName: 'cpu.util' } }), undefined);
    expect(back!.queries[0].agg).toBe('avg');
    expect(back!.queries[0].scope).toBe('');
    expect(back!.queries[0].splitBy).toEqual([]);
    expect(back!.queries[0].filters).toEqual([]);
    expect(back!.step).toBe(0);
  });

  it('groupBy empty string yields an empty array, not a blank key', () => {
    const back = panelToBuilder(
      mk({ type: 'metric', config: { metricName: 'cpu.util', groupBy: '' } }), undefined);
    expect(back!.queries[0].splitBy).toEqual([]);
  });

  it('groupBy with blanks and spaces is trimmed', () => {
    const back = panelToBuilder(
      mk({ type: 'metric', config: { metricName: 'cpu.util', groupBy: ' a , ,b ' } }), undefined);
    expect(back!.queries[0].splitBy).toEqual(['a', 'b']);
  });

  it('PromQL-driven metric panel refuses (BuilderQuery has no promql slot)', () => {
    const back = panelToBuilder(mk({
      type: 'metric',
      config: { metricName: '', promql: 'rate(http_requests_total[5m])' },
    }), undefined);
    expect(back).toBeNull();
  });
});

describe('panelToBuilder — spanmetric panels', () => {
  it('round-trips a pinned span query and lifts the scope back out', () => {
    const q = {
      ...blankQuery('A', 'span'),
      agg: 'p99',
      metric: 'duration_ms',
      scope: 'payments',
      splitBy: ['name'],
      filters: [{ k: 'http.status_code', op: '=', v: ['500'] }] as FilterExpr[],
    };
    const panel = queryToPanel(q);
    // queryToPanel folds the scope INTO the flat filter list…
    const cfg = (panel as Panel).config as { filters?: string };
    expect(JSON.parse(cfg.filters ?? '[]')[0].k).toBe('service.name');
    // …and panelToBuilder lifts it back into the scope slot.
    const back = panelToBuilder(panel as Panel, undefined);
    const rq = back!.queries[0];
    expect(rq.source).toBe('span');
    expect(rq.scope).toBe('payments');
    expect(rq.filters).toEqual(q.filters);
    expect(rq.agg).toBe('p99');
    expect(rq.metric).toBe('duration_ms');
    expect(rq.splitBy).toEqual(['name']);
  });

  it('absent field defaults to duration_ms', () => {
    const back = panelToBuilder(mk({ type: 'spanmetric', config: { agg: 'count' } }), undefined);
    expect(back!.queries[0].metric).toBe('duration_ms');
    expect(back!.queries[0].agg).toBe('count');
    expect(back!.queries[0].dsl).toBe('');
  });

  it('non-duration measured field survives', () => {
    const back = panelToBuilder(
      mk({ type: 'spanmetric', config: { agg: 'avg', field: 'db.rows' } }), undefined);
    expect(back!.queries[0].metric).toBe('db.rows');
  });

  it('no service chip leaves scope empty and keeps every filter', () => {
    const filters = [{ k: 'http.method', op: '=', v: ['POST'] }];
    const back = panelToBuilder(mk({
      type: 'spanmetric',
      config: { agg: 'count', filters: JSON.stringify(filters) },
    }), undefined);
    expect(back!.queries[0].scope).toBe('');
    expect(back!.queries[0].filters).toEqual(filters);
  });
});

describe('panelToBuilder — variable expansion', () => {
  it('${service} is expanded into the scope slot', () => {
    const back = panelToBuilder(mk({
      type: 'metric',
      config: { metricName: 'cpu.util', service: '${service}', agg: 'avg' },
    }), { service: 'checkout' });
    expect(back!.queries[0].scope).toBe('checkout');
  });

  it('an empty variable drops the predicate line instead of resolving to ""', () => {
    const back = panelToBuilder(mk({
      type: 'spanmetric',
      config: { agg: 'count', dsl: 'service.name = "${service}"\nstatus = "error"' },
    }), { service: '' });
    expect(back!.queries[0].dsl).toBe('status = "error"');
  });

  it('a set variable keeps both DSL lines', () => {
    const back = panelToBuilder(mk({
      type: 'spanmetric',
      config: { agg: 'count', dsl: 'service.name = "${service}"\nstatus = "error"' },
    }), { service: 'cart' });
    expect(back!.queries[0].dsl).toBe('service.name = "cart"\nstatus = "error"');
  });

  it('an expanded filters JSON still decodes and its scope is lifted', () => {
    const back = panelToBuilder(mk({
      type: 'spanmetric',
      config: {
        agg: 'count',
        filters: '[{"k":"service.name","op":"=","v":["${service}"]}]',
      },
    }), { service: 'orders' });
    expect(back!.queries[0].scope).toBe('orders');
    expect(back!.queries[0].filters).toEqual([]);
  });
});

describe('panelToBuilder — honest refusals', () => {
  const refused: Array<[string, Panel]> = [
    ['stat', mk({ type: 'stat', config: { source: 'spanmetric', span: { agg: 'count' } } })],
    ['gauge', mk({ type: 'gauge', config: { source: 'spanmetric', span: { agg: 'count' } } })],
    ['markdown', mk({ type: 'markdown', config: { text: 'hi' } })],
    ['row', mk({ type: 'row', config: { collapsed: false } })],
    ['heatmap', mk({ type: 'heatmap', config: { metricName: 'x', unit: 'ms' } })],
    ['promql', mk({ type: 'promql', config: { query: 'up', viz: 'line' } })],
  ];
  for (const [name, panel] of refused) {
    it(`${name} panel returns null`, () => {
      expect(panelToBuilder(panel, undefined)).toBeNull();
      expect(panelToExploreHref(panel, undefined, { preset: '30m' })).toBeNull();
    });
  }
});

describe('panelToExploreHref', () => {
  it('carries the window and a decodable builder state', () => {
    const href = panelToExploreHref(
      mk({ type: 'metric', config: { metricName: 'cpu.util', agg: 'p95', service: 'cart' } }),
      undefined, { preset: '6h' });
    expect(href).not.toBeNull();
    const sp = new URLSearchParams(href!.slice(href!.indexOf('?') + 1));
    expect(href!.startsWith('/explore?')).toBe(true);
    expect(sp.get('range')).toBe('6h');
    const st = hrefState(href!);
    expect(st!.queries[0].metric).toBe('cpu.util');
    expect(st!.queries[0].agg).toBe('p95');
    expect(st!.queries[0].scope).toBe('cart');
  });

  it('encodes a custom window', () => {
    const href = panelToExploreHref(
      mk({ type: 'spanmetric', config: { agg: 'error_rate' } }),
      undefined, { preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_900_000 });
    const sp = new URLSearchParams(href!.slice(href!.indexOf('?') + 1));
    expect(sp.get('range')).toBe('custom:1700000000000-1700000900000');
  });
});

// ---------------------------------------------------------------------------
// v0.9.786 — the return trip carries the MARK too. Before this, panelToBuilder
// hard-coded viz:'line', so an operator opening a bars/area/stacked panel in
// Explore was shown a different chart than the one they clicked from. Every
// PanelVizType gets a row — the bug lives exactly in the names that don't
// match ('bar' vs 'bars') and in the one with no twin ('stacked-bar').
// ---------------------------------------------------------------------------
describe('panelToBuilder — viz round-trip', () => {
  const rows: [PanelVizType | undefined, string][] = [
    ['line', 'line'],
    ['bar', 'bars'],
    ['area', 'area'],
    ['stacked-area', 'stacked'],
    ['stacked-bar', 'stacked'],   // no Explore twin — stacking beats the mark
    [undefined, 'line'],          // absent = panel default
  ];
  for (const [cfgViz, want] of rows) {
    it(`spanmetric viz=${cfgViz ?? 'undefined'} opens Explore as ${want}`, () => {
      const st = panelToBuilder(mk({
        type: 'spanmetric',
        config: { agg: 'p95', ...(cfgViz ? { viz: cfgViz } : {}) },
      }), undefined);
      expect(st!.viz).toBe(want);
    });
  }

  it('pin → open round-trips the mark for every time-series viz', () => {
    const q = { ...blankQuery('A', 'span'), agg: 'p99', scope: 'payments' };
    for (const v of ['line', 'bars', 'area', 'stacked'] as const) {
      const panel = queryToPanel(q, { viz: v })!;
      expect(panelToBuilder(panel, undefined)!.viz, `viz=${v}`).toBe(v);
    }
  });

  // v0.9.790 — metric panelleri de markı geri getirir. Bu blok v0.9.786'da
  // "metric panels stay line" diye sabitlenmişti (MetricPanelConfig'de viz
  // alanı yoktu); alan geldiğine göre dönüş bileti spanmetric'le aynı satır
  // setinden geçmeli.
  for (const [cfgViz, want] of rows) {
    it(`metric viz=${cfgViz ?? 'undefined'} opens Explore as ${want}`, () => {
      const st = panelToBuilder(mk({
        type: 'metric',
        config: { metricName: 'cpu.util', agg: 'avg', ...(cfgViz ? { viz: cfgViz } : {}) },
      }), undefined);
      expect(st!.viz).toBe(want);
    });
  }

  it('metric pin → open round-trips the mark for every time-series viz', () => {
    const q = { ...blankQuery('A', 'metric'), metric: 'cpu.util', agg: 'avg' };
    for (const v of ['line', 'bars', 'area', 'stacked'] as const) {
      const panel = queryToPanel(q, { viz: v })!;
      expect(panel.type).toBe('metric');
      expect(panelToBuilder(panel, undefined)!.viz, `viz=${v}`).toBe(v);
    }
  });

  // Alanın YOKLUĞU hâlâ çizgi demek: v0.9.790 öncesi kaydedilmiş her metric
  // paneli viz taşımıyor ve onları bars'a kaydırmak sessiz bir görsel
  // regresyon olurdu.
  it('viz alanı olmayan ESKİ metric paneli çizgi kalır', () => {
    const st = panelToBuilder(mk({
      type: 'metric', config: { metricName: 'cpu.util', agg: 'avg' },
    }), undefined);
    expect(st!.viz).toBe('line');
  });
});
