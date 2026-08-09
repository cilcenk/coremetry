import { describe, it, expect } from 'vitest';
import { topBubble } from './RootCausePanel';
import type { RootCause } from '@/lib/types';

// v0.9.836 regresyon testi — OPERATÖR BİLDİRİMİ.
//
// BULUNAN HATA: `/problems?problem=<id>` tıkında ErrorBoundary
// "Cannot read properties of null (reading 'length')" — sayfanın
// TAMAMI çöküyordu.
//
// Kök neden: chstore/bubbleup.go'nun erken dönüşleri (selTotal==0 ||
// baseTotal==0, ve len(keys)==0) `&BubbleUpResult{...}` döndürüyor ama
// `Attributes` alanını hiç doldurmuyordu. Go'da nil dilim JSON'a `null`
// çıkar — `[]` değil — ve topBubble `rc.bubbleUp.attributes.length`
// derken orada patlıyordu.
//
// Tetikleyici koşul sıradan: analiz penceresi boş kalan HER error-tipli
// problem (eski/çözülmüş kayıt, derin link). Yani çökme rastlantısal
// değil, DETERMİNİSTİKTİ — o problemlere her tıklayışta.
//
// Tipler bunu yakalayamazdı: `attributes: BubbleUpAttribute[]` şekli
// null'ı DIŞLAR, ama JSON sınırında tip kontrolü YOKTUR — sunucu ne
// gönderirse o gelir. Bu yüzden düzeltme iki taraflı (backend dürüst
// boş dizi + burada tolerans) ve test null'ı AÇIKÇA besliyor.
//
// `as unknown as RootCause` bilinçli: testin amacı tam olarak tipin
// yalan söylediği durumu kurmak. `as any` değil (ev kuralı) — iki
// aşamalı dönüşüm niyeti okunur kılıyor.

const base = {
  problemId: 'p1',
  service: 'checkout',
  metric: 'error_rate',
  startedAt: 0,
  fromNs: 0,
  toNs: 60e9,
  correlations: [],
};

describe('topBubble — null dizileri (v0.9.836)', () => {
  const cases: Array<{ name: string; bubbleUp: unknown; wantKey: string | null }> = [
    {
      name: 'bubbleUp HİÇ YOK → null',
      bubbleUp: undefined,
      wantKey: null,
    },
    {
      name: 'bubbleUp var, attributes NULL → null (ÇÖKMEDEN)',
      // ÇÖKMENİN TA KENDİSİ: sunucudan gelen gerçek şekil.
      bubbleUp: { selectionTotal: 0, baselineTotal: 0, attributes: null },
      wantKey: null,
    },
    {
      name: 'attributes BOŞ DİZİ → null',
      bubbleUp: { selectionTotal: 10, baselineTotal: 100, attributes: [] },
      wantKey: null,
    },
    {
      name: 'attribute var ama values NULL → null (iç seviye de tolere)',
      bubbleUp: {
        selectionTotal: 10, baselineTotal: 100,
        attributes: [{ key: 'http.status_code', values: null }],
      },
      wantKey: null,
    },
    {
      name: 'değerlerin hepsi skorsuz → null (düz dağılım açıklayıcı değil)',
      bubbleUp: {
        selectionTotal: 10, baselineTotal: 100,
        attributes: [{ key: 'k', values: [{ value: 'a', score: 0, selectionPct: 1, baselinePct: 1 }] }],
      },
      wantKey: null,
    },
    {
      name: 'gerçek attribute → EN YÜKSEK skorlu anahtar',
      bubbleUp: {
        selectionTotal: 10, baselineTotal: 100,
        attributes: [
          { key: 'zayıf', values: [{ value: 'x', score: 0.1, selectionPct: 20, baselinePct: 10 }] },
          { key: 'güçlü', values: [{ value: 'y', score: 0.8, selectionPct: 90, baselinePct: 10 }] },
        ],
      },
      wantKey: 'güçlü',
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const rc = { ...base, bubbleUp: c.bubbleUp } as unknown as RootCause;
      const got = topBubble(rc);
      expect(got?.key ?? null).toBe(c.wantKey);
    });
  }

  it('değerler skora göre AZALAN sıralı döner', () => {
    const rc = {
      ...base,
      bubbleUp: {
        selectionTotal: 10, baselineTotal: 100,
        attributes: [{
          key: 'k',
          values: [
            { value: 'düşük', score: 0.2, selectionPct: 30, baselinePct: 10 },
            { value: 'yüksek', score: 0.9, selectionPct: 95, baselinePct: 5 },
          ],
        }],
      },
    } as unknown as RootCause;
    expect(topBubble(rc)?.values.map(v => v.value)).toEqual(['yüksek', 'düşük']);
  });

  it('kaynak dizi MUTASYONA UĞRAMAZ (sıralama kopya üzerinde)', () => {
    const values = [
      { value: 'düşük', score: 0.2, selectionPct: 30, baselinePct: 10 },
      { value: 'yüksek', score: 0.9, selectionPct: 95, baselinePct: 5 },
    ];
    const rc = {
      ...base,
      bubbleUp: { selectionTotal: 10, baselineTotal: 100, attributes: [{ key: 'k', values }] },
    } as unknown as RootCause;
    topBubble(rc);
    expect(values.map(v => v.value)).toEqual(['düşük', 'yüksek']);
  });
});
