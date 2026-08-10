// globalScope — UX denetimi B2/K9 regresyon testleri (v0.9.942).
//
// Orijinal belirti: `?env=uat` ve `?cluster=A` Explore'un adres çubuğunda
// duruyor, sorguya HİÇ girmiyordu. EndpointDetail'in "Explore →" pivotu
// ikisini de yazdığı için, cluster=A altında okunan bir endpoint'in p99'u
// Explore'da TÜM cluster'ların p99'u olarak açılıyordu.
//
// Bu dosyanın çivilediği dört şey:
//   1. boş kapsam = KİMLİK (nesne bile aynı) — memo düşmesin, cache anahtarı
//      bayt-bayt eski kalsın;
//   2. gruplu (OR) sorguda çip GRUBA da iner — yoksa fetch katmanı düz
//      filtreleri göndermediği için daraltma sessizce düşerdi;
//   3. çip anahtarları backend'in TANIDIĞI yazımlar;
//   4. enjeksiyon querySignature'ı DEĞİŞTİRİR (cache anahtarı ayrışması).

import { describe, it, expect } from 'vitest';
import { applyGlobalScope, scopeChips, ENV_FILTER_KEY, CLUSTER_FILTER_KEY } from './globalScope';
import { blankQuery, querySignature, effectiveFilters, effectiveFilterGroup, hasGroupedFilter, exemplarDescriptor } from './model';
import type { BuilderState } from './model';

const st = (over?: Partial<ReturnType<typeof blankQuery>>): BuilderState => ({
  queries: [{ ...blankQuery('A'), ...over }],
  formula: '', viz: 'line', step: 0,
});

describe('scopeChips', () => {
  it('boş değerler çip üretmez — yokluk "hepsi" demek', () => {
    expect(scopeChips('', '')).toEqual([]);
    expect(scopeChips('   ', '  ')).toEqual([]);
  });

  it('backend’in tanıdığı anahtarları üretir', () => {
    expect(scopeChips('uat', 'ocp-a')).toEqual([
      { k: ENV_FILTER_KEY, op: '=', v: ['uat'] },
      { k: CLUSTER_FILTER_KEY, op: '=', v: ['ocp-a'] },
    ]);
    // Yazımlar backend eşlemeleriyle birebir olmalı (filterexpr.go
    // wellKnown / metricPointsWellKnown).
    expect(ENV_FILTER_KEY).toBe('deployment.environment');
    expect(CLUSTER_FILTER_KEY).toBe('cluster');
  });

  it('yalnız biri verilse tek çip', () => {
    expect(scopeChips('prod', '')).toHaveLength(1);
    expect(scopeChips('', 'ocp-b')).toHaveLength(1);
  });
});

describe('applyGlobalScope — kimlik dalı', () => {
  it('kapsam yoksa AYNI nesne döner (memo düşmesin)', () => {
    const s = st();
    expect(applyGlobalScope(s, '', '')).toBe(s);
  });

  it('kapsam yokken imza bayt-bayt aynı kalır', () => {
    const s = st();
    expect(querySignature(applyGlobalScope(s, '', '').queries[0], 60))
      .toBe(querySignature(s.queries[0], 60));
  });
});

describe('applyGlobalScope — düz filtre dalı', () => {
  it('çipler fetch’e giden filtrelere iner', () => {
    const out = applyGlobalScope(st(), 'uat', 'ocp-a');
    const f = effectiveFilters(out.queries[0]);
    expect(f).toContainEqual({ k: ENV_FILTER_KEY, op: '=', v: ['uat'] });
    expect(f).toContainEqual({ k: CLUSTER_FILTER_KEY, op: '=', v: ['ocp-a'] });
  });

  it('operatörün kendi çiplerini KORUR', () => {
    const s = st({ filters: [{ k: 'http.route', op: '=', v: ['/pay'] }] });
    const f = effectiveFilters(applyGlobalScope(s, 'uat', '').queries[0]);
    expect(f).toContainEqual({ k: 'http.route', op: '=', v: ['/pay'] });
    expect(f).toHaveLength(2);
  });

  it('girdi state MUTASYONA UĞRAMAZ', () => {
    const s = st();
    applyGlobalScope(s, 'uat', 'ocp-a');
    expect(s.queries[0].filters).toEqual([]);
  });

  it('imza değişir — env/cluster cache anahtarına girer (v0.5.187 sınıfı)', () => {
    const base = st();
    const a = querySignature(applyGlobalScope(base, 'uat', '').queries[0], 60);
    const b = querySignature(applyGlobalScope(base, 'prep', '').queries[0], 60);
    const c = querySignature(applyGlobalScope(base, 'uat', 'ocp-a').queries[0], 60);
    expect(new Set([a, b, c]).size).toBe(3);
  });
});

