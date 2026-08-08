import { describe, it, expect } from 'vitest';
import { buildPipelineRuleBody, describeCondition, type PipelineRuleForm } from './pipelineRuleBody';
import type { PipelineRule } from '@/lib/api';

// v0.9.803 regression — PipelineRuleModal düzenlemede gövdeyi sıfırdan
// kuruyor ve `and` koşullarını TAŞIMIYORDU. Somut zarar: v0.9.797'nin
// türettiği "metric-excl-*" kuralı
//
//   when: http.route =~ ^/health   AND   metric = http.server.duration
//
// UI'dan düzenlenince tek-metrik kısıtı düşüyor, ingest drop'u SESSİZCE
// bütün metriklere yayılıyordu — ve yazılmayan datapoint geri gelmez.

const form = (o: Partial<PipelineRuleForm> = {}): PipelineRuleForm => ({
  name: 'metric-excl: http.server.duration ~ ^/health',
  kind: 'drop',
  signal: 'metrics',
  enabled: true,
  whenKey: 'http.route',
  whenOp: '=~',
  whenValue: '^/health',
  enrichKey: '',
  enrichValue: '',
  rate: 0.1,
  ...o,
});

const derivedRule = (): PipelineRule => ({
  id: 'metric-excl-9f2c',
  name: 'metric-excl: http.server.duration ~ ^/health',
  kind: 'drop',
  signal: 'metrics',
  enabled: true,
  when: { key: 'http.route', op: '=~', value: '^/health' },
  and: [{ key: 'metric', op: '=', value: 'http.server.duration' }],
});

describe('buildPipelineRuleBody — `and` gidiş-dönüşü', () => {
  it('düzenlemede `and` AYNEN taşınır (v0.9.803 kayıp)', () => {
    const body = buildPipelineRuleBody(derivedRule(), form());
    expect(body.and).toEqual([{ key: 'metric', op: '=', value: 'http.server.duration' }]);
  });

  it('yalnız adı değişen bir düzenleme kuralın geri kalanını bozmaz', () => {
    const existing = derivedRule();
    const body = buildPipelineRuleBody(existing, form({ name: '  yeni ad  ' }));
    expect(body).toEqual({
      id: 'metric-excl-9f2c',
      name: 'yeni ad',
      kind: 'drop',
      signal: 'metrics',
      enabled: true,
      when: { key: 'http.route', op: '=~', value: '^/health' },
      and: [{ key: 'metric', op: '=', value: 'http.server.duration' }],
    });
  });

  it('çok koşullu `and` sırasıyla birlikte korunur', () => {
    const existing: PipelineRule = {
      ...derivedRule(),
      and: [
        { key: 'metric', op: '=', value: 'http.server.duration' },
        { key: 'service.name', op: 'startsWith', value: 'frontend' },
        { key: 'unit', op: '!=', value: 'By' },
      ],
    };
    const body = buildPipelineRuleBody(existing, form());
    expect(body.and).toEqual(existing.and);
  });

  it('`and` taşınırken KOPYALANIR — gövde kaynağı paylaşmaz', () => {
    const existing = derivedRule();
    const body = buildPipelineRuleBody(existing, form());
    expect(body.and).not.toBe(existing.and);
    expect(body.and![0]).not.toBe(existing.and![0]);
  });

  it('kind değişse bile `and` düşmez (koşul kind-bağımsız)', () => {
    const body = buildPipelineRuleBody(derivedRule(), form({ kind: 'sample', rate: 0.25 }));
    expect(body.and).toEqual([{ key: 'metric', op: '=', value: 'http.server.duration' }]);
    expect(body.rate).toBe(0.25);
  });
});

describe('buildPipelineRuleBody — `and` yokken alan da yok', () => {
  const cases: Array<{ name: string; and: PipelineRule['and'] }> = [
    { name: 'tanımsız', and: undefined },
    { name: 'boş liste', and: [] },
  ];
  for (const c of cases) {
    it(`${c.name} → gövdede \`and\` anahtarı bulunmaz`, () => {
      const existing: PipelineRule = { ...derivedRule(), and: c.and };
      const body = buildPipelineRuleBody(existing, form());
      expect('and' in body).toBe(false);
    });
  }

  it('yeni kuralda (existing null) `and` yoktur', () => {
    const body = buildPipelineRuleBody(null, form());
    expect('and' in body).toBe(false);
    expect(body.id).toBe('');
  });
});

describe('buildPipelineRuleBody — form alanları', () => {
  it('ad ve predicate trim edilir', () => {
    const body = buildPipelineRuleBody(null, form({
      name: '  drop probes  ', whenKey: ' http.route ', whenValue: '  ^/probe  ',
    }));
    expect(body.name).toBe('drop probes');
    expect(body.when).toEqual({ key: 'http.route', op: '=~', value: '^/probe' });
  });

  it('enrich → setAttributes; sample/drop → yok', () => {
    const enriched = buildPipelineRuleBody(null, form({
      kind: 'enrich', enrichKey: ' team ', enrichValue: ' payments ',
    }));
    expect(enriched.setAttributes).toEqual({ team: 'payments' });
    expect(enriched.rate).toBeUndefined();

    const sampled = buildPipelineRuleBody(null, form({ kind: 'sample', rate: 0.5 }));
    expect(sampled.setAttributes).toBeUndefined();
    expect(sampled.rate).toBe(0.5);

    const dropped = buildPipelineRuleBody(null, form({ kind: 'drop' }));
    expect(dropped.setAttributes).toBeUndefined();
    expect(dropped.rate).toBeUndefined();
  });

  it('enrich anahtarı boşken setAttributes boş nesne (sunucu temizler)', () => {
    const body = buildPipelineRuleBody(null, form({ kind: 'enrich', enrichKey: '   ' }));
    expect(body.setAttributes).toEqual({});
  });
});

describe('describeCondition', () => {
  it('okunur tek satır üretir', () => {
    expect(describeCondition({ key: 'metric', op: '=', value: 'http.server.duration' }))
      .toBe('metric = http.server.duration');
    expect(describeCondition({ key: 'http.route', op: '=~', value: '^/health' }))
      .toBe('http.route =~ ^/health');
  });
});
