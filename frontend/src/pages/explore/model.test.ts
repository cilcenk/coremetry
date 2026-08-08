import { describe, it, expect } from 'vitest';
import {
  blankQuery, exemplarDescriptor, pinnedService, pinnedOperation,
  seriesGroupLabel, queryUnit, compareOffsetNs, compareLabel,
  type BuilderQuery, type ExploreCompare,
} from './model';
import type { FilterExpr } from '@/lib/types';

// explore-v2 Phase-3 — pins the exemplar-eligibility gate and the SLO/deploy
// service-pin extraction. exemplarDescriptor mirrors the backend planner
// (chstore/metricresolve.go tierDimColumn + spanmetricStateAgg): a query the
// rollups can't serve MUST return null, or the panel fires a resolveMetric
// that falls back to the raw-spans path and returns no exemplars anyway —
// a wasted billion-row-window query per render cycle.

const f = (k: string, v: string, op: FilterExpr['op'] = '='): FilterExpr =>
  ({ k, op, v: [v] });

const q = (over: Partial<BuilderQuery>): BuilderQuery =>
  ({ ...blankQuery('A'), ...over });

describe('exemplarDescriptor', () => {
  it('accepts a plain p95 duration query and maps it onto spanmetrics', () => {
    const d = exemplarDescriptor(q({
      agg: 'p95', metric: 'duration_ms', scope: 'checkout',
      splitBy: ['name'], filters: [f('kind', 'server')],
    }));
    expect(d).not.toBeNull();
    expect(d!.source).toBe('spanmetrics');
    expect(d!.metric).toBe('duration_milliseconds_bucket');
    expect(d!.agg).toBe('p95');
    expect(d!.filters).toEqual({ 'service.name': 'checkout', kind: 'server' });
    expect(d!.groupBy).toEqual(['name']);
  });

  it('maps count-shaped aggs onto calls_total with no groupBy when splitBy empty', () => {
    const d = exemplarDescriptor(q({ agg: 'rate' }));
    expect(d).not.toBeNull();
    expect(d!.metric).toBe('calls_total');
    expect(d!.groupBy).toBeUndefined();
  });

  // Table of rejections — every row is a query the rollups cannot serve.
  const rejects: Array<[string, Partial<BuilderQuery>]> = [
    ['metric source',            { source: 'metric', metric: 'jvm.gc.pause', agg: 'avg' }],
    ['DSL present',              { dsl: 'duration > 500ms' }],
    ['p999 (not on rollups)',    { agg: 'p999', metric: 'duration_ms' }],
    ['min (not on rollups)',     { agg: 'min', metric: 'duration_ms' }],
    ['non-duration field',       { agg: 'p95', metric: 'http.response_size' }],
    ['off-dim filter',           { filters: [f('db.system', 'postgresql')] }],
    ['non-eq operator',          { filters: [{ k: 'kind', op: 'IN', v: ['server', 'client'] }] }],
    ['off-dim splitBy',          { splitBy: ['db.system'] }],
    ['contradictory dup filter', { filters: [f('kind', 'server'), f('kind', 'client')] }],
    // gap-2 → Explore: a genuine OR / nested group can't be expressed as the
    // resolver's equality-only filter map, so it must fall to raw spanMetric.
    ['grouped OR filter', {
      filterGroup: { join: 'OR', filters: [f('kind', 'server'), f('kind', 'client')] },
    }],
  ];
  for (const [name, over] of rejects) {
    it(`rejects: ${name}`, () => {
      expect(exemplarDescriptor(q(over))).toBeNull();
    });
  }

  it('accepts a duplicate filter that agrees (no information lost in the map)', () => {
    const d = exemplarDescriptor(q({ filters: [f('kind', 'server'), f('kind', 'server')] }));
    expect(d).not.toBeNull();
  });

  // A flat-AND group is inert (byte-identical to the flat chip path), so it
  // must NOT disqualify the resolver — exemplars keep working for it.
  it('accepts a flat-AND filterGroup (inert; exemplars still resolve)', () => {
    const d = exemplarDescriptor(q({
      agg: 'p95', metric: 'duration_ms',
      filterGroup: { join: 'AND', filters: [f('kind', 'server')] },
    }));
    expect(d).not.toBeNull();
  });
});

