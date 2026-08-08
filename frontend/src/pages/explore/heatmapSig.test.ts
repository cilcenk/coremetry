import { describe, it, expect } from 'vitest';
import { heatmapQuerySig } from './heatmapSig';
import { blankQuery, defaultBuilderState, type BuilderState } from './model';

// v0.9.810 — heatmap effect'inin bağımlılığı TÜM builder state'iydi.
// /api/spans/heatmap yalnız üreten İLK sorgunun filtrelerini + DSL'ini +
// pencereyi alıyor; B/C düzenlemeleri ve formül metni isteğe hiç
// dokunmuyor ama sayfanın en pahalı taramasını yeniden tetikliyordu.
//
// Testin iki yönü var ve İKİSİ de gerekli:
//   • DEĞİŞMELİ: isteğe giden bir alan oynadığında imza oynar.
//   • DEĞİŞMEMELİ: isteğe girmeyen bir alan oynadığında imza sabit kalır.
// Yalnız ikincisini yazmak, imzayı sabite eşitleyerek "geçen" bir test
// üretirdi.

const heat = (mut: (s: BuilderState) => BuilderState = s => s): BuilderState =>
  mut({ ...defaultBuilderState(), viz: 'heatmap', queries: ['A', 'B'].map(l => blankQuery(l)) });

const FROM = 1_751_980_000_000_000_000;
const TO = FROM + 3_600e9;
const sig = (s: BuilderState, from = FROM, to = TO, buckets = 80) =>
  heatmapQuerySig(s, from, to, buckets);

describe('heatmapQuerySig — DEĞİŞMELİ', () => {
  it('A sorgusunun filtresi', () => {
    const withChip = heat(s => ({
      ...s,
      queries: s.queries.map(q => q.letter === 'A'
        ? { ...q, filters: [{ k: 'service.name', op: '=', v: ['checkout'] }] } : q),
    }));
    expect(sig(withChip)).not.toBe(sig(heat()));
  });

  it('A sorgusunun DSL\'i', () => {
    const withDsl = heat(s => ({
      ...s,
      queries: s.queries.map(q => q.letter === 'A' ? { ...q, dsl: "status = 'ERROR'" } : q),
    }));
    expect(sig(withDsl)).not.toBe(sig(heat()));
  });

  it('pencere', () => {
    expect(sig(heat(), FROM, TO + 60e9)).not.toBe(sig(heat()));
  });

  it('bucket sayısı (genişlik kovası değişimi)', () => {
    expect(sig(heat(), FROM, TO, 120)).not.toBe(sig(heat()));
  });

  it('viz — heatmap\'ten çıkış bekleyen kutu-seçimini temizlemeli', () => {
    const asLine = heat(s => ({ ...s, viz: 'line' }));
    expect(sig(asLine)).not.toBe(sig(heat()));
  });

  it('A DEVRE DIŞI kalırsa "üreten ilk sorgu" B olur → imza oynar', () => {
    const bScoped = heat(s => ({
      ...s,
      queries: s.queries.map(q => q.letter === 'B'
        ? { ...q, filters: [{ k: 'service.name', op: '=', v: ['cart'] }] } : q),
    }));
    const aOff = { ...bScoped, queries: bScoped.queries.map(q => q.letter === 'A' ? { ...q, enabled: false } : q) };
    expect(sig(aOff)).not.toBe(sig(bScoped));
  });
});

describe('heatmapQuerySig — DEĞİŞMEMELİ (daraltmanın kendisi)', () => {
  it('🔴 B sorgusunun agg\'i — isteğe girmiyor', () => {
    const bAgg = heat(s => ({
      ...s,
      queries: s.queries.map(q => q.letter === 'B' ? { ...q, agg: 'p99' } : q),
    }));
    expect(sig(bAgg)).toBe(sig(heat()));
  });

  it('🔴 B sorgusunun filtresi — heatmap A ile çiziliyor', () => {
    const bChip = heat(s => ({
      ...s,
      queries: s.queries.map(q => q.letter === 'B'
        ? { ...q, filters: [{ k: 'http.route', op: '=', v: ['/pay'] }] } : q),
    }));
    expect(sig(bChip)).toBe(sig(heat()));
  });

  it('🔴 formül metni', () => {
    expect(sig(heat(s => ({ ...s, formula: 'A / B' })))).toBe(sig(heat()));
  });

  it('🔴 topN / logY / step — hiçbiri heatmap isteğinde yok', () => {
    expect(sig(heat(s => ({ ...s, topN: 5 })))).toBe(sig(heat()));
    expect(sig(heat(s => ({ ...s, logY: true })))).toBe(sig(heat()));
    expect(sig(heat(s => ({ ...s, step: 300 })))).toBe(sig(heat()));
  });

  it('A sorgusunun splitBy\'ı — heatmap gruplama taşımıyor', () => {
    const aSplit = heat(s => ({
      ...s,
      queries: s.queries.map(q => q.letter === 'A' ? { ...q, splitBy: ['name'] } : q),
    }));
    expect(sig(aSplit)).toBe(sig(heat()));
  });
});

describe('heatmapQuerySig — kenar durumlar', () => {
  it('heatmap DIŞINDA imza yalnız viz taşır (istek atılmıyor)', () => {
    const line = heat(s => ({ ...s, viz: 'line' }));
    const lineOther = heat(s => ({
      ...s, viz: 'line',
      queries: s.queries.map(q => ({ ...q, agg: 'p95' })),
    }));
    expect(sig(line)).toBe(sig(lineOther));
  });

  it('üreten sorgu yoksa "none" — pencere bile imzayı oynatmaz', () => {
    const none = heat(s => ({ ...s, queries: s.queries.map(q => ({ ...q, enabled: false })) }));
    expect(sig(none)).toBe('viz=heatmap|none');
    expect(sig(none, FROM, TO + 60e9)).toBe('viz=heatmap|none');
  });
});
