import { describe, expect, it } from 'vitest';
import { buildPanels, IDLE_HINT, type PanelInputs } from './PanelStack';
import { blankQuery, defaultBuilderState, type BuilderState } from './model';
import type { SpanMetricSeries } from '@/lib/types';

// v0.9.804 regression — buildPanels had exactly ONE branch for "no data":
//
//   if (data === undefined) → { loading: true }
//
// and react-query leaves `data` undefined in three very different
// situations, two of which never resolve:
//
//   (a) PARAMSIZ /explore. builderActive=false → builderFrom stays 0 → the
//       fan-out is `enabled: false` → data is undefined for the lifetime of
//       the page. Panel A spun forever on a query that was never sent.
//   (b) FAILED query. isError leaves data undefined, so the panel spun on
//       top of a dead fetch; only the FIRST failing letter got a message,
//       in a page-level band, and the panel itself said nothing.
//   (c) genuinely in flight — the only case the spinner was ever right for.
//
// The table below pins all four states, plus the two-error scenario the old
// single-`error` shape could not represent.

const q = (letter: string) => blankQuery(letter);

const stateWith = (letters: string[], formula = ''): BuilderState => ({
  ...defaultBuilderState(),
  queries: letters.map(q),
  formula,
});

const series = (label: string, value = 1): SpanMetricSeries => ({
  groupKey: [label],
  points: [{ time: 1_751_980_000_000_000_000, value }],
} as SpanMetricSeries);

// ACTIVE_FROM — any non-zero unix-ns window start means "the fan-out ran".
const ACTIVE_FROM = 1_751_980_000_000_000_000;

describe('buildPanels — dört durum', () => {
  const cases: Array<{
    name: string;
    inputs: PanelInputs;
    wantState: 'idle' | 'loading' | 'error' | 'ready';
    wantMessage?: string;
    wantEmptyReason?: string | undefined;
  }> = [
    {
      name: 'from=0 (paramsız /explore) → idle, spinner DEĞİL',
      inputs: { byLetter: {}, from: 0 },
      wantState: 'idle',
      wantEmptyReason: IDLE_HINT,
    },
    {
      name: 'from=0 + elde veri olsa bile idle (hiç sorulmadı)',
      inputs: { byLetter: { A: [series('x')] }, from: 0 },
      wantState: 'idle',
      wantEmptyReason: IDLE_HINT,
    },
    {
      name: 'aktif + data undefined → loading',
      inputs: { byLetter: {}, from: ACTIVE_FROM },
      wantState: 'loading',
    },
    {
      name: 'aktif + hata → error, harfin kendi mesajıyla',
      inputs: { byLetter: {}, from: ACTIVE_FROM, errorByLetter: { A: 'ch: timeout' } },
      wantState: 'error',
      wantMessage: 'ch: timeout',
    },
    {
      name: 'hata, data undefined dalını YENER (isError ikisini birden verir)',
      inputs: {
        byLetter: { A: undefined }, from: ACTIVE_FROM,
        errorByLetter: { A: 'HTTP 500' },
      },
      wantState: 'error',
      wantMessage: 'HTTP 500',
    },
    {
      name: 'aktif + boş dizi → ready, "veri yok" gerekçesiyle',
      inputs: { byLetter: { A: [] }, from: ACTIVE_FROM },
      wantState: 'ready',
      wantEmptyReason: 'Bu pencerede veri yok — aralığı genişlet veya filtreleri azalt',
    },
    {
      name: 'aktif + seri → ready, gerekçe yok',
      inputs: { byLetter: { A: [series('x')] }, from: ACTIVE_FROM },
      wantState: 'ready',
      wantEmptyReason: undefined,
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const [p] = buildPanels(stateWith(['A']), c.inputs);
      expect(p.state).toBe(c.wantState);
      expect(p.errorMessage).toBe(c.wantMessage);
      if ('wantEmptyReason' in c) expect(p.emptyReason).toBe(c.wantEmptyReason);
    });
  }

  it('from varsayılanı 0 — inputs yalnız byLetter taşıyorsa idle', () => {
    const [p] = buildPanels(stateWith(['A']), { byLetter: { A: [series('x')] } });
    expect(p.state).toBe('idle');
  });
});