describe('pinnedService / pinnedOperation', () => {
  it('scope slot wins', () => {
    expect(pinnedService(q({ scope: 'checkout' }))).toBe('checkout');
  });
  it('single service.name = chip pins', () => {
    expect(pinnedService(q({ filters: [f('service.name', 'cart')] }))).toBe('cart');
  });
  it('two service chips = ambiguous = no pin', () => {
    expect(pinnedService(q({
      filters: [f('service.name', 'cart'), f('service.name', 'checkout')],
    }))).toBe('');
  });
  it('non-eq service chip does not pin', () => {
    expect(pinnedService(q({ filters: [f('service.name', 'cart', 'LIKE')] }))).toBe('');
  });
  it('operation pin from a single name = chip', () => {
    expect(pinnedOperation(q({ filters: [f('name', 'GET /cart')] }))).toBe('GET /cart');
    expect(pinnedOperation(q({}))).toBe('');
  });
});

describe('seriesGroupLabel', () => {
  it('matches the PanelStack label derivation (key tail + value, comma-joined)', () => {
    const query = q({ splitBy: ['service.name', 'name'] });
    expect(seriesGroupLabel(query, ['cart', 'GET /cart'], 'desc'))
      .toBe('name=cart, name=GET /cart');
  });
  it('falls back to the query desc when there is no group', () => {
    expect(seriesGroupLabel(q({}), [], 'count')).toBe('count');
  });
  // v0.8.411 — agg=band folds the quantile into the LAST groupKey
  // element (one more than splitBy); it must name the line, not be
  // mislabeled as a split dimension.
  it('band: bare quantile names the line', () => {
    expect(seriesGroupLabel(q({ agg: 'band' }), ['p95'], 'desc')).toBe('p95');
  });
  it('band: grouped key keeps split labels aligned + appends the quantile', () => {
    const query = q({ agg: 'band', splitBy: ['service.name'] });
    expect(seriesGroupLabel(query, ['checkout', 'p99'], 'desc'))
      .toBe('name=checkout \u00b7 p99');
  });
  it('band: non-band queries are untouched by the peel', () => {
    const query = q({ agg: 'p95', splitBy: ['service.name'] });
    expect(seriesGroupLabel(query, ['checkout'], 'desc')).toBe('name=checkout');
  });
});

// ---------------------------------------------------------------------------
// queryUnit — v0.9.801. Panelin GÖRÜNTÜ birimini çözen tek fonksiyon.
//
// Operatör raporu (Explore süre metrikleri çıplak saniye basıyordu) iki ayrı
// kusur ortaya çıkardı ve ikisi de buradan geçer:
//   1. tohum yolları q.unit'i hiç doldurmuyordu → '' → birimsiz panel
//      (doldurma tarafı metricUnits.test.ts'te),
//   2. dolan birim HAM OTLP'ydi ve Grafana kimliğine çevrilmiyordu —
//      's'/'ms' iki alfabede aynı yazıldığı için o dal KAZARA doğruydu,
//      'By'/'1' ise bilinmeyen kimlik olarak iniyordu.
//
// SPAN DALI PİNLİ: bu değişiklik span sorgularının birimine dokunmaz.
// ---------------------------------------------------------------------------
describe('queryUnit', () => {
  const metricQ = (over: Partial<BuilderQuery>): BuilderQuery =>
    ({ ...blankQuery('A', 'metric'), ...over });

  const metricCases: [string, string][] = [
    ['s', 's'],           // prod semconv süre metriği → alt-saniye "348 ms" olur
    ['ms', 'ms'],         // lokal demo süre metriği
    ['ns', 'ns'],
    ['By', 'bytes'],      // UCUM → Grafana kimliği (eskiden ham geçiyordu)
    ['%', 'percent'],
    ['1', ''],            // boyutsuz = birim YOK
    ['', ''],             // katalogda birim yok → dürüstçe birimsiz
    ['{request}', ''],    // UCUM annotation → ham sayı
    ['furlong', ''],      // bilinmeyen → sessizce ms DEĞİL
  ];
  for (const [raw, want] of metricCases) {
    it(`metric source: katalog '${raw}' → panel '${want}'`, () => {
      expect(queryUnit(metricQ({ metric: 'm', unit: raw }))).toBe(want);
    });
  }

  // spanAggUnit sözleşmesi DEĞİŞMEDİ — bu satırlar regresyon çapası.
  const spanCases: [string, string][] = [
    ['count', ''], ['rate', '/s'], ['per_min', '/min'], ['error_rate', '%'],
    ['apdex', ''], ['avg', 'ms'], ['p95', 'ms'], ['p999', 'ms'], ['band', 'ms'],
  ];
  for (const [agg, want] of spanCases) {
    it(`span source: ${agg} → '${want}'`, () => {
      expect(queryUnit(q({ agg }))).toBe(want);
    });
  }

  it('span sorgusunun q.unit alanı GÖRMEZDEN gelinir (birim agg\'den)', () => {
    expect(queryUnit(q({ agg: 'p95', unit: 'By' }))).toBe('ms');
  });
});

