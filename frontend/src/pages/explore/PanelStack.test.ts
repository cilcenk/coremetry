import { describe, expect, it } from 'vitest';
import { buildPanels, buildGhostSeries, IDLE_HINT, type PanelInputs } from './PanelStack';
import { blankQuery, defaultBuilderState, seriesGroupLabel, type BuilderState } from './model';
import type { SpanMetricSeries } from '@/lib/types';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

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
    // v0.9.1157 (VM Faz 2) — SUNUCUNUN sebebi varsayılan cümlenin YERİNE
    // geçer. Varsayılan bir TAVSİYE ("aralığı genişlet veya filtreleri
    // azalt") ve VictoriaMetrics'te bulunamayan bir `_bucket` serisi için
    // yanlış tavsiye: pencere ne kadar genişlerse genişlesin o seri yok.
    // Operatörü ölçmediği bir şeyi ölçmeye göndermek, boş panelden pahalı.
    {
      name: 'boş sonuç + sunucu notu → not, varsayılan cümlenin YERİNE geçer',
      inputs: {
        byLetter: { A: [] }, from: ACTIVE_FROM,
        noteByLetter: { A: 'm_bucket serisi bulunamadı — metrik histogram olmayabilir' },
      },
      wantState: 'ready',
      wantEmptyReason: 'm_bucket serisi bulunamadı — metrik histogram olmayabilir',
    },
    {
      name: 'not YOKSA varsayılan cümle korunur (CH kurulumundaki hâl)',
      inputs: {
        byLetter: { A: [] }, from: ACTIVE_FROM,
        noteByLetter: { A: undefined },
      },
      wantState: 'ready',
      wantEmptyReason: 'Bu pencerede veri yok — aralığı genişlet veya filtreleri azalt',
    },
    {
      // Not DOLU ama seri VAR: gerekçe hiç basılmaz. Sunucu boş-olmayan bir
      // sonuçla not göndermiyor, ama gönderse panel onu "veri yok" diye
      // göstermemeli — emptyReason yalnızca çizim alanı BOŞKEN bir şey der.
      name: 'seri varken not gerekçeye DÖNÜŞMEZ',
      inputs: {
        byLetter: { A: [series('x')] }, from: ACTIVE_FROM,
        noteByLetter: { A: 'bir not' },
      },
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

  // ── v0.9.809 — ÇÖZÜNÜRLÜK HİZASI ──────────────────────────────────────
  // formulaSeries bucket'ları KESİŞİMLE eşliyor, yani A 15 saniyelik ve B
  // 60 saniyelik bucket'larda gelse bile zaman damgaları örtüşür ve A+B
  // hesaplanır — farklı uzunlukta pencerelerde sayılmış değerleri
  // toplayarak. Boş panel değil, MAKUL GÖRÜNEN yanlış bir sayı.
  const gridSeries = (stepSec: number): SpanMetricSeries[] => ([{
    groupKey: ['x'],
    points: [0, 1, 2].map(i => ({
      time: 1_751_980_000_000_000_000 + i * stepSec * 1e9, value: 1,
    })),
  } as SpanMetricSeries]);

  it('🔴 harfler farklı çözünürlükte → formül HESAPLANMAZ, sebep yazılır', () => {
    const p = formulaPanel({
      byLetter: { A: gridSeries(15), B: gridSeries(60) },
      from: ACTIVE_FROM,
      stepByLetter: { A: 15, B: 60 },
    });
    expect(p.state).toBe('ready');
    expect(p.series).toEqual([]);
    expect(p.emptyReason).toContain('B 1dk');
    expect(p.emptyReason).toContain('15s');
  });

  it('aynı çözünürlük → formül eskisi gibi çizilir', () => {
    const p = formulaPanel({
      byLetter: { A: gridSeries(15), B: gridSeries(15) },
      from: ACTIVE_FROM,
      stepByLetter: { A: 15, B: 15 },
    });
    expect(p.state).toBe('ready');
    expect(p.series.length).toBe(1);
    expect(p.series[0].points.length).toBe(3);
  });

  it('step bilinmiyorsa (0) davranış BAYT BAYT eski — kapı sessiz kalır', () => {
    const p = formulaPanel({
      byLetter: { A: gridSeries(15), B: gridSeries(15) },
      from: ACTIVE_FROM,
      // stepByLetter hiç verilmedi (eski çağıranlar / bilinmeyen step).
    });
    expect(p.series.length).toBe(1);
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

// ── v0.9.824 — önceki dönem (hayalet) ───────────────────────────────────────
//
// Hayalet iki şeyi vaat ediyor: (1) çizgi BUGÜNÜN eksenine bindirilmiş,
// (2) bindirme BİREBİR. İkisinden biri sessizce bozulursa grafik yine
// çizilir ve karşılaştırma yanlış yerde ya da yanlış ölçekte okunur.
// Bu testler ikisini de saf tarafta sabitliyor.

const HOUR_NS = 3600 * 1e9;
const GHOST_OFFSET = 24 * HOUR_NS;

// Etiketler PRODUCTION türetmesinden gelir (seriesGroupLabel) — elle
// yazılmış bir etiket, eşleşme kapısını gerçekte sınamazdı.
const GQ = blankQuery('A');
const lbl = (l: string) => seriesGroupLabel(GQ, [l], 'd');

// shownSeries — buildGhostSeries'e giden "panelde görünen" seri.
const shownSeries = (label: string) => ({
  label: lbl(label), color: '#000',
  points: [{ time: ACTIVE_FROM, value: 10 }],
});

describe('buildGhostSeries — bindirme + kapılar', () => {
  const qA = blankQuery('A');

  it('noktalar +offset ile bugünün eksenine kayar (BİREBİR)', () => {
    const cmp: SpanMetricSeries[] = [{
      groupKey: ['x'],
      points: [
        { time: ACTIVE_FROM - GHOST_OFFSET, value: 4 },
        { time: ACTIVE_FROM - GHOST_OFFSET + 60e9, value: 6 },
      ],
    } as SpanMetricSeries];
    const g = buildGhostSeries(qA, 'desc', [shownSeries('x')], cmp, 60, 60, GHOST_OFFSET);
    expect(g.note).toBeUndefined();
    expect(g.ghosts).toHaveLength(1);
    // Kaydırılmış zamanlar GÜNCEL pencerenin zamanlarıyla BİREBİR aynı.
    expect(g.ghosts[0].points.map(p => p.time))
      .toEqual([ACTIVE_FROM, ACTIVE_FROM + 60e9]);
    expect(g.ghosts[0].points.map(p => p.value)).toEqual([4, 6]);
    // Etiket ekSİZ — " (önceki)" ekini CorePanelMulti basar, biz değil
    // (iki taraf da basarsa "x (önceki) (önceki)" olurdu).
    expect(g.ghosts[0].label).toBe(lbl('x'));
  });

  it('offset 0 (karşılaştırma kapalı) → hayalet de not da yok', () => {
    const cmp = [{ groupKey: ['x'], points: [{ time: 1, value: 1 }] }] as SpanMetricSeries[];
    expect(buildGhostSeries(qA, 'd', [shownSeries('x')], cmp, 60, 60, 0))
      .toEqual({ ghosts: [] });
  });

  it('önceki dönem YÜKLENİYOR (undefined) → not YOK (henüz söylenecek bir şey yok)', () => {
    expect(buildGhostSeries(qA, 'd', [shownSeries('x')], undefined, 60, 60, GHOST_OFFSET))
      .toEqual({ ghosts: [] });
  });

  it('ÇÖZÜNÜRLÜK uyuşmazlığı → hayalet ÇİZİLMEZ ve sebebi yazılır', () => {
    const cmp = [{ groupKey: ['x'], points: [{ time: 1, value: 1 }] }] as SpanMetricSeries[];
    const g = buildGhostSeries(qA, 'd', [shownSeries('x')], cmp, 15, 60, GHOST_OFFSET);
    expect(g.ghosts).toHaveLength(0);
    expect(g.note).toContain('1dk');
    expect(g.note).toContain('15s');
    expect(g.note).toContain('ölçek');
  });

  it('step BİLİNMİYORSA (0) düşürülmez — bilmemek uyuşmamak değildir', () => {
    const cmp = [{
      groupKey: ['x'], points: [{ time: ACTIVE_FROM - GHOST_OFFSET, value: 3 }],
    }] as SpanMetricSeries[];
    for (const [cur, prev] of [[0, 60], [60, 0], [0, 0]]) {
      const g = buildGhostSeries(qA, 'd', [shownSeries('x')], cmp, cur, prev, GHOST_OFFSET);
      expect(g.ghosts).toHaveLength(1);
      expect(g.note).toBeUndefined();
    }
  });

  it('önceki dönemde HİÇ veri yok → boş liste + açık cümle', () => {
    const g = buildGhostSeries(qA, 'd', [shownSeries('x')], [], 60, 60, GHOST_OFFSET);
    expect(g.ghosts).toHaveLength(0);
    expect(g.note).toBe('önceki dönemde veri yok');
  });

  it('yalnız GÖRÜNEN etiketlerin hayaleti çizilir (top-N hayalete de uygulanır)', () => {
    const cmp = ['x', 'y', 'z'].map(l => ({
      groupKey: [l], points: [{ time: ACTIVE_FROM - GHOST_OFFSET, value: 1 }],
    })) as SpanMetricSeries[];
    const g = buildGhostSeries(qA, 'd', [shownSeries('x'), shownSeries('z')], cmp, 60, 60, GHOST_OFFSET);
    expect(g.ghosts.map(s => s.label)).toEqual([lbl('x'), lbl('z')]);
  });

  it('hiçbir etiket eşleşmiyorsa boş + sebep', () => {
    const cmp = [{ groupKey: ['eski'], points: [{ time: 1, value: 1 }] }] as SpanMetricSeries[];
    const g = buildGhostSeries(qA, 'd', [shownSeries('yeni')], cmp, 60, 60, GHOST_OFFSET);
    expect(g.ghosts).toHaveLength(0);
    expect(g.note).toContain('eşleşmedi');
  });

  it('GÜNCEL pencerede seri yoksa not da YOK (panel zaten "veri yok" diyor)', () => {
    const cmp = [{ groupKey: ['x'], points: [{ time: 1, value: 1 }] }] as SpanMetricSeries[];
    expect(buildGhostSeries(qA, 'd', [], cmp, 60, 60, GHOST_OFFSET))
      .toEqual({ ghosts: [] });
  });
});

describe('buildPanels — hayalet eşlemesi ve FORMÜL MUAFİYETİ', () => {
  const cmpSeries = (label: string, value: number): SpanMetricSeries => ({
    groupKey: [label],
    points: [{ time: ACTIVE_FROM - GHOST_OFFSET, value }],
  } as SpanMetricSeries);

  const inputs = (over: Partial<PanelInputs> = {}): PanelInputs => ({
    byLetter: { A: [series('x', 10)], B: [series('x', 20)] },
    from: ACTIVE_FROM,
    stepByLetter: { A: 60, B: 60 },
    compareByLetter: { A: [cmpSeries('x', 5)], B: [cmpSeries('x', 8)] },
    compareStepByLetter: { A: 60, B: 60 },
    compareOffsetNs: GHOST_OFFSET,
    ...over,
  });

  it('her harf panelinde hayalet var ve bugünün eksenine binmiş', () => {
    const panels = buildPanels(stateWith(['A', 'B']), inputs());
    for (const p of panels) {
      expect(p.ghosts).toHaveLength(1);
      expect(p.ghosts![0].points[0].time).toBe(ACTIVE_FROM);
    }
  });

  it('FORMÜL paneli hayalet TAŞIMAZ (formül yalnız güncel)', () => {
    const panels = buildPanels(stateWith(['A', 'B'], 'A / B'), inputs());
    const f = panels.find(p => p.isFormula)!;
    expect(f.ghosts).toBeUndefined();
    expect(f.compareNote).toBeUndefined();
    // …ama harflerin kendi panelleri hayaleti taşımaya devam eder.
    expect(panels.filter(p => !p.isFormula).every(p => p.ghosts?.length === 1)).toBe(true);
  });

  it('karşılaştırma KAPALIYKEN hiçbir panelde hayalet/not yok', () => {
    const panels = buildPanels(stateWith(['A', 'B'], 'A / B'), inputs({
      compareByLetter: {}, compareStepByLetter: {}, compareOffsetNs: 0,
    }));
    for (const p of panels) {
      expect(p.ghosts).toBeUndefined();
      expect(p.compareNote).toBeUndefined();
    }
  });

  it('bir harfin çözünürlüğü uyuşmazsa YALNIZ o harf düşer', () => {
    const panels = buildPanels(stateWith(['A', 'B']), inputs({
      compareStepByLetter: { A: 15, B: 60 },
    }));
    const a = panels.find(p => p.letter === 'A')!;
    const b = panels.find(p => p.letter === 'B')!;
    expect(a.ghosts).toBeUndefined();
    expect(a.compareNote).toContain('hayalet çizilmedi');
    expect(b.ghosts).toHaveLength(1);
    expect(b.compareNote).toBeUndefined();
  });

  it('hayalet, ANA serilerin sayısını / sırasını değiştirmez', () => {
    const withCmp = buildPanels(stateWith(['A']), inputs());
    const without = buildPanels(stateWith(['A']), inputs({
      compareByLetter: {}, compareOffsetNs: 0,
    }));
    expect(withCmp[0].series).toEqual(without[0].series);
    expect(withCmp[0].more).toBe(without[0].more);
  });
});

// ── The note's delivery chain (v0.9.1157, VM Faz 2) ────────────────────────
//
// buildPanels' own tests above prove the note is USED once it arrives. They
// cannot prove it ARRIVES: the hook that fetches it (useExploreQueries) reads
// React Query results and has no pure surface to call. A mutation check
// confirmed the gap — deleting the forwarding line left every explore test
// green, which is the exact failure this chain is prone to (three layers,
// each with a passing test, one missing link between them).
//
// So the forwarding half is scanned from SOURCE. Mechanical, and precise about
// the one thing that goes wrong.
describe("the server's empty-reason reaches buildPanels (v0.9.1157)", () => {
  const hook = readFileSync(resolve(__dirname, 'useExploreQueries.ts'), 'utf8');

  it('the metric fetch branch forwards the response note', () => {
    // Vacuous-pass guard first: if the branch was renamed, the assertions
    // below would scan text that no longer describes the fetch.
    expect(hook, 'api.metricQueryFull branch not found — update this scan, do not delete it')
      .toContain('api.metricQueryFull(');
    expect(hook, 'the metric branch drops r.note, so a VictoriaMetrics percentile with no ' +
      '_bucket series renders as "widen the range" — advice that cannot work')
      .toContain('note: r?.note');
  });

  it('the per-letter map is built and returned', () => {
    expect(hook).toContain('noteByLetter[q.letter] = r.data?.note');
    // Returned from the hook, or buildPanels never sees it.
    expect(hook).toMatch(/return \{[^}]*noteByLetter/s);
  });

  it('Explore.tsx passes it into buildPanels', () => {
    const page = readFileSync(resolve(__dirname, '..', 'Explore.tsx'), 'utf8');
    // Destructured from the hook AND handed to buildPanels — two separate
    // places, and dropping either one is silent.
    expect((page.match(/noteByLetter/g) ?? []).length,
      'noteByLetter must appear in the hook destructure, the buildPanels input and the memo deps')
      .toBeGreaterThanOrEqual(3);
  });
});
