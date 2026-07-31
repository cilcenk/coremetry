import { describe, expect, it } from 'vitest';
import type { AISubject } from '@/lib/aiSubject';
import {
  EXPLAIN_CONTEXT_MAX, aiSubjectQuestion, buildExplainContext, clampExplain, drawerFollowups,
} from './drawerChat';

// v0.9.479 pinleri — AI çekmecesi sohbetinin bağlam devri.
// Operatör raporu: çekmecedeki "Chat'te devam et" global CoSRE
// penceresini açıyordu ve sohbet ekrandaki açıklamayı bilmiyordu
// (ekranda exception dururken "bu trace ID'ye ait veri yok" + filo
// geneli JVM GC anlatımı). Devir metnini bu testler pinler.

const exceptionSubject: AISubject = { kind: 'exception', id: 'fp-abc123' };
const traceSubject: AISubject = { kind: 'trace', id: 'a1b2c3d4e5f60718293a4b5c6d7e8f90' };
const spanSubject: AISubject = { kind: 'span', id: 'a'.repeat(32), spanId: 'b'.repeat(16) };
const svcSubject: AISubject = { kind: 'service-health', id: 'checkout', fromNs: 1, toNs: 2 };

describe('buildExplainContext', () => {
  it('künye + metin taşır (model konuyu bilir)', () => {
    const ctx = buildExplainContext({ subject: exceptionSubject, text: 'NPE checkout serviste, 34 kez' });
    expect(ctx).toContain('KONU: Explain root cause — exception grubu fp-abc123');
    expect(ctx).toContain('NPE checkout serviste, 34 kez');
  });

  it('künyede TAM id geçer (kısaltma model için işe yaramaz)', () => {
    const ctx = buildExplainContext({ subject: traceSubject, text: 'yavaş db çağrısı' });
    expect(ctx).toContain(`trace ${traceSubject.id}`);
    expect(ctx).not.toContain('…');
  });

  it('span öznesi hem trace hem span id taşır', () => {
    const ctx = buildExplainContext({ subject: spanSubject, text: 'timeout' });
    expect(ctx).toContain(`trace ${'a'.repeat(32)} · span ${'b'.repeat(16)}`);
  });

  it('boş/boşluk metin → boş bağlam (sohbet açılmaz)', () => {
    expect(buildExplainContext({ subject: exceptionSubject, text: '' })).toBe('');
    expect(buildExplainContext({ subject: exceptionSubject, text: '  \n ' })).toBe('');
  });

  it('kanıt id\'leri eklenir ve 10 ile sınırlanır', () => {
    const traceIds = Array.from({ length: 25 }, (_, i) => `t${i}`);
    const ctx = buildExplainContext({ subject: exceptionSubject, text: 'NPE', traceIds, spanIds: ['s1', 's2'] });
    expect(ctx).toContain("Kanıt span'leri: s1, s2");
    expect(ctx).toContain("Kanıt trace'leri: t0, t1, t2, t3, t4, t5, t6, t7, t8, t9");
    expect(ctx).not.toContain('t10');
  });

  it('kanıt yoksa satır hiç yazılmaz', () => {
    const ctx = buildExplainContext({ subject: exceptionSubject, text: 'NPE', traceIds: [], spanIds: [] });
    expect(ctx).not.toContain('Kanıt');
  });

  it('uzun açıklama kırpılır (POST + küçük model bağlam bütçesi)', () => {
    const ctx = buildExplainContext({ subject: exceptionSubject, text: 'ğ'.repeat(EXPLAIN_CONTEXT_MAX + 500) });
    expect([...ctx].length).toBe(EXPLAIN_CONTEXT_MAX + [...'\n…(kısaltıldı)'].length);
    expect(ctx.endsWith('(kısaltıldı)')).toBe(true);
  });
});

describe('clampExplain', () => {
  // Sınır davranışı tablo hâlinde — çok-baytlı (Türkçe) metinde kesme
  // RUNE bazlı olmalı, aksi hâlde karakter ortadan bölünür.
  const suffixRunes = [...'\n…(kısaltıldı)'].length;
  const cases: Array<{ name: string; input: string; runes: number; trunc: boolean }> = [
    { name: 'boş', input: '', runes: 0, trunc: false },
    { name: 'kısa', input: 'NPE', runes: 3, trunc: false },
    { name: 'tam sınır', input: 'a'.repeat(EXPLAIN_CONTEXT_MAX), runes: EXPLAIN_CONTEXT_MAX, trunc: false },
    { name: 'sınır+1', input: 'a'.repeat(EXPLAIN_CONTEXT_MAX + 1), runes: EXPLAIN_CONTEXT_MAX + suffixRunes, trunc: true },
    { name: 'çok-baytlı', input: 'ğ'.repeat(EXPLAIN_CONTEXT_MAX + 9), runes: EXPLAIN_CONTEXT_MAX + suffixRunes, trunc: true },
  ];
  for (const c of cases) {
    it(c.name, () => {
      const out = clampExplain(c.input);
      expect([...out].length).toBe(c.runes);
      expect(out.endsWith('(kısaltıldı)')).toBe(c.trunc);
    });
  }
});

describe('aiSubjectQuestion', () => {
  // Seed sorusu tele ilk kullanıcı turu olarak gider; SERVİS öznesinde
  // servis adını taşıması şart — sunucudaki takip-devralma (v0.9.410)
  // ancak o zaman "peki hata logları?"nı gerçek telemetriye oturtur.
  it('service-health seed\'i servis adını taşır', () => {
    expect(aiSubjectQuestion('service-health', 'checkout')).toBe('checkout servisinin sağlığı nasıl?');
  });

  it('her özne tipi boş olmayan bir soru üretir', () => {
    const kinds = ['trace', 'span', 'problem', 'incident', 'anomaly', 'service-health', 'runbook', 'exception'] as const;
    for (const k of kinds) {
      const q = aiSubjectQuestion(k, 'X1');
      expect(q.length).toBeGreaterThan(0);
      expect(q).toContain('X1');
    }
  });
});

describe('drawerFollowups', () => {
  it('servis öznesinde çipler servis adını taşır (guided\'a oturur)', () => {
    const chips = drawerFollowups(svcSubject);
    expect(chips).toHaveLength(3);
    for (const c of chips) expect(c).toContain('checkout');
  });

  it('diğer öznelerde konu-kapsamlı takipler', () => {
    const chips = drawerFollowups(exceptionSubject);
    expect(chips).toContain('Bu neden oluyor?');
    // Global chat'in filo çipleri çekmeceye sızmamalı.
    expect(chips.join(' ')).not.toContain('Takımımın');
  });
});