describe('applyGlobalScope — GRUPLU (OR) sorgu dalı', () => {
  // ASIL RİSK: fetch katmanı gruplu sorguda düz `filters`i GÖNDERMEZ
  // (effectiveFilterGroup supersedes). Yalnız düz listeye yazsaydık,
  // OR'lu filtre kuran operatörde daraltma SESSİZCE düşerdi.
  const grouped = st({
    filterGroup: {
      join: 'OR',
      filters: [
        { k: 'http.status_code', op: '=', v: ['500'] },
        { k: 'http.status_code', op: '=', v: ['503'] },
      ],
    },
  });

  it('önkoşul: bu sorgu gerçekten gruplu sayılıyor', () => {
    expect(hasGroupedFilter(grouped.queries[0])).toBe(true);
  });

  it('çip grubun EN ÜST seviyesine iner (AND tarafı)', () => {
    const out = applyGlobalScope(grouped, 'uat', 'ocp-a');
    const g = effectiveFilterGroup(out.queries[0]);
    expect(g).not.toBeNull();
    expect(g!.filters).toContainEqual({ k: ENV_FILTER_KEY, op: '=', v: ['uat'] });
    expect(g!.filters).toContainEqual({ k: CLUSTER_FILTER_KEY, op: '=', v: ['ocp-a'] });
    // İçerideki OR bozulmadı.
    expect(g!.join).toBe('OR');
  });

  it('gruplu sorguda da imza ayrışır', () => {
    expect(querySignature(applyGlobalScope(grouped, 'uat', '').queries[0], 60))
      .not.toBe(querySignature(grouped.queries[0], 60));
  });

  it('düz-AND grup çipi grup dalına YAZMAZ (düz yol tek kaynak kalır)', () => {
    const flat = st({ filterGroup: { join: 'AND', filters: [{ k: 'kind', op: '=', v: ['server'] }] } });
    const out = applyGlobalScope(flat, 'uat', '');
    // Düz-AND grup inert; effectiveFilterGroup null döner ve düz yol
    // çipi zaten taşıyor.
    expect(effectiveFilterGroup(out.queries[0])).toBeNull();
    expect(effectiveFilters(out.queries[0])).toContainEqual({ k: ENV_FILTER_KEY, op: '=', v: ['uat'] });
  });
});

describe('rollup hızlı yolu — SESSİZ YOK SAYMA OLMAZ', () => {
  // spanmetrics rollup'ında env/cluster boyutu YOK
  // (chstore/rollup_fastpath_test.go). Resolver'a gitseydi daraltma
  // sessizce yok sayılır, panel "uat" derken tüm ortamları çizerdi.
  it('kapsam yokken resolver UYGUN', () => {
    expect(exemplarDescriptor(st({ agg: 'p99', metric: 'duration_ms' }).queries[0])).not.toBeNull();
  });

  it('env çipi resolver’ı diskalifiye eder → ham yol daraltmayı UYGULAR', () => {
    const out = applyGlobalScope(st({ agg: 'p99', metric: 'duration_ms' }), 'uat', '');
    expect(exemplarDescriptor(out.queries[0])).toBeNull();
  });

  it('cluster çipi de diskalifiye eder', () => {
    const out = applyGlobalScope(st({ agg: 'p99', metric: 'duration_ms' }), '', 'ocp-a');
    expect(exemplarDescriptor(out.queries[0])).toBeNull();
  });
});

describe('çok sorgulu builder', () => {
  it('HER üretici sorgu daralır — biri kaçamaz', () => {
    const s: BuilderState = {
      queries: [blankQuery('A'), blankQuery('B'), blankQuery('C')],
      formula: '', viz: 'line', step: 0,
    };
    const out = applyGlobalScope(s, 'uat', '');
    for (const q of out.queries) {
      expect(effectiveFilters(q)).toContainEqual({ k: ENV_FILTER_KEY, op: '=', v: ['uat'] });
    }
  });
});
