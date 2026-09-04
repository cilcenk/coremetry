import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { traceColumnOrder, FIXED_COLS, DEFAULT_TRACE_COLUMNS } from './traceColumns';

// traceColumns.test.ts — v0.9.841. Pins the /traces column order.
//
//   v0.10.340 (operatör: "Start time'dan sonra Service sonra Name"):
//   Start time(time) · Service · Name(operation) · <attributes> ·
//   Duration · Status · Spans
//   v0.10.220 (operatör: "Start time en solda"):
//   Start time(time) · Name(operation) · Service · <attributes> ·
//   Duration · Status · Spans
//   (v0.10.217 Dynatrace düzeni Start time'ı en sağa koymuştu; v0.9.841 →
//   v0.10.216: Time · Service · Operation · <attributes> · Duration ·
//   Spans · Status — attr kolonlarının kimlik alanlarından hemen sonra
//   durması o karardan KALIYOR.)
//
// Worth a test because the header and the body cells both derive from
// this array. If it ever emitted a column twice, or dropped one, the
// two would still agree with EACH OTHER while disagreeing with reality
// — a table printing the wrong value under the right heading, which no
// type checks and no eye catches on a page of 50 rows.

describe('traceColumnOrder', () => {
  const cases: Array<[string, string[], string[]]> = [
    [
      'no extras — the six fixed columns in canonical order',
      [],
      ['time', 'service', 'operation', 'duration', 'status', 'spans'],
    ],
    [
      'the operator default set lands between Service and Duration',
      ['openshift.cluster.name', 'channel_code', 'function_code', 'http.status_code'],
      [
        'time', 'service', 'operation',
        'openshift.cluster.name', 'channel_code', 'function_code', 'http.status_code',
        'duration', 'status', 'spans',
      ],
    ],
    [
      'one extra',
      ['pod'],
      ['time', 'service', 'operation', 'pod', 'duration', 'status', 'spans'],
    ],
    [
      'extras keep the order they were added in',
      ['z', 'a', 'm'],
      ['time', 'service', 'operation', 'z', 'a', 'm', 'duration', 'status', 'spans'],
    ],
  ];
  for (const [name, extras, want] of cases) {
    it(name, () => {
      expect(traceColumnOrder(extras)).toEqual(want);
    });
  }

  it('never leaks a fixed column into the attribute slot', () => {
    // The filter must exclude ALL THREE leading columns (time, operation,
    // service). Getting that wrong duplicates one of them — the regression
    // this whole helper exists to make visible.
    const ids = traceColumnOrder([]);
    expect(new Set(ids).size).toBe(ids.length);
    for (const c of FIXED_COLS) {
      expect(ids.filter(x => x === c)).toHaveLength(1);
    }
  });

  it('emits every fixed column exactly once, extras or not', () => {
    const ids = traceColumnOrder(DEFAULT_TRACE_COLUMNS);
    for (const c of FIXED_COLS) {
      expect(ids.filter(x => x === c)).toHaveLength(1);
    }
    expect(ids).toHaveLength(FIXED_COLS.length + DEFAULT_TRACE_COLUMNS.length);
  });

  it('does not mutate the caller array', () => {
    const extras = ['pod'];
    traceColumnOrder(extras);
    expect(extras).toEqual(['pod']);
  });
});

describe('DEFAULT_TRACE_COLUMNS', () => {
  it('is the operator-requested set, in the requested order', () => {
    // v0.9.1360 `function_id` ekledi; v0.9.1361 aynı gün operatör
    // isteğiyle `http.status_code`'u ÇIKARDI. Küme yine dört ve
    // operatörün fiilen kullandığının aynısı. status_code'un satırdaki
    // bilgisi zaten Status kolonunda (OK/ERROR) — ikinci kez, daha dar
    // biçimde gösteriliyordu.
    expect(DEFAULT_TRACE_COLUMNS).toEqual([
      'openshift.cluster.name', 'channel_code', 'function_code', 'function_id',
    ]);
  });

  it('fits under the 8-column ceiling with room to add', () => {
    expect(DEFAULT_TRACE_COLUMNS.length).toBeLessThan(8);
  });

  // v0.9.1360 — GENİŞLİK BÜTÇESİ. Traces.tsx'in COL_W şerhi bir aritmetik
  // iddia ediyor; v0.9.841 varsayılanı ikiden dörde çıkardığında o şerh
  // güncellenmedi ve bütçe SESSİZCE 264px aşıldı. Operatörün "kolonlar
  // sığmıyor" şikâyeti o aşımdı. Bu test aritmetiği çivileyerek aynı
  // sessiz aşımı imkânsız kılıyor: varsayılan kolon sayısı artarsa ya
  // genişlikler kısılır ya bu test kırılır.
  it('varsayılan küme + sabit kolonlar ölçülen bütçeyi aşmaz', () => {
    // Traces.tsx COL_W ile AYNI değerler; ıraksarsa bu test yalan söyler,
    // o yüzden kaynaktan okunuyor.
    const src = readFileSync(resolve(__dirname, '..', 'pages', 'Traces.tsx'), 'utf8');
    const m = src.match(/const COL_W[^=]*=\s*\{([^}]*)\}/);
    expect(m, 'COL_W bulunamadı — Traces.tsx yeniden düzenlenmiş olabilir').toBeTruthy();
    const fixed = [...m![1].matchAll(/(\w+)\s*:\s*(\d+)/g)]
      .reduce((s, x) => s + Number(x[2]), 0);
    // v0.10.216 — ▸ ön-izleme kolonu (30px öncü hücre) silindi; öncü
    // pay artık SIFIR. Traces.tsx'te leading prop'u geri gelirse bu
    // sabit de geri gelmeli (tracesRowLink.test.ts onu ayrıca yasaklıyor).
    const ATTR_W = 130, LEADING = 0;
    const total = fixed + LEADING + DEFAULT_TRACE_COLUMNS.length * ATTR_W;

    // 1440px dizüstü: ~220 kenar çubuğu + ~40 dolgu → ~1180 kalıyor.
    // Kaba-sığdırma (v0.9.1334) aşımı KAYMA yerine SIKIŞMAYA çeviriyor,
    // o yüzden tavan mutlak değil: sıkışmanın okunabilir kalacağı bir
    // pay. %20'den fazla aşım, her kolonun beşte bir daralması demek.
    expect(total).toBeLessThanOrEqual(Math.round(1180 * 1.2));
  });
});
