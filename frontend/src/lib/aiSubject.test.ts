import { describe, it, expect } from 'vitest';
import {
  formatAiParam, parseAiParam, aiSubjectTitle, aiSubjectSubtitle,
  AI_KINDS, type AISubject,
} from './aiSubject';

// v0.9.477 — AI-drawer'ın `?ai=` kodeği. Çekmece TEK yüzey olduğundan
// parse hatası = yanlış özne için LLM çağrısı (ya da boş çekmece). Pin'lenen
// sözleşme: her tür round-trip eder, ':' içeren id ayracı bozmaz, eksik/fazla
// segment ve bozuk yüzde-dizisi null döner (çekmece kapalı kalır).

const CASES: Array<{ name: string; subject: AISubject; param: string }> = [
  { name: 'trace',     subject: { kind: 'trace', id: '1b67a4f0c9de' },     param: 'trace:1b67a4f0c9de' },
  { name: 'problem',   subject: { kind: 'problem', id: 'p-42' },           param: 'problem:p-42' },
  { name: 'incident',  subject: { kind: 'incident', id: 'inc-7' },         param: 'incident:inc-7' },
  { name: 'anomaly',   subject: { kind: 'anomaly', id: 'an-9' },           param: 'anomaly:an-9' },
  { name: 'runbook',   subject: { kind: 'runbook', id: 'p-42' },           param: 'runbook:p-42' },
  { name: 'exception', subject: { kind: 'exception', id: 'fp0badc0ffee' }, param: 'exception:fp0badc0ffee' },
  { name: 'span',      subject: { kind: 'span', id: 'trace-1', spanId: 'span-2' }, param: 'span:trace-1:span-2' },
  {
    name: 'service-health',
    subject: { kind: 'service-health', id: 'checkout', fromNs: 1000, toNs: 2000 },
    param: 'service-health:checkout:1000:2000',
  },
];

describe('formatAiParam / parseAiParam round-trip', () => {
  for (const c of CASES) {
    it(`${c.name} round-trips`, () => {
      expect(formatAiParam(c.subject)).toBe(c.param);
      expect(parseAiParam(c.param)).toEqual(c.subject);
    });
  }

  it('covers every declared kind (yeni tür eklenince bu test düşer)', () => {
    expect(new Set(CASES.map(c => c.subject.kind))).toEqual(new Set(AI_KINDS));
  });
});

describe('parseAiParam — id encoding', () => {
  it("id içindeki ':' ayracı bozmaz", () => {
    const s: AISubject = { kind: 'service-health', id: 'ns:checkout', fromNs: 1, toNs: 2 };
    const p = formatAiParam(s);
    expect(p).toBe('service-health:ns%3Acheckout:1:2');
    expect(parseAiParam(p)).toEqual(s);
  });
  it('boşluk / yüzde / eğik çizgi taşıyan id round-trip eder', () => {
    for (const id of ['a b', '100%', 'a/b', 'çökme', 'a+b', 'a&b=c']) {
      const p = formatAiParam({ kind: 'exception', id });
      expect(parseAiParam(p)).toEqual({ kind: 'exception', id });
    }
  });
  it('bozuk yüzde-dizisi atmak yerine null döner', () => {
    expect(parseAiParam('trace:%E0%A4%A')).toBeNull();
  });
});

describe('parseAiParam — reddedilenler', () => {
  const bad = [
    ['null', null],
    ['undefined', undefined],
    ['boş', ''],
    ['bilinmeyen tür', 'metric:foo'],
    ['id yok', 'trace'],
    ['boş id', 'trace:'],
    ['basit türde fazla segment', 'trace:abc:def'],
    ['span spanId eksik', 'span:trace-1'],
    ['span boş spanId', 'span:trace-1:'],
    ['span fazla segment', 'span:trace-1:span-2:x'],
    ['service-health penceresiz', 'service-health:checkout'],
    ['service-health yarım pencere', 'service-health:checkout:1000'],
    ['service-health sayı değil', 'service-health:checkout:a:b'],
    ['service-health ters pencere', 'service-health:checkout:2000:1000'],
    ['service-health sıfır pencere', 'service-health:checkout:1000:1000'],
    ['service-health negatif başlangıç', 'service-health:checkout:-5:1000'],
  ] as const;
  for (const [name, raw] of bad) {
    it(`${name} → null`, () => expect(parseAiParam(raw)).toBeNull());
  }
});

describe('başlık yardımcıları', () => {
  it('her tür için başlık üretir', () => {
    for (const c of CASES) {
      expect(aiSubjectTitle(c.subject).length).toBeGreaterThan(0);
    }
  });
  it('uzun id kısaltılır, servis adı olduğu gibi kalır', () => {
    expect(aiSubjectSubtitle({ kind: 'trace', id: 'a'.repeat(32) })).toBe(`${'a'.repeat(16)}…`);
    expect(aiSubjectSubtitle({ kind: 'service-health', id: 'checkout-service-long', fromNs: 1, toNs: 2 }))
      .toBe('checkout-service-long');
    expect(aiSubjectSubtitle({ kind: 'span', id: 'trace-1', spanId: 'span-2' }))
      .toBe('trace-1 · span span-2');
  });
});