describe('buildPanels — İKİ hatalı sorgu', () => {
  // Eski `error: {letter,message} | null` şekli yalnız ilkini taşıyabiliyordu.
  const inputs: PanelInputs = {
    byLetter: { B: [series('ok')] },
    from: ACTIVE_FROM,
    errorByLetter: { A: 'ch: timeout', C: 'HTTP 500' },
  };

  it('her başarısız harf KENDİ mesajını taşır', () => {
    const panels = buildPanels(stateWith(['A', 'B', 'C']), inputs);
    const byLetter = Object.fromEntries(panels.map(p => [p.letter, p]));

    expect(byLetter.A.state).toBe('error');
    expect(byLetter.A.errorMessage).toBe('ch: timeout');
    expect(byLetter.C.state).toBe('error');
    expect(byLetter.C.errorMessage).toBe('HTTP 500');
  });

  it('sağlam sorgu hatalardan etkilenmez', () => {
    const panels = buildPanels(stateWith(['A', 'B', 'C']), inputs);
    const b = panels.find(p => p.letter === 'B')!;
    expect(b.state).toBe('ready');
    expect(b.errorMessage).toBeUndefined();
    expect(b.series).toHaveLength(1);
  });

  it('hatalı panel seri taşımaz (boş grafik yerine sebep gösterilir)', () => {
    const panels = buildPanels(stateWith(['A', 'B', 'C']), inputs);
    for (const p of panels.filter(x => x.state === 'error')) {
      expect(p.series).toHaveLength(0);
      expect(p.more).toBe(0);
    }
  });
});

describe('buildPanels — formül paneli durumu girdilerini izler', () => {
  const withFormula = stateWith(['A', 'B'], 'A + B');
  const formulaPanel = (inputs: PanelInputs) =>
    buildPanels(withFormula, inputs).find(p => p.isFormula)!;

  it('from=0 → idle', () => {
    expect(formulaPanel({ byLetter: {}, from: 0 }).state).toBe('idle');
  });

  it('bir girdi hata verdiyse → error (girdiler gelemez)', () => {
    const p = formulaPanel({
      byLetter: { A: [series('x')], B: undefined },
      from: ACTIVE_FROM,
      errorByLetter: { B: 'HTTP 500' },
    });
    expect(p.state).toBe('error');
    expect(p.errorMessage).toContain('B');
  });

  it('birden çok girdi hata verdiyse hepsi mesajda', () => {
    const p = formulaPanel({
      byLetter: {}, from: ACTIVE_FROM,
      errorByLetter: { B: 'HTTP 500', A: 'ch: timeout' },
    });
    expect(p.errorMessage).toBe('Girdi sorgusu A, B hata verdi');
  });

  it('girdi hâlâ uçuşta → loading', () => {
    const p = formulaPanel({ byLetter: { A: [series('x')], B: undefined }, from: ACTIVE_FROM });
    expect(p.state).toBe('loading');
  });

  it('girdiler geldi ama örtüşme yok → ready + formül gerekçesi', () => {
    const p = formulaPanel({ byLetter: { A: [], B: [] }, from: ACTIVE_FROM });
    expect(p.state).toBe('ready');
    expect(p.emptyReason).toBe('Formül için ortak zaman aralığında veri yok');
  });
});

describe('buildPanels — üretmeyen sorgular panel açmaz', () => {
  it('devre dışı sorgu atlanır (durum ne olursa olsun)', () => {
    const s: BuilderState = {
      ...defaultBuilderState(),
      queries: [{ ...q('A'), enabled: false }, q('B')],
    };
    const panels = buildPanels(s, { byLetter: {}, from: ACTIVE_FROM });
    expect(panels.map(p => p.letter)).toEqual(['B']);
  });

  it('metriksiz metric-source sorgusu atlanır', () => {
    const s: BuilderState = {
      ...defaultBuilderState(),
      queries: [blankQuery('A', 'metric'), q('B')],
    };
    const panels = buildPanels(s, { byLetter: {}, from: 0 });
    expect(panels.map(p => p.letter)).toEqual(['B']);
  });
});