// ── v0.9.824 — önceki döneme karşılaştırma penceresi ────────────────────────
//
// compareOffsetNs, hayaletin bugünün eksenine BİREBİR bindirilmesini sağlayan
// tek sayı. Yanlış olursa grafik yine çizilir — yalnız yanlış yerde, ve bunu
// gösteren hiçbir şey yoktur. Bu yüzden üç kip de, DST sınırı da tabloda.

describe('compareOffsetNs (v0.9.824)', () => {
  // 2026-08-08 10:00Z → 11:00Z, unix ns.
  const from = Date.UTC(2026, 7, 8, 10, 0, 0) * 1e6;
  const to = Date.UTC(2026, 7, 8, 11, 0, 0) * 1e6;

  const cases: Array<{ cmp: ExploreCompare | undefined; want: number; why: string }> = [
    { cmp: undefined, want: 0, why: 'kapalı — ikinci fan-out HİÇ koşmaz' },
    { cmp: '24h', want: 24 * 3600 * 1e9, why: 'tam 24 saat' },
    { cmp: '7d', want: 7 * 24 * 3600 * 1e9, why: 'tam 7 gün' },
    { cmp: 'prev', want: 3600 * 1e9, why: 'pencerenin kendi genişliği (1 saat)' },
  ];
  for (const c of cases) {
    it(`${c.cmp ?? '(kapalı)'} → ${c.why}`, () => {
      expect(compareOffsetNs(c.cmp, from, to)).toBe(c.want);
    });
  }

  it('prev: 7 günlük pencerede offset de 7 gün', () => {
    const f = Date.UTC(2026, 7, 1) * 1e6;
    const t = Date.UTC(2026, 7, 8) * 1e6;
    expect(compareOffsetNs('prev', f, t)).toBe(7 * 24 * 3600 * 1e9);
  });

  it('ters / sıfır pencerede prev = 0 (hayalet KAPANIR, uydurulmuş kaydırma yok)', () => {
    expect(compareOffsetNs('prev', to, from)).toBe(0);
    expect(compareOffsetNs('prev', from, from)).toBe(0);
  });

  // DST — asıl mesele. 2026'da Avrupa'da saatler 25 Ekim'de geri alınıyor.
  // O sınırı KAPSAYAN bir pencerede bile offset TAM 24 saattir: hesap UTC
  // nanosaniye aritmetiği, takvim değil. Takvim-duyarlı bir offset kullansaydık
  // hayalet kovaları bir saat kayar ve çizgi güncelin YANINA değil ARASINA
  // düşerdi — operatör bunu "veri kaymış" diye okur.
  it('DST sınırını geçen pencerede bile 24h TAM 24 saat (UTC ns aritmetiği)', () => {
    const dstFrom = Date.UTC(2026, 9, 25, 0, 30, 0) * 1e6;   // 25 Ekim 00:30Z
    const dstTo = Date.UTC(2026, 9, 25, 3, 30, 0) * 1e6;
    expect(compareOffsetNs('24h', dstFrom, dstTo)).toBe(24 * 3600 * 1e9);
    // prev de saf fark — 3 saat, 4 değil.
    expect(compareOffsetNs('prev', dstFrom, dstTo)).toBe(3 * 3600 * 1e9);
  });

  it('offset pencereden BAĞIMSIZ (24h/7d) — aralık değişimi kaydırmayı oynatmaz', () => {
    const wide = compareOffsetNs('24h', from, from + 30 * 24 * 3600 * 1e9);
    expect(wide).toBe(compareOffsetNs('24h', from, to));
  });
});

describe('compareLabel', () => {
  it.each([
    ['24h', '24 saat önce'],
    ['7d', '7 gün önce'],
    ['prev', 'önceki pencere'],
  ] as const)('%s → %s', (cmp, want) => {
    expect(compareLabel(cmp)).toBe(want);
  });

  it('kapalı kip boş metin (şerit başlığı hiç çizilmez)', () => {
    expect(compareLabel(undefined)).toBe('');
  });
});
